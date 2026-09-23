// Package p2p provides the WebRTC peer-to-peer layer for the watch party,
// using Pion WebRTC v4 and a Weron-compatible signaling transport.
package p2p

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/base64"
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
	MsgHello    MsgType = "hello"    // share current URL, playback state and role
	MsgPlay     MsgType = "play"     // resume playback
	MsgPause    MsgType = "pause"    // pause playback
	MsgSeek     MsgType = "seek"     // seek to position
	MsgSync     MsgType = "sync"     // periodic heartbeat with position
	MsgTransfer MsgType = "transfer" // transfer host control to a peer
	MsgClaim    MsgType = "claim"    // signed return of the room creator
	MsgLeave    MsgType = "leave"    // voluntary departure
	MsgSnapshot MsgType = "snapshot" // current stream sent by outgoing host to creator
)

// Message is the envelope sent over the WebRTC data channel.
type Message struct {
	Type       MsgType `json:"type"`
	Position   float64 `json:"pos,omitempty"`    // seconds
	Timestamp  int64   `json:"ts,omitempty"`     // unix ms (sender's clock)
	URL        string  `json:"url,omitempty"`    // stream URL (hello or snapshot)
	Role       string  `json:"role,omitempty"`   // "host" or "peer" (hello only)
	TargetID   string  `json:"target,omitempty"` // peer ID (transfer only)
	Paused     bool    `json:"paused,omitempty"` // host pause state in sync/hello/snapshot
	CreatorKey string  `json:"creatorKey,omitempty"`
	Signature  string  `json:"signature,omitempty"`
	Term       uint64  `json:"term,omitempty"` // leadership revision
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

	selfID       string
	hostID       string // current controller, updated by transfer, election or signed creator claim
	role         string // "host" or "peer"
	streamURL    string
	streamPos    float64
	streamPaused bool
	signalerURL  string
	roomID       string
	password     string
	creatorKey   ed25519.PublicKey
	creatorPriv  ed25519.PrivateKey
	previousHost string
	claimOnJoin  bool
	term         uint64
	creatorID    string

	adapter *transport.Adapter
	cancel  context.CancelFunc

	// peerConns maps peerID → writer func so we can send targeted messages
	peerConns map[string]func([]byte) error
	peers     map[string]*PeerInfo

	// Incoming messages from any peer
	Incoming chan IncomingMsg
	// PeerJoined / PeerLeft events for the UI
	PeerJoined  chan PeerInfo
	PeerLeft    chan string
	RoleChanged chan struct{}
	Connection  chan bool
}

// SetCreatorIdentity pins the creator key from a previous visit and optionally
// supplies the private key held only by the creator. Call before Open.
func (r *Room) SetCreatorIdentity(public ed25519.PublicKey, private ed25519.PrivateKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.creatorKey = append(ed25519.PublicKey(nil), public...)
	r.creatorPriv = append(ed25519.PrivateKey(nil), private...)
	r.claimOnJoin = r.role == "peer" && len(private) == ed25519.PrivateKeySize
}

func (r *Room) CreatorKey() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return base64.RawStdEncoding.EncodeToString(r.creatorKey)
}

func (r *Room) IsCreator() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.creatorPriv) == ed25519.PrivateKeySize
}

// NewRoom creates a room but does not connect yet.
func NewRoom(signalerURL, roomID, password, role, streamURL string) *Room {
	return &Room{
		signalerURL:  signalerURL,
		roomID:       roomID,
		password:     password,
		role:         role,
		streamURL:    streamURL,
		streamPaused: true,
		peerConns:    make(map[string]func([]byte) error),
		peers:        make(map[string]*PeerInfo),
		Incoming:     make(chan IncomingMsg, 128),
		PeerJoined:   make(chan PeerInfo, 32),
		PeerLeft:     make(chan string, 32),
		RoleChanged:  make(chan struct{}, 8),
		Connection:   make(chan bool, 8),
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
		r.term = 1
		if len(r.creatorPriv) == ed25519.PrivateKeySize {
			r.creatorID = r.selfID
		}
	}
	returningCreator := len(r.creatorPriv) == ed25519.PrivateKeySize && r.role == "peer"
	r.mu.Unlock()
	go r.eventLoop(rctx)
	if returningCreator {
		go func() {
			select {
			case <-rctx.Done():
				return
			case <-time.After(4 * time.Second):
			}
			r.mu.Lock()
			if r.hostID == "" && len(r.peers) == 0 {
				r.setHostLocked(r.selfID)
				r.mu.Unlock()
				select {
				case r.RoleChanged <- struct{}{}:
				default:
				}
			} else {
				r.mu.Unlock()
			}
		}()
	}
	return nil
}

// Close disconnects from the room.
func (r *Room) Close() {
	r.Broadcast(Message{Type: MsgLeave})
	if r.cancel != nil {
		r.cancel()
	}
}

