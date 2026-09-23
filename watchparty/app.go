package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"math/big"
	"os"
	"sort"
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
	RoomID       string     `json:"roomId"`
	SelfID       string     `json:"selfId"`
	IsHost       bool       `json:"isHost"`
	StreamURL    string     `json:"streamUrl"`
	PlayerClosed bool       `json:"playerClosed"`
	Peers        []PeerView `json:"peers"`
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

	signalerURL  string
	isHost       bool
	streamURL    string
	currentRoom  string
	lastSession  Session
	lastState    PlaybackState
	playerClosed bool
	launching    bool
	restoring    bool
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
		lastSession: readSession(),
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

// GetLastRoom exposes non-secret reconnect details to the UI.
func (a *App) GetLastRoom() Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.lastSession
	s.PrivateKey = ""
	return s
}

func (a *App) ForgetLastRoom() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	path, err := sessionPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	a.lastSession = Session{}
	return nil
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

// ReopenPlayer rejoins playback without leaving the P2P room.
func (a *App) ReopenPlayer() error {
	a.mu.Lock()
	if a.room == nil || a.streamURL == "" || a.mpvClient != nil || a.launching {
		a.mu.Unlock()
		return fmt.Errorf("no hay una transmisión pendiente de reabrir")
	}
	url, isHost, playback := a.streamURL, a.room.IsHost(), a.lastState
	a.restoring = true
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.restoring = false; a.mu.Unlock() }()
	if err := a.launchMPV(url); err != nil {
		return err
	}
	a.mu.Lock()
	client := a.mpvClient
	a.mu.Unlock()
	if client != nil {
		client.Pause()
		if playback.Position > 0 {
			client.Seek(playback.Position)
		}
		if isHost && !playback.Paused {
			client.Play()
		}
		if isHost {
			a.mu.Lock()
			ctrl := a.controller
			a.mu.Unlock()
			if ctrl != nil {
				if playback.Paused {
					ctrl.NotifyPause(playback.Position)
				} else {
					ctrl.NotifyPlay(playback.Position)
				}
			}
		}
	}
	return nil
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
	a.lastState = PlaybackState{Paused: true}
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
		Type:       p2p.MsgHello,
		Role:       "host",
		URL:        rawURL,
		CreatorKey: room.CreatorKey(),
		Paused:     a.GetPlaybackState().Paused,
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
	a.lastState = PlaybackState{Paused: false, Position: pos}
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
	a.lastState = PlaybackState{Paused: true, Position: pos}
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
	a.lastState.Position = position
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
		return a.lastState
	}
	pos, err := a.mpvClient.GetPosition()
	if err != nil {
		return a.lastState
	}
	paused, err := a.mpvClient.GetPaused()
	if err != nil {
		return a.lastState
	}
	a.lastState = PlaybackState{Paused: paused, Position: pos}
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
		RoomID:       a.currentRoom,
		SelfID:       a.room.SelfID(),
		IsHost:       a.room.IsHost(),
		StreamURL:    a.streamURL,
		PlayerClosed: a.playerClosed,
		Peers:        peers,
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
	var session Session
	var private ed25519.PrivateKey
	if role == "host" {
		var err error
		session, private, err = newSession(roomID, a.signalerURL)
		if err != nil {
			return err
		}
	} else if a.lastSession.RoomID == roomID && a.lastSession.SignalerURL == a.signalerURL {
		session = a.lastSession
		key, _ := base64.RawStdEncoding.DecodeString(session.PrivateKey)
		if len(key) == ed25519.PrivateKeySize && session.Creator {
			private = key
		}
	} else {
		session = Session{RoomID: roomID, SignalerURL: a.signalerURL}
	}

	rctx, rcancel := context.WithCancel(a.ctx)
	a.roomCancel = rcancel

	room := p2p.NewRoom(a.signalerURL, roomID, password, role, streamURL)
	public, _ := base64.RawStdEncoding.DecodeString(session.CreatorKey)
	room.SetCreatorIdentity(public, private)
	if err := room.Open(rctx); err != nil {
		rcancel()
		return fmt.Errorf("joining room: %w", err)
	}

	a.room = room
	a.isHost = role == "host"
	a.streamURL = streamURL
	a.currentRoom = roomID
	a.lastSession = session
	if err := saveSession(session); err != nil {
		log.Printf("app: cannot remember room: %v", err)
	}

	// Start event handlers
	go a.handleRoomEvents(rctx, room)

	return nil
}

