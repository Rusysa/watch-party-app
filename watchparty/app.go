package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"os"
	"strings"
	gosync "sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"watchparty/internal/mpv"
	"watchparty/internal/p2p"
	syncc "watchparty/internal/sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// App state
// ─────────────────────────────────────────────────────────────────────────────

// RoomState is emitted to the frontend to describe current room status.
type RoomState struct {
	RoomID    string     `json:"roomId"`
	SelfID    string     `json:"selfId"`
	IsHost    bool       `json:"isHost"`
	StreamURL string     `json:"streamUrl"`
	Peers     []PeerView `json:"peers"`
}

// PeerView is the frontend-friendly representation of a peer.
type PeerView struct {
	ID       string  `json:"id"`
	Role     string  `json:"role"`
	Position float64 `json:"position"`
}

// PlaybackState is emitted when playback changes.
type PlaybackState struct {
	Paused   bool    `json:"paused"`
	Position float64 `json:"position"`
}

// ─────────────────────────────────────────────────────────────────────────────
// App
// ─────────────────────────────────────────────────────────────────────────────

// App is the main application struct. All exported methods are bound to JS.
type App struct {
	ctx context.Context

	mu         gosync.Mutex
	room       *p2p.Room
	mpvClient  *mpv.Client
	mpvManager *mpv.Manager
	mpvPath    string
	mpvCancel  context.CancelFunc
	controller *syncc.Controller
	roomCancel context.CancelFunc
	syncCancel context.CancelFunc

	signalerURL string
	isHost      bool
	streamURL   string
	currentRoom string
}

// NewApp creates a new App application struct.
func NewApp() *App {
	mgr, err := mpv.NewManager()
	if err != nil {
		log.Printf("warning: mpv manager init failed: %v", err)
	}
	signalerURL := strings.TrimSpace(os.Getenv("WATCHPARTY_SIGNALER_URL"))
	if signalerURL == "" {
		signalerURL = "wss://watchparty-signaler.onrender.com/"
	}
	return &App{
		mpvManager:  mgr,
		signalerURL: signalerURL,
	}
}

// startup is called when the app starts. The context is saved so we can call runtime methods.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// Start the prerequisite check as soon as Wails is ready. The frontend also
	// awaits this operation so it can show progress; Manager serializes both
	// calls and performs at most one installation.
	go func() {
		if err := a.CheckAndInstallMPV(); err != nil {
			log.Printf("mpv prerequisite check: %v", err)
		}
	}()
}

