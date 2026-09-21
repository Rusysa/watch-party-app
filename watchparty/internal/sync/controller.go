// Package sync implements the Syncplay-style drift correction algorithm for
// keeping all peers synchronized with the host's playback position.
package sync

import (
	"context"
	"log"
	gosync "sync"
	"time"

	"watchparty/internal/mpv"
	"watchparty/internal/p2p"
)

// ─────────────────────────────────────────────────────────────────────────────
// Configuration
// ─────────────────────────────────────────────────────────────────────────────

const (
	// HeartbeatInterval is how often we send our position to peers.
	HeartbeatInterval = 2 * time.Second

	// DriftThresholdSeek is the drift (seconds) above which we do a hard seek.
	// If we're more than 2s behind the host → seek forward.
	DriftThresholdSeek = 2.0

	// DriftThresholdSlow is the drift (seconds) below which we slow down.
	// If we're more than 1s ahead of the host → reduce speed.
	DriftThresholdSlow = 1.0

	// SlowSpeed is the playback speed used to catch up when slightly ahead.
	SlowSpeed = 0.85

	// NormalSpeed is the normal playback speed.
	NormalSpeed = 1.0

	// DriftHistoryLen is the number of samples for the moving average.
	DriftHistoryLen = 5
)

// ─────────────────────────────────────────────────────────────────────────────
// Controller
// ─────────────────────────────────────────────────────────────────────────────

// Controller ties the mpv client and the P2P room together.
// It is responsible for:
//   - As host: broadcasting play/pause/seek commands and position heartbeats.
//   - As peer: receiving commands from the host and correcting drift.
type Controller struct {
	mu   gosync.Mutex
	mpv  *mpv.Client
	room *p2p.Room

	// drift moving average (peer only)
	driftHistory []float64

	// lastManualSeek prevents correction during manual user interaction
	lastManualSeek time.Time

	// IncomingCh receives messages forwarded from the app layer.
	// This avoids competing with the app for messages from room.Incoming.
	IncomingCh chan p2p.IncomingMsg
}

// New creates a new sync controller.
// The room is the single source of truth for host authority.
func New(mpvClient *mpv.Client, room *p2p.Room) *Controller {
	return &Controller{
		mpv:        mpvClient,
		room:       room,
		IncomingCh: make(chan p2p.IncomingMsg, 128),
	}
}

// Run starts the sync control loop. Blocks until ctx is cancelled.
func (c *Controller) Run(ctx context.Context) {
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			c.sendHeartbeat()

		case inc := <-c.IncomingCh:
			c.handleMessage(ctx, inc)
		}
	}
}

// NotifyPlay should be called when the local user presses Play (host only).
func (c *Controller) NotifyPlay(pos float64) {
	c.mu.Lock()
	host := c.room.IsHost()
	c.mu.Unlock()
	if !host {
		return
	}
	c.room.Broadcast(p2p.Message{
		Type:     p2p.MsgPlay,
		Position: pos,
	})
}

// NotifyPause should be called when the local user presses Pause (host only).
func (c *Controller) NotifyPause(pos float64) {
	c.mu.Lock()
	host := c.room.IsHost()
	c.mu.Unlock()
	if !host {
		return
	}
	c.room.Broadcast(p2p.Message{
		Type:     p2p.MsgPause,
		Position: pos,
	})
}