func (a *App) launchMPV(streamURL string) error {
	a.mu.Lock()
	if a.launching {
		a.mu.Unlock()
		return fmt.Errorf("el reproductor ya se está abriendo")
	}
	a.launching = true
	defer func() { a.mu.Lock(); a.launching = false; a.mu.Unlock() }()
	path := a.mpvPath

	// If MPV is already running, close it first
	if a.mpvClient != nil {
		a.mpvClient.Close()
		a.mpvClient = nil
	}
	a.controller = nil
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
	if a.room != room {
		a.mu.Unlock()
		client.Close()
		return fmt.Errorf("la sala cambió mientras se abría el reproductor")
	}
	a.mpvClient = client
	a.playerClosed = false
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
			if ctx.Err() != nil {
				return
			}
			log.Printf("app: peer joined: %s", info.ID)
			runtime.EventsEmit(a.ctx, "peer:joined", PeerView{
				ID:   info.ID,
				Role: info.Role,
			})
			a.emitRoomState()

		case peerID := <-room.PeerLeft:
			if ctx.Err() != nil {
				return
			}
			log.Printf("app: peer left: %s", peerID)
			runtime.EventsEmit(a.ctx, "peer:left", peerID)
			if room.IsHost() {
				runtime.EventsEmit(a.ctx, "room:host-change", "Se asignó un host temporal")
			}
			a.emitRoomState()
		case <-room.RoleChanged:
			a.mu.Lock()
			if a.room != room {
				a.mu.Unlock()
				return
			}
			a.isHost = room.IsHost()
			a.mu.Unlock()
			a.emitRoomState()
		case connected := <-room.Connection:
			if ctx.Err() != nil {
				return
			}
			runtime.EventsEmit(a.ctx, "room:connection", connected)

		case inc := <-room.Incoming:
			if ctx.Err() != nil {
				return
			}
			a.mu.Lock()
			current := a.room == room
			a.mu.Unlock()
			if !current {
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
	case p2p.MsgClaim:
		a.mu.Lock()
		if a.room != nil && !a.room.IsHost() && a.streamURL != "" {
			a.room.SendTo(inc.SenderID, p2p.Message{Type: p2p.MsgSnapshot, URL: a.streamURL,
				Position: a.lastState.Position, Paused: a.lastState.Paused})
		}
		a.isHost = a.room != nil && a.room.IsHost()
		a.mu.Unlock()
		runtime.EventsEmit(a.ctx, "room:host-change", "El creador recuperó el control")
		a.emitRoomState()
	case p2p.MsgSnapshot:
		a.mu.Lock()
		if a.room == nil || !a.room.IsHost() {
			a.mu.Unlock()
			return
		}
		wasPlaying := a.mpvClient != nil && a.streamURL == msg.URL
		a.streamURL = msg.URL
		a.lastState = PlaybackState{Position: msg.Position, Paused: msg.Paused}
		a.room.SetStreamURL(msg.URL)
		room := a.room
		a.mu.Unlock()
		room.Broadcast(p2p.Message{Type: p2p.MsgHello, Role: "host", URL: msg.URL, CreatorKey: room.CreatorKey(), Position: msg.Position, Paused: msg.Paused})
		if !wasPlaying {
			if err := a.launchMPV(msg.URL); err != nil {
				runtime.EventsEmit(a.ctx, "error", err.Error())
			} else {
				a.mu.Lock()
				client := a.mpvClient
				a.mu.Unlock()
				if client != nil {
					client.Pause()
					client.Seek(msg.Position)
					if !msg.Paused {
						client.Play()
					}
				}
			}
		} else {
			a.mu.Lock()
			client := a.mpvClient
			a.mu.Unlock()
			if client != nil {
				client.Seek(msg.Position)
				if msg.Paused {
					client.Pause()
				} else {
					client.Play()
				}
			}
		}
		a.emitRoomState()
	case p2p.MsgLeave:
		runtime.EventsEmit(a.ctx, "peer:departure", inc.SenderID)
	case p2p.MsgHello:
		a.mu.Lock()
		isHost := a.room != nil && a.room.IsHost()
		a.isHost = isHost
		trustedHost := a.room != nil && inc.SenderID == a.room.HostID()
		if trustedHost && msg.Role == "host" {
			if key := a.room.CreatorKey(); key != "" && a.lastSession.CreatorKey == "" && a.lastSession.RoomID == a.currentRoom {
				a.lastSession.CreatorKey = key
				if err := saveSession(a.lastSession); err != nil {
					log.Printf("app: cannot remember creator: %v", err)
				}
			}
		}
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
			if a.room == nil || inc.SenderID != a.room.HostID() {
				a.mu.Unlock()
				return
			}
			alreadyPlaying := (a.mpvClient != nil || a.playerClosed) && a.streamURL == msg.URL
			a.streamURL = msg.URL
			a.lastState = PlaybackState{Position: msg.Position, Paused: msg.Paused}
			if a.room != nil {
				a.room.SetStreamURL(msg.URL)
			}
			a.mu.Unlock()

			// Launch mpv with the received URL
			if alreadyPlaying {
				a.emitRoomState()
				return
			}
			if err := a.launchMPV(msg.URL); err != nil {
				log.Printf("app: error launching mpv: %v", err)
				runtime.EventsEmit(a.ctx, "error", err.Error())
				return
			}
			a.mu.Lock()
			client := a.mpvClient
			a.mu.Unlock()
			if client != nil {
				if msg.Paused {
					client.Pause()
				}
				if msg.Position > 0 {
					client.Seek(msg.Position)
				}
			}
			runtime.EventsEmit(a.ctx, "stream:received", msg.URL)
			a.emitRoomState()
		}
		a.emitRoomState()

	case p2p.MsgPlay, p2p.MsgPause, p2p.MsgSeek:
		a.mu.Lock()
		if a.room != nil && inc.SenderID == a.room.HostID() {
			a.lastState.Position = msg.Position
			if msg.Type == p2p.MsgPlay {
				a.lastState.Paused = false
			}
			if msg.Type == p2p.MsgPause {
				a.lastState.Paused = true
			}
		}
		a.mu.Unlock()

	case p2p.MsgSync:
		if a.room != nil && inc.SenderID == a.room.HostID() {
			a.mu.Lock()
			a.lastState = PlaybackState{Paused: msg.Paused, Position: msg.Position}
			a.mu.Unlock()
		}
		// Forward peer positions to the UI for the dashboard
		runtime.EventsEmit(a.ctx, "peer:position", map[string]interface{}{
			"peerId":   inc.SenderID,
			"position": msg.Position,
		})
	}
}