// CheckAndInstallMPV checks if mpv is installed, and if not, downloads it.
func (a *App) CheckAndInstallMPV() error {
	a.mu.Lock()
	if a.mpvManager == nil {
		a.mu.Unlock()
		return fmt.Errorf("mpv manager not initialized")
	}
	a.mu.Unlock()

	path, err := a.mpvManager.Install(a.ctx, func(status string) {
		runtime.EventsEmit(a.ctx, "installer:progress", status)
	})
	if err != nil {
		return err
	}

	a.mu.Lock()
	a.mpvPath = path
	a.mu.Unlock()
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Room management (bound to JS)
// ─────────────────────────────────────────────────────────────────────────────

// CreateRoom creates a new watch party room as host.
// Returns the room code to share with peers.
func (a *App) CreateRoom(password string) (string, error) {
	if len(password) < 4 || len(password) > 256 || strings.TrimSpace(password) == "" {
		return "", fmt.Errorf("password must contain 4 to 256 bytes")
	}

	roomID, err := generateRoomCode()
	if err != nil {
		return "", err
	}

	if err := a.startRoom(roomID, password, "host", ""); err != nil {
		return "", err
	}

	return roomID, nil
}

// JoinRoom joins an existing watch party room as a peer.
// The stream URL will be received from the host via the data channel.
func (a *App) JoinRoom(roomID, password string) error {
	roomID = strings.ToUpper(strings.TrimSpace(roomID))
	if len(roomID) != 6 || strings.Trim(roomID, roomCodeChars) != "" {
		return fmt.Errorf("invalid room code")
	}
	if len(password) < 4 || len(password) > 256 || strings.TrimSpace(password) == "" {
		return fmt.Errorf("password must contain 4 to 256 bytes")
	}

	// Join without URL (will receive from host)
	return a.startRoom(roomID, password, "peer", "")
}

// SetStreamURL is called by the host from the UI to set/change the video link.
func (a *App) SetStreamURL(rawURL string) error {
	// Validate URL scheme before accepting
	if err := p2p.ValidateStreamURL(rawURL); err != nil {
		return err
	}

	a.mu.Lock()
	if a.room == nil || !a.room.IsHost() {
		a.mu.Unlock()
		return fmt.Errorf("only the host can set the stream url")
	}
	a.streamURL = rawURL
	room := a.room
	a.mu.Unlock()

	if room == nil {
		return fmt.Errorf("not in a room")
	}

	room.SetStreamURL(rawURL)

	// Launch or restart MPV
	if err := a.launchMPV(rawURL); err != nil {
		return fmt.Errorf("failed to launch mpv: %w", err)
	}

	// Broadcast the new URL to everyone
	room.Broadcast(p2p.Message{
		Type: p2p.MsgHello,
		Role: "host",
		URL:  rawURL,
	})

	return nil
}

// LeaveRoom disconnects from the current room and stops mpv.
func (a *App) LeaveRoom() {
	a.leaveRoomInternal()
	runtime.EventsEmit(a.ctx, "room:left", nil)
}

// TransferControl transfers host control to a specific peer.
func (a *App) TransferControl(peerID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.room == nil || !a.room.IsHost() {
		return fmt.Errorf("only the host can transfer control")
	}
	if a.room == nil {
		return fmt.Errorf("not in a room")
	}

	if err := a.room.TransferControl(peerID); err != nil {
		return err
	}

	a.isHost = false

	runtime.EventsEmit(a.ctx, "control:transferred", peerID)
	return nil
}

// SetSignalerURL updates the signaler URL used for new rooms.
func (a *App) SetSignalerURL(url string) error {
	if _, err := p2p.SignalerAddress(url, "", ""); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.signalerURL = url
	return nil
}

// GetSignalerURL returns the currently configured signaler URL.
func (a *App) GetSignalerURL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.signalerURL
}

// ─────────────────────────────────────────────────────────────────────────────
// Playback control (bound to JS, host only)
// ─────────────────────────────────────────────────────────────────────────────

// Play resumes playback and notifies all peers.
func (a *App) Play() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.room == nil || !a.room.IsHost() {
		return fmt.Errorf("only the host can control playback")
	}

	if a.mpvClient == nil {
		return fmt.Errorf("mpv not running")
	}
	if err := a.mpvClient.Play(); err != nil {
		return err
	}
	pos, _ := a.mpvClient.GetPosition()
	if a.controller != nil {
		a.controller.NotifyPlay(pos)
	}
	return nil
}

// Pause pauses playback and notifies all peers.
func (a *App) Pause() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.room == nil || !a.room.IsHost() {
		return fmt.Errorf("only the host can control playback")
	}

	if a.mpvClient == nil {
		return fmt.Errorf("mpv not running")
	}
	if err := a.mpvClient.Pause(); err != nil {
		return err
	}
	pos, _ := a.mpvClient.GetPosition()
	if a.controller != nil {
		a.controller.NotifyPause(pos)
	}
	return nil
}

// Seek seeks to a position (seconds) and notifies all peers.
func (a *App) Seek(position float64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.room == nil || !a.room.IsHost() {
		return fmt.Errorf("only the host can control playback")
	}
	if !p2p.ValidPosition(position) {
		return fmt.Errorf("invalid playback position")
	}

	if a.mpvClient == nil {
		return fmt.Errorf("mpv not running")
	}
	if err := a.mpvClient.Seek(position); err != nil {
		return err
	}
	if a.controller != nil {
		a.controller.NotifySeek(position)
	}
	return nil
}