// SetStreamURL updates the stream URL for the room.
func (r *Room) SetStreamURL(url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.streamURL != url {
		r.streamURL = url
		r.streamPos = 0
		r.streamPaused = true
	}
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
	r.term++
	r.claimOnJoin = false
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
			return msg.URL == "" && msg.CreatorKey == "" && id != r.hostID
		}
		if r.hostID != id {
			if (r.creatorID != "" && r.hostID == r.creatorID) ||
				(r.role == "host" && len(r.creatorPriv) != 0) ||
				(r.hostID != "" && (msg.Term < r.term || (msg.Term == r.term && id >= r.hostID))) {
				return false
			}
		}
		if msg.URL != "" && ValidateStreamURL(msg.URL) != nil {
			return false
		}
		key, err := base64.RawStdEncoding.DecodeString(msg.CreatorKey)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return false
		}
		if len(r.creatorKey) != 0 && !ed25519.PublicKey(key).Equal(r.creatorKey) {
			return false
		}
		r.creatorKey = key
		if msg.Term > r.term {
			r.term = msg.Term
		}
		r.setHostLocked(id)
		if msg.URL != "" {
			r.streamURL = msg.URL
			r.streamPos, r.streamPaused = msg.Position, msg.Paused
		}
	case MsgClaim:
		if len(r.creatorKey) != ed25519.PublicKeySize || msg.URL != "" ||
			time.Since(time.UnixMilli(msg.Timestamp)) > 30*time.Second ||
			time.Until(time.UnixMilli(msg.Timestamp)) > 5*time.Second {
			return false
		}
		signature, err := base64.RawStdEncoding.DecodeString(msg.Signature)
		if err != nil || !ed25519.Verify(r.creatorKey, []byte(fmt.Sprintf("%s:%s:%d", r.roomID, id, msg.Timestamp)), signature) {
			return false
		}
		if r.hostID == id {
			r.creatorID = id
			if msg.Term > r.term {
				r.term = msg.Term
			}
			return false // proof confirms the current host; no handover to report
		}
		r.previousHost = r.hostID
		if msg.Term > r.term {
			r.term = msg.Term
		}
		r.creatorID = id
		r.setHostLocked(id)
	case MsgSnapshot:
		if r.role != "host" || len(r.creatorPriv) == 0 || id != r.previousHost ||
			ValidateStreamURL(msg.URL) != nil {
			return false
		}
		r.previousHost = ""
		return true
	case MsgLeave:
		return true
	case MsgTransfer:
		if r.role == "host" || id != r.hostID || msg.Term <= r.term || msg.TargetID == id ||
			(msg.TargetID != r.selfID && r.peers[msg.TargetID] == nil) {
			return false
		}
		r.setHostLocked(msg.TargetID)
		r.term = msg.Term
	case MsgPlay, MsgPause, MsgSeek:
		if r.role == "host" || id != r.hostID {
			return false
		}
		r.streamPos = msg.Position
		if msg.Type == MsgPlay {
			r.streamPaused = false
		}
		if msg.Type == MsgPause {
			r.streamPaused = true
		}
	case MsgSync:
		if id == r.hostID {
			r.streamPos, r.streamPaused = msg.Position, msg.Paused
		}
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
	if msg.Timestamp == 0 {
		msg.Timestamp = time.Now().UnixMilli()
	}
	r.mu.RLock()
	if msg.Term == 0 {
		msg.Term = r.term
	}
	r.mu.RUnlock()
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	data = append(data, '\n')

	r.mu.Lock()
	if r.role == "host" {
		switch msg.Type {
		case MsgHello:
			r.streamPos, r.streamPaused = msg.Position, msg.Paused
		case MsgSync:
			r.streamPos, r.streamPaused = msg.Position, msg.Paused
		case MsgPause:
			r.streamPos, r.streamPaused = msg.Position, true
		case MsgPlay:
			r.streamPos, r.streamPaused = msg.Position, false
		case MsgSeek:
			r.streamPos = msg.Position
		}
	}
	senders := make(map[string]func([]byte) error, len(r.peerConns))
	for id, send := range r.peerConns {
		senders[id] = send
	}
	r.mu.Unlock()
	for id, send := range senders {
		if err := send(data); err != nil {
			log.Printf("p2p: error sending to peer %s: %v", id, err)
		}
	}
}