// NotifySeek should be called when the local user seeks (host only).
func (c *Controller) NotifySeek(pos float64) {
	c.mu.Lock()
	host := c.room.IsHost()
	if !host {
		c.mu.Unlock()
		return
	}
	c.lastManualSeek = time.Now()
	c.mu.Unlock()
	c.room.Broadcast(p2p.Message{
		Type:     p2p.MsgSeek,
		Position: pos,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal
// ─────────────────────────────────────────────────────────────────────────────

func (c *Controller) sendHeartbeat() {
	pos, err := c.mpv.GetPosition()
	if err != nil {
		return
	}
	paused := c.mpv.IsPaused() || c.mpv.IsBuffering()
	c.room.Broadcast(p2p.Message{
		Type:     p2p.MsgSync,
		Position: pos,
		Paused:   paused,
	})
}

func (c *Controller) handleMessage(ctx context.Context, inc p2p.IncomingMsg) {
	msg := inc.Msg

	c.mu.Lock()
	isHost := c.room.IsHost()
	hostID := c.room.HostID()
	c.mu.Unlock()

	switch msg.Type {
	case p2p.MsgHello:
		// Host discovery is validated by Room; App handles the URL.
		return

	case p2p.MsgPlay:
		if isHost {
			return // ignore commands from peers if we're the host
		}
		if inc.SenderID != hostID {
			return
		}
		log.Printf("sync: received PLAY from host at %.2fs", msg.Position)
		// Sync position before playing
		if err := c.mpv.Seek(msg.Position); err != nil {
			log.Printf("sync: seek error: %v", err)
		}
		c.mpv.Play()

	case p2p.MsgPause:
		if isHost || inc.SenderID != hostID {
			return
		}
		log.Printf("sync: received PAUSE from host at %.2fs", msg.Position)
		c.mpv.Seek(msg.Position)
		c.mpv.Pause()

	case p2p.MsgSeek:
		if isHost || inc.SenderID != hostID {
			return
		}
		log.Printf("sync: received SEEK from host to %.2fs", msg.Position)
		c.mu.Lock()
		c.lastManualSeek = time.Now()
		c.mu.Unlock()
		c.mpv.Seek(msg.Position)

	case p2p.MsgSync:
		if isHost || inc.SenderID != hostID {
			return
		}
		c.correctDrift(msg)

	case p2p.MsgTransfer:
		// Room validates and applies transfers before dispatching them.
		return
	}
}

// correctDrift applies the Syncplay-style drift correction algorithm.
func (c *Controller) correctDrift(hostMsg p2p.Message) {
	// Don't correct during recent manual seeks
	c.mu.Lock()
	if time.Since(c.lastManualSeek) < 5*time.Second {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	myPos, err := c.mpv.GetPosition()
	if err != nil {
		return
	}

	isPeerBuffering := c.mpv.IsBuffering()
	if isPeerBuffering && !hostMsg.Paused {
		log.Println("sync: peer is buffering, skipping drift correction to avoid interrupting cache")
		return
	}

	// Account for network latency: estimate RTT/2 from timestamp
	// Bound to max 1.0s to prevent massive desyncs if system clocks are not synchronized
	networkDelay := float64(time.Now().UnixMilli()-hostMsg.Timestamp) / 1000.0
	if networkDelay < 0 || networkDelay > 1.0 {
		networkDelay = 0.5
	}
	expectedHostPos := hostMsg.Position + networkDelay

	// drift = how far behind the host we are (positive = we're behind)
	drift := expectedHostPos - myPos

	// Add to moving average
	c.mu.Lock()
	c.driftHistory = append(c.driftHistory, drift)
	if len(c.driftHistory) > DriftHistoryLen {
		c.driftHistory = c.driftHistory[1:]
	}
	avgDrift := average(c.driftHistory)
	c.mu.Unlock()

	log.Printf("sync: drift=%.3fs avg=%.3fs (host=%.2fs me=%.2fs)", drift, avgDrift, expectedHostPos, myPos)

	if avgDrift > DriftThresholdSeek {
		// Too far behind → hard seek forward
		log.Printf("sync: correcting (seek forward by %.2fs)", avgDrift)
		c.mpv.Seek(expectedHostPos)
		c.mu.Lock()
		c.driftHistory = nil // reset history after correction
		c.mu.Unlock()
	} else if avgDrift < -DriftThresholdSeek {
		// Too far ahead by a lot (e.g. host rewinded) → hard seek backward
		log.Printf("sync: correcting (seek backward by %.2fs)", -avgDrift)
		c.mpv.Seek(expectedHostPos)
		c.mu.Lock()
		c.driftHistory = nil
		c.mu.Unlock()
	} else if avgDrift < -DriftThresholdSlow {
		// Slightly ahead → slow down
		log.Printf("sync: correcting (slow down, ahead by %.2fs)", -avgDrift)
		c.mpv.SetSpeed(SlowSpeed)
	} else {
		// In tolerance → normal speed
		c.mpv.SetSpeed(NormalSpeed)
	}

	// Apply paused state if it differs
	if hostMsg.Paused && !c.mpv.IsPaused() {
		log.Println("sync: host is paused, pausing peer")
		c.mpv.Pause()
	} else if !hostMsg.Paused && c.mpv.IsPaused() {
		log.Println("sync: host is playing, resuming peer")
		c.mpv.Play()
	}
}

func average(s []float64) float64 {
	if len(s) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range s {
		sum += v
	}
	return sum / float64(len(s))
}