// GetPlaybackState returns the current mpv playback state.
func (a *App) GetPlaybackState() PlaybackState {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.mpvClient == nil {
		return PlaybackState{}
	}
	pos, _ := a.mpvClient.GetPosition()
	paused := a.mpvClient.IsPaused()
	return PlaybackState{Paused: paused, Position: pos}
}

// GetRoomState returns the current room state for the UI.
func (a *App) GetRoomState() RoomState {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.room == nil {
		return RoomState{}
	}

	rawPeers := a.room.Peers()
	peers := make([]PeerView, len(rawPeers))
	for i, p := range rawPeers {
		peers[i] = PeerView{ID: p.ID, Role: p.Role, Position: p.Position}
	}

	return RoomState{
		RoomID:    a.currentRoom,
		SelfID:    a.room.SelfID(),
		IsHost:    a.room.IsHost(),
		StreamURL: a.streamURL,
		Peers:     peers,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal
// ─────────────────────────────────────────────────────────────────────────────

func (a *App) startRoom(roomID, password, role, streamURL string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Leave any existing room first
	a.leaveRoomNoLock()

	rctx, rcancel := context.WithCancel(a.ctx)
	a.roomCancel = rcancel

	room := p2p.NewRoom(a.signalerURL, roomID, password, role, streamURL)
	if err := room.Open(rctx); err != nil {
		rcancel()
		return fmt.Errorf("joining room: %w", err)
	}

	a.room = room
	a.isHost = role == "host"
	a.streamURL = streamURL
	a.currentRoom = roomID

	// Start event handlers
	go a.handleRoomEvents(rctx, room)

	return nil
}

func (a *App) launchMPV(streamURL string) error {
	a.mu.Lock()
	path := a.mpvPath

	// If MPV is already running, close it first
	if a.mpvClient != nil {
		a.mpvClient.Close()
		a.mpvClient = nil
	}
	if a.syncCancel != nil {
		a.syncCancel()
		a.syncCancel = nil
	}
	if a.mpvCancel != nil {
		a.mpvCancel()
		a.mpvCancel = nil
	}

	if path == "" {
		a.mu.Unlock()
		return fmt.Errorf("mpv no está instalado")
	}

	room := a.room
	a.mu.Unlock()

	mpvCtx, mpvCancel := context.WithCancel(a.ctx)
	client, err := mpv.Launch(mpvCtx, path, streamURL)
	if err != nil {
		mpvCancel()
		return err
	}

	a.mu.Lock()
	a.mpvClient = client
	a.mpvCancel = mpvCancel

	// Start the sync controller
	if room != nil {
		sctx, scancel := context.WithCancel(a.ctx)
		a.syncCancel = scancel
		ctrl := syncc.New(client, room)
		a.controller = ctrl
		a.mu.Unlock()
		go ctrl.Run(sctx)
	} else {
		a.mu.Unlock()
	}

	// Listen for mpv events and forward to frontend
	go a.handleMPVEvents(client)

	return nil
}

func (a *App) handleRoomEvents(ctx context.Context, room *p2p.Room) {
	for {
		select {
		case <-ctx.Done():
			return

		case info := <-room.PeerJoined:
			log.Printf("app: peer joined: %s", info.ID)
			runtime.EventsEmit(a.ctx, "peer:joined", PeerView{
				ID:   info.ID,
				Role: info.Role,
			})
			a.emitRoomState()

		case peerID := <-room.PeerLeft:
			log.Printf("app: peer left: %s", peerID)
			runtime.EventsEmit(a.ctx, "peer:left", peerID)
			a.emitRoomState()

		case inc := <-room.Incoming:
			if ctx.Err() != nil {
				return
			}
			a.handleIncoming(ctx, inc)
			// Forward to sync controller if active (fan-out pattern)
			a.mu.Lock()
			ctrl := a.controller
			a.mu.Unlock()
			if ctrl != nil {
				select {
				case ctrl.IncomingCh <- inc:
				default:
					log.Println("app: controller incoming channel full, dropping message")
				}
			}
		}
	}
}

func (a *App) handleIncoming(ctx context.Context, inc p2p.IncomingMsg) {
	msg := inc.Msg

	switch msg.Type {
	case p2p.MsgTransfer:
		a.mu.Lock()
		if a.room != nil {
			a.isHost = a.room.IsHost()
		}
		a.mu.Unlock()
		a.emitRoomState()
	case p2p.MsgHello:
		a.mu.Lock()
		isHost := a.isHost
		trustedHost := a.room != nil && inc.SenderID == a.room.HostID()
		a.mu.Unlock()

		// A peer sent us their hello. If we're a peer receiving from host,
		// the URL is in the message — launch mpv with it.
		if !isHost && trustedHost && msg.Role == "host" && msg.URL != "" {
			// Validate URL scheme from network
			if p2p.ValidateStreamURL(msg.URL) != nil {
				log.Print("app: rejecting invalid URL from host")
				return
			}

			log.Print("app: received stream URL from host")
			a.mu.Lock()
			a.streamURL = msg.URL
			if a.room != nil {
				a.room.SetStreamURL(msg.URL)
			}
			a.mu.Unlock()

			// Launch mpv with the received URL
			if err := a.launchMPV(msg.URL); err != nil {
				log.Printf("app: error launching mpv: %v", err)
				runtime.EventsEmit(a.ctx, "error", err.Error())
				return
			}
			runtime.EventsEmit(a.ctx, "stream:received", msg.URL)
			a.emitRoomState()
		}

	case p2p.MsgSync:
		// Forward peer positions to the UI for the dashboard
		runtime.EventsEmit(a.ctx, "peer:position", map[string]interface{}{
			"peerId":   inc.SenderID,
			"position": msg.Position,
		})
	}
}

func (a *App) handleMPVEvents(client *mpv.Client) {
	for evt := range client.Events {
		switch evt.Event {
		case "property-change":
			switch evt.Name {
			case "pause":
				paused, _ := client.GetPaused()
				pos, _ := client.GetPosition()
				runtime.EventsEmit(a.ctx, "playback:state", PlaybackState{
					Paused:   paused,
					Position: pos,
				})

				a.mu.Lock()
				ctrl := a.controller
				isHost := a.isHost
				a.mu.Unlock()
				if ctrl != nil && isHost {
					if paused {
						ctrl.NotifyPause(pos)
					} else {
						ctrl.NotifyPlay(pos)
					}
				}
			case "time-pos":
				pos, _ := client.GetPosition()
				runtime.EventsEmit(a.ctx, "playback:position", pos)
			}
		case "end-file":
			if evt.Reason == "error" || evt.Reason == "unsupported" {
				runtime.EventsEmit(a.ctx, "playback:error", "mpv no pudo cargar el enlace de vídeo")
			} else {
				runtime.EventsEmit(a.ctx, "playback:ended", nil)
			}
		case "file-loaded":
			runtime.EventsEmit(a.ctx, "playback:loaded", nil)
		}
	}
}

func (a *App) emitRoomState() {
	state := a.GetRoomState()
	runtime.EventsEmit(a.ctx, "room:state", state)
}

func (a *App) leaveRoomInternal() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.leaveRoomNoLock()
}

func (a *App) leaveRoomNoLock() {
	if a.syncCancel != nil {
		a.syncCancel()
		a.syncCancel = nil
	}
	if a.mpvCancel != nil {
		a.mpvCancel()
		a.mpvCancel = nil
	}
	if a.mpvClient != nil {
		a.mpvClient.Close()
		a.mpvClient = nil
	}
	if a.room != nil {
		a.room.Close()
		a.room = nil
	}
	if a.roomCancel != nil {
		a.roomCancel()
		a.roomCancel = nil
	}
	a.isHost = false
	a.streamURL = ""
	a.currentRoom = ""
	a.controller = nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Utilities
// ─────────────────────────────────────────────────────────────────────────────

const roomCodeChars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func generateRoomCode() (string, error) {
	b := make([]byte, 6)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(roomCodeChars))))
		if err != nil {
			return "", fmt.Errorf("generating secure room code: %w", err)
		}
		b[i] = roomCodeChars[n.Int64()]
	}
	return string(b), nil
}
