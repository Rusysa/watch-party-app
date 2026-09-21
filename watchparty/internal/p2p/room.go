// Package p2p provides the WebRTC peer-to-peer layer for the watch party,
// using Pion WebRTC v4 and a Weron-compatible signaling transport.
package p2p

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"watchparty/internal/transport"
)

// ─────────────────────────────────────────────────────────────────────────────
// Message types sent over the WebRTC data channel
// ─────────────────────────────────────────────────────────────────────────────

// MsgType identifies what kind of message is being sent.
type MsgType string

const (
	MsgHello    MsgType = "hello"    // first message: share URL and role
	MsgPlay     MsgType = "play"     // resume playback
	MsgPause    MsgType = "pause"    // pause playback
	MsgSeek     MsgType = "seek"     // seek to position
	MsgSync     MsgType = "sync"     // periodic heartbeat with position
	MsgTransfer MsgType = "transfer" // transfer host control to a peer
)

// Message is the envelope sent over the WebRTC data channel.
type Message struct {
	Type      MsgType `json:"type"`
	Position  float64 `json:"pos,omitempty"`    // seconds
	Timestamp int64   `json:"ts,omitempty"`     // unix ms (sender's clock)
	URL       string  `json:"url,omitempty"`    // stream URL (hello only)
	Role      string  `json:"role,omitempty"`   // "host" or "peer" (hello only)
	TargetID  string  `json:"target,omitempty"` // peer ID (transfer only)
	Paused    bool    `json:"paused,omitempty"` // true if host is paused (sync only)
}

// ─────────────────────────────────────────────────────────────────────────────
// PeerInfo tracks a connected peer's state
// ─────────────────────────────────────────────────────────────────────────────

// PeerInfo holds runtime info about a connected peer.
type PeerInfo struct {
	ID       string
	Role     string
	Position float64
	LastSeen time.Time
}

// ─────────────────────────────────────────────────────────────────────────────
// Room
// ─────────────────────────────────────────────────────────────────────────────

// IncomingMsg is a message received from a peer, annotated with the sender ID.
type IncomingMsg struct {
	SenderID string
	Msg      Message
}

// Room manages a weron community (watch party room).
type Room struct {
	mu sync.RWMutex

	selfID      string
	hostID      string // pinned on first host hello; changed only by the current host
	role        string // "host" or "peer"
	streamURL   string
	signalerURL string
	roomID      string
	password    string

	adapter *transport.Adapter
	cancel  context.CancelFunc

	// peerConns maps peerID → writer func so we can send targeted messages
	peerConns map[string]func([]byte) error
	peers     map[string]*PeerInfo

	// Incoming messages from any peer
	Incoming chan IncomingMsg
	// PeerJoined / PeerLeft events for the UI
	PeerJoined chan PeerInfo
	PeerLeft   chan string
}

// NewRoom creates a room but does not connect yet.
func NewRoom(signalerURL, roomID, password, role, streamURL string) *Room {
	return &Room{
		signalerURL: signalerURL,
		roomID:      roomID,
		password:    password,
		role:        role,
		streamURL:   streamURL,
		peerConns:   make(map[string]func([]byte) error),
		peers:       make(map[string]*PeerInfo),
		Incoming:    make(chan IncomingMsg, 128),
		PeerJoined:  make(chan PeerInfo, 32),
		PeerLeft:    make(chan string, 32),
	}
}

// Open connects to the signaler and starts accepting peers.
// The returned context cancel stops all activity.
func (r *Room) Open(ctx context.Context) error {
	rctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel

	// The community name allows peers to discover each other.
	// The signaler requires a password for ephemeral communities.
	sigURL, err := SignalerAddress(r.signalerURL, r.roomID, r.password)
	if err != nil {
		cancel()
		return err
	}

	r.adapter, err = transport.New(
		rctx,
		sigURL,
		r.password, // Signaling encryption key (also known to the signaler)
		[]string{"stun:stun.l.google.com:19302", "stun:stun1.l.google.com:19302"},
	)
	if err != nil {
		cancel()
		return err
	}
	if err := r.adapter.Open(); err != nil {
		cancel()
		return err
	}
	r.mu.Lock()
	r.selfID = r.adapter.ID()
	if r.role == "host" {
		r.hostID = r.selfID
	}
	r.mu.Unlock()
	go r.eventLoop(rctx)
	return nil
}

// Close disconnects from the room.
func (r *Room) Close() {
	if r.cancel != nil {
		r.cancel()
	}
}

// SetStreamURL updates the stream URL for the room.
func (r *Room) SetStreamURL(url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.streamURL = url
}

// SelfID returns this client's peer ID (available after Open).
func (r *Room) SelfID() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.selfID
}

func (r *Room) HostID() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.hostID
}

func (r *Room) IsHost() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.role == "host"
}

func (r *Room) setHostLocked(id string) {
	r.hostID = id
	r.role = "peer"
	if id == r.selfID {
		r.role = "host"
	}
	for peerID, peer := range r.peers {
		peer.Role = "peer"
		if peerID == id {
			peer.Role = "host"
		}
	}
}

// TransferControl validates the target before relinquishing local authority.
func (r *Room) TransferControl(id string) error {
	r.mu.Lock()
	if r.role != "host" || id == r.selfID || r.peers[id] == nil {
		r.mu.Unlock()
		return fmt.Errorf("only the host can transfer to a connected peer")
	}
	r.setHostLocked(id)
	r.mu.Unlock()
	r.Broadcast(Message{Type: MsgTransfer, TargetID: id})
	return nil
}