func (a *App) handleMPVEvents(client *mpv.Client) {
	for evt := range client.Events {
		a.mu.Lock()
		current := a.mpvClient == client
		a.mu.Unlock()
		if !current {
			return
		}
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
				restoring := a.restoring
				a.mu.Unlock()
				if ctrl != nil && isHost && !restoring {
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
	a.mu.Lock()
	if a.mpvClient == client {
		a.mpvClient = nil
		a.playerClosed = true
		a.controller = nil
		if a.syncCancel != nil {
			a.syncCancel()
			a.syncCancel = nil
		}
		if a.mpvCancel != nil {
			a.mpvCancel()
			a.mpvCancel = nil
		}
		if a.room != nil && a.room.IsHost() {
			a.lastState.Paused = true
			a.room.Broadcast(p2p.Message{Type: p2p.MsgPause, Position: a.lastState.Position})
		}
		a.mu.Unlock()
		runtime.EventsEmit(a.ctx, "playback:closed", nil)
		return
	}
	a.mu.Unlock()
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
		if a.room.IsHost() {
			var ids []string
			for _, p := range a.room.Peers() {
				ids = append(ids, p.ID)
			}
			sort.Strings(ids)
			if len(ids) > 0 {
				_ = a.room.TransferControl(ids[0])
			}
		}
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
	a.lastState = PlaybackState{Paused: true}
	a.playerClosed = false
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