// SendTo sends a message to a specific peer.
func (r *Room) SendTo(peerID string, msg Message) error {
	msg.Timestamp = time.Now().UnixMilli()
	r.mu.RLock()
	if msg.Term == 0 {
		msg.Term = r.term
	}
	r.mu.RUnlock()
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
		case connected := <-r.adapter.Status():
			select {
			case r.Connection <- connected:
			default:
			}

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
	pos, paused := r.streamPos, r.streamPaused
	key := base64.RawStdEncoding.EncodeToString(r.creatorKey)
	if role != "host" {
		url = ""
	}
	r.mu.RUnlock()
	if err := r.SendTo(peerID, Message{
		Type:     MsgHello,
		URL:      url,
		Role:     role,
		Position: pos,
		Paused:   paused,
		CreatorKey: func() string {
			if role == "host" {
				return key
			}
			return ""
		}(),
	}); err != nil {
		return
	}
	r.mu.RLock()
	claim := r.claimOnJoin
	creatorHost := r.role == "host" && len(r.creatorPriv) == ed25519.PrivateKeySize
	r.mu.RUnlock()
	if claim {
		time.Sleep(time.Second)
		r.ClaimCreator()
	} else if creatorHost {
		r.sendCreatorProof(peerID)
	}
}

func (r *Room) sendCreatorProof(peerID string) {
	r.mu.RLock()
	if r.role != "host" || len(r.creatorPriv) != ed25519.PrivateKeySize {
		r.mu.RUnlock()
		return
	}
	private := append(ed25519.PrivateKey(nil), r.creatorPriv...)
	id, roomID := r.selfID, r.roomID
	url, pos, paused := r.streamURL, r.streamPos, r.streamPaused
	key := base64.RawStdEncoding.EncodeToString(r.creatorKey)
	r.mu.RUnlock()
	now := time.Now().UnixMilli()
	if err := r.SendTo(peerID, Message{Type: MsgClaim, Timestamp: now,
		Signature: base64.RawStdEncoding.EncodeToString(ed25519.Sign(private,
			[]byte(fmt.Sprintf("%s:%s:%d", roomID, id, now))))}); err == nil {
		_ = r.SendTo(peerID, Message{Type: MsgHello, Role: "host", URL: url,
			CreatorKey: key, Position: pos, Paused: paused})
	}
}

// ClaimCreator proves ownership without revealing the private key to peers.
func (r *Room) ClaimCreator() {
	r.mu.RLock()
	if len(r.creatorPriv) != ed25519.PrivateKeySize || !r.claimOnJoin {
		r.mu.RUnlock()
		return
	}
	private := append(ed25519.PrivateKey(nil), r.creatorPriv...)
	id, roomID := r.selfID, r.roomID
	r.mu.RUnlock()
	now := time.Now().UnixMilli()
	r.mu.Lock()
	if !r.claimOnJoin {
		r.mu.Unlock()
		return
	}
	r.term++
	r.setHostLocked(id)
	r.creatorID = id
	r.claimOnJoin = false
	r.mu.Unlock()
	select {
	case r.RoleChanged <- struct{}{}:
	default:
	}
	r.Broadcast(Message{Type: MsgClaim, Signature: base64.RawStdEncoding.EncodeToString(
		ed25519.Sign(private, []byte(fmt.Sprintf("%s:%s:%d", roomID, id, now)))), Timestamp: now})
	r.mu.RLock()
	url := r.streamURL
	key := base64.RawStdEncoding.EncodeToString(r.creatorKey)
	pos, paused := r.streamPos, r.streamPaused
	r.mu.RUnlock()
	r.Broadcast(Message{Type: MsgHello, Role: "host", URL: url, CreatorKey: key, Position: pos, Paused: paused})
}

// readPeer reads messages from a connected peer until disconnected.
func (r *Room) readPeer(ctx context.Context, peerID string, peer *transport.Peer) {
	defer peer.Conn.Close()
	stop := context.AfterFunc(ctx, func() { peer.Conn.Close() })
	defer stop()
	defer func() {
		log.Printf("p2p: peer disconnected: %s", peerID)
		r.mu.Lock()
		wasHost := r.hostID == peerID
		delete(r.peers, peerID)
		delete(r.peerConns, peerID)
		if wasHost {
			r.hostID = ""
			r.creatorID = ""
			r.term++
			// Deterministic interim host: peers remaining in the connected mesh
			// choose the smallest transport ID, including the local participant.
			candidate := r.selfID
			for id := range r.peers {
				if id < candidate {
					candidate = id
				}
			}
			r.setHostLocked(candidate)
		}
		r.mu.Unlock()
		if wasHost {
			select {
			case r.RoleChanged <- struct{}{}:
			default:
			}
		}
		select {
		case r.PeerLeft <- peerID:
		default:
		}
		if wasHost && r.IsHost() {
			r.mu.RLock()
			url, pos, paused := r.streamURL, r.streamPos, r.streamPaused
			key := base64.RawStdEncoding.EncodeToString(r.creatorKey)
			r.mu.RUnlock()
			r.Broadcast(Message{Type: MsgHello, Role: "host", URL: url, CreatorKey: key, Position: pos, Paused: paused})
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