// acceptMessage is the authorization boundary before application dispatch.
// Initial host discovery is trust-on-first-use within the password-protected room.
func (r *Room) acceptMessage(id string, msg Message) bool {
	if !ValidPosition(msg.Position) {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.peers[id] == nil {
		return false
	}
	switch msg.Type {
	case MsgHello:
		if msg.Role != "host" && msg.Role != "peer" {
			return false
		}
		if msg.Role == "peer" {
			return msg.URL == "" && id != r.hostID
		}
		if r.role == "host" || (r.hostID != "" && r.hostID != id) {
			return false
		}
		if msg.URL != "" && ValidateStreamURL(msg.URL) != nil {
			return false
		}
		r.setHostLocked(id)
	case MsgTransfer:
		if r.role == "host" || id != r.hostID || msg.TargetID == id ||
			(msg.TargetID != r.selfID && r.peers[msg.TargetID] == nil) {
			return false
		}
		r.setHostLocked(msg.TargetID)
	case MsgPlay, MsgPause, MsgSeek:
		return r.role != "host" && id == r.hostID
	case MsgSync:
		return true
	default:
		return false
	}
	return true
}

// Peers returns a snapshot of currently connected peers.
func (r *Room) Peers() []PeerInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]PeerInfo, 0, len(r.peers))
	for _, p := range r.peers {
		out = append(out, *p)
	}
	return out
}

// Broadcast sends a message to ALL connected peers.
func (r *Room) Broadcast(msg Message) {
	msg.Timestamp = time.Now().UnixMilli()
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	data = append(data, '\n')

	r.mu.RLock()
	senders := make(map[string]func([]byte) error, len(r.peerConns))
	for id, send := range r.peerConns {
		senders[id] = send
	}
	r.mu.RUnlock()
	for id, send := range senders {
		if err := send(data); err != nil {
			log.Printf("p2p: error sending to peer %s: %v", id, err)
		}
	}
}

// SendTo sends a message to a specific peer.
func (r *Room) SendTo(peerID string, msg Message) error {
	msg.Timestamp = time.Now().UnixMilli()
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	r.mu.RLock()
	send, ok := r.peerConns[peerID]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("peer %s not connected", peerID)
	}
	return send(data)
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal
// ─────────────────────────────────────────────────────────────────────────────

func (r *Room) eventLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return

		case peer, ok := <-r.adapter.Accept():
			if !ok || peer == nil {
				return
			}
			// A new peer has connected to us via WebRTC
			log.Printf("p2p: peer connected: %s (channel: %s)", peer.PeerID, peer.ChannelID)

			info := &PeerInfo{
				ID:       peer.PeerID,
				Role:     "peer",
				LastSeen: time.Now(),
			}
			r.mu.Lock()
			if len(r.peers) >= 32 || r.peers[peer.PeerID] != nil {
				r.mu.Unlock()
				peer.Conn.Close()
				continue
			}
			r.peers[peer.PeerID] = info
			// Register a send function for this peer
			var writeMu sync.Mutex
			r.peerConns[peer.PeerID] = func(data []byte) error {
				writeMu.Lock()
				defer writeMu.Unlock()
				timer := time.AfterFunc(5*time.Second, func() { peer.Conn.Close() })
				defer timer.Stop()
				_, err := peer.Conn.Write(data)
				return err
			}
			r.mu.Unlock()

			select {
			case r.PeerJoined <- *info:
			default:
			}

			// Send hello to the new peer immediately
			go r.sendHello(peer.PeerID)

			// Start reading from this peer in a goroutine
			go r.readPeer(ctx, peer.PeerID, peer)
		}
	}
}

// sendHello sends our hello message to a specific peer.
func (r *Room) sendHello(peerID string) {
	time.Sleep(200 * time.Millisecond) // small delay to let data channel stabilize
	r.mu.RLock()
	url := r.streamURL
	role := r.role
	if role != "host" {
		url = ""
	}
	r.mu.RUnlock()
	r.SendTo(peerID, Message{
		Type: MsgHello,
		URL:  url,
		Role: role,
	})
}

// readPeer reads messages from a connected peer until disconnected.
func (r *Room) readPeer(ctx context.Context, peerID string, peer *transport.Peer) {
	defer peer.Conn.Close()
	stop := context.AfterFunc(ctx, func() { peer.Conn.Close() })
	defer stop()
	defer func() {
		log.Printf("p2p: peer disconnected: %s", peerID)
		r.mu.Lock()
		delete(r.peers, peerID)
		delete(r.peerConns, peerID)
		r.mu.Unlock()
		select {
		case r.PeerLeft <- peerID:
		default:
		}
	}()

	scanner := bufio.NewScanner(peer.Conn)
	scanner.Buffer(make([]byte, 4096), 32<<10)
	window := time.Now()
	messages := 0
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if !scanner.Scan() {
			return
		}
		if time.Since(window) >= time.Second {
			window, messages = time.Now(), 0
		}
		messages++
		if messages > 64 {
			return
		}

		var msg Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			log.Printf("p2p: invalid message from %s: %v", peerID, err)
			continue
		}

		if !r.acceptMessage(peerID, msg) {
			continue
		}

		// Update peer position/info on sync messages
		if msg.Type == MsgSync || msg.Type == MsgHello {
			r.mu.Lock()
			if p, ok := r.peers[peerID]; ok {
				p.LastSeen = time.Now()
				if msg.Position > 0 {
					p.Position = msg.Position
				}
			}
			r.mu.Unlock()
		}

		select {
		case r.Incoming <- IncomingMsg{SenderID: peerID, Msg: msg}:
		default:
			log.Printf("p2p: incoming channel full, dropping message from %s", peerID)
		}
	}
}
