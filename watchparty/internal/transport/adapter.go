package transport

import (
	"context"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

const (
	Channel            = "watchparty/sync/v2"
	maxPeers           = 32
	ioTimeout          = 10 * time.Second
	negotiationTimeout = 30 * time.Second
)

// Peer carries the same ordered, reliable data stream used by the room protocol.
type Peer struct {
	PeerID    string
	ChannelID string
	Conn      io.ReadWriteCloser
}

type trackedConn struct {
	io.ReadWriteCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (c *trackedConn) Close() error {
	err := c.ReadWriteCloser.Close()
	c.once.Do(c.cancel)
	return err
}

type Adapter struct {
	ctx           context.Context
	cancel        context.CancelFunc
	address       string
	id            string
	cipher        cipher.AEAD
	api           *webrtc.API
	configuration webrtc.Configuration
	peers         chan *Peer
	status        chan bool
	done          chan struct{}
	started       atomic.Bool
	retryDelay    time.Duration
}

func New(ctx context.Context, address, password string, ice []string) (*Adapter, error) {
	aead, err := signalingCipher(password)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	settings := webrtc.SettingEngine{}
	settings.DetachDataChannels()
	config := webrtc.Configuration{}
	if len(ice) != 0 {
		config.ICEServers = []webrtc.ICEServer{{URLs: ice}}
	}
	return &Adapter{
		ctx: ctx, cancel: cancel, address: address, id: uuid.NewString(),
		cipher: aead, api: webrtc.NewAPI(webrtc.WithSettingEngine(settings)),
		configuration: config, peers: make(chan *Peer), status: make(chan bool, 8), done: make(chan struct{}),
		retryDelay: 2 * time.Second,
	}, nil
}

func (a *Adapter) ID() string           { return a.id }
func (a *Adapter) Accept() <-chan *Peer { return a.peers }
func (a *Adapter) Status() <-chan bool  { return a.status }

// Open verifies the initial WebSocket connection before reporting success.
// Further connections keep the same peer ID until the room is closed.
func (a *Adapter) Open() error {
	if !a.started.CompareAndSwap(false, true) {
		return errors.New("transport already opened")
	}
	conn, err := a.dial()
	if err != nil {
		a.cancel()
		close(a.done)
		return err
	}
	go a.run(conn)
	return nil
}

func (a *Adapter) Close() {
	a.cancel()
	if a.started.Load() {
		<-a.done
	}
}

func (a *Adapter) dial() (*websocket.Conn, error) {
	ctx, cancel := context.WithTimeout(a.ctx, ioTimeout)
	defer cancel()
	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, a.address, nil)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		// Do not expose the address: it contains the community password.
		return nil, errors.New("signaler connection failed")
	}
	return conn, nil
}

func (a *Adapter) run(conn *websocket.Conn) {
	defer close(a.done)
	defer close(a.peers)
	for {
		a.session(conn)
		if a.ctx.Err() == nil {
			select {
			case a.status <- false:
			default:
			}
		}
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-time.After(a.retryDelay):
			}
			var err error
			conn, err = a.dial()
			if err == nil {
				select {
				case a.status <- true:
				default:
				}
				break
			}
		}
	}
}

type remote struct {
	ctx    context.Context
	cancel context.CancelFunc
	input  chan signal
}

// A session owns its peer map; each peer has a single negotiation worker.
// WebSocket writes have a single writer and all queues are bounded.
func (a *Adapter) session(conn *websocket.Conn) {
	ctx, cancel := context.WithCancel(a.ctx)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	var workers sync.WaitGroup
	defer func() {
		cancel()
		conn.Close()
		workers.Wait()
		stop()
	}()
	conn.SetReadLimit(256 << 10)
	conn.SetReadDeadline(time.Now().Add(2 * ioTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(2 * ioTimeout))
	})
	out := make(chan signal, 64)
	send := func(msg signal) error {
		msg.From = a.id
		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- msg:
			return nil
		default:
			cancel()
			return errors.New("signaling queue full")
		}
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		defer cancel()
		ticker := time.NewTicker(ioTimeout / 2)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-out:
				plain, err := json.Marshal(msg)
				if err != nil {
					return
				}
				conn.SetWriteDeadline(time.Now().Add(ioTimeout))
				if err := conn.WriteMessage(websocket.TextMessage, a.cipher.Seal(nil, nil, plain, nil)); err != nil {
					return
				}
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(ioTimeout)); err != nil {
					return
				}
			}
		}
	}()
	if send(signal{Type: "introduction"}) != nil {
		return
	}
	remotes := make(map[string]*remote)
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		plain, err := a.cipher.Open(nil, nil, data, nil)
		if err != nil {
			continue // Includes truncated messages and incorrect passwords.
		}
		var msg signal
		if json.Unmarshal(plain, &msg) != nil || msg.From == a.id || msg.From == "" || len(msg.From) > 128 || (msg.To != "" && msg.To != a.id) {
			continue
		}
		if msg.Type != "introduction" && msg.To != a.id {
			continue
		}
		// Only the lower ID offers. A targeted introduction lets a newcomer
		// discover an existing higher ID without simultaneous-offer glare.
		if msg.Type == "introduction" && a.id > msg.From {
			if msg.To == "" {
				send(signal{Type: "introduction", To: msg.From})
			}
			continue
		}
		if msg.Type == "offer" && msg.From > a.id {
			continue
		}
		r := remotes[msg.From]
		if r != nil && r.ctx.Err() != nil {
			delete(remotes, msg.From)
			r = nil
		}
		if r == nil {
			if msg.Type != "introduction" && msg.Type != "offer" {
				continue
			}
			for id, previous := range remotes {
				if previous.ctx.Err() != nil {
					delete(remotes, id)
				}
			}
			if len(remotes) >= maxPeers {
				continue
			}
			rctx, rcancel := context.WithCancel(ctx)
			r = &remote{ctx: rctx, cancel: rcancel, input: make(chan signal, 32)}
			remotes[msg.From] = r
			workers.Add(1)
			go func(id string, r *remote) {
				defer workers.Done()
				defer r.cancel()
				a.connectPeer(id, r, send)
				// Reintroduce ourselves after a failed data channel, even when the
				// signaler WebSocket has remained healthy throughout the failure.
				select {
				case <-ctx.Done():
				case <-time.After(a.retryDelay):
					send(signal{Type: "introduction", To: id})
				}
			}(msg.From, r)
		}
		select {
		case r.input <- msg:
		default:
			r.cancel()
		}
	}
}

func (a *Adapter) connectPeer(id string, r *remote, send func(signal) error) {
	pc, err := a.api.NewPeerConnection(a.configuration)
	if err != nil {
		return
	}
	defer pc.Close()
	timer := time.AfterFunc(negotiationTimeout, r.cancel)
	defer timer.Stop()
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateDisconnected {
			r.cancel()
		}
	})
	var delivered atomic.Bool
	bind := func(dc *webrtc.DataChannel) {
		if dc.Label() != Channel || !dc.Ordered() || dc.MaxRetransmits() != nil || dc.MaxPacketLifeTime() != nil {
			dc.Close()
			return
		}
		dc.OnOpen(func() {
			if !delivered.CompareAndSwap(false, true) {
				dc.Close()
				return
			}
			stream, err := dc.Detach()
			if err != nil {
				r.cancel()
				return
			}
			timer.Stop()
			conn := &trackedConn{ReadWriteCloser: stream, cancel: r.cancel}
			select {
			case <-r.ctx.Done():
				conn.Close()
			case a.peers <- &Peer{PeerID: id, ChannelID: dc.Label(), Conn: conn}:
			}
		})
		dc.OnClose(r.cancel)
	}
	pc.OnDataChannel(bind)
	localDescription := func(desc webrtc.SessionDescription) error {
		gathered := webrtc.GatheringCompletePromise(pc)
		if err := pc.SetLocalDescription(desc); err != nil {
			return err
		}
		select {
		case <-r.ctx.Done():
			return r.ctx.Err()
		case <-gathered:
		}
		payload, err := json.Marshal(pc.LocalDescription())
		if err != nil {
			return err
		}
		return send(signal{Type: desc.Type.String(), To: id, Payload: payload})
	}
	for {
		select {
		case <-r.ctx.Done():
			return
		case msg := <-r.input:
			switch msg.Type {
			case "introduction":
				if pc.LocalDescription() != nil {
					continue
				}
				dc, err := pc.CreateDataChannel(Channel, nil)
				if err != nil {
					return
				}
				bind(dc)
				offer, err := pc.CreateOffer(nil)
				if err != nil || localDescription(offer) != nil {
					return
				}
			case "offer", "answer":
				if pc.RemoteDescription() != nil {
					continue
				}
				var desc webrtc.SessionDescription
				if json.Unmarshal(msg.Payload, &desc) != nil || desc.Type.String() != msg.Type || pc.SetRemoteDescription(desc) != nil {
					return
				}
				if msg.Type == "offer" {
					answer, err := pc.CreateAnswer(nil)
					if err != nil || localDescription(answer) != nil {
						return
					}
				}
			}
		}
	}
}
