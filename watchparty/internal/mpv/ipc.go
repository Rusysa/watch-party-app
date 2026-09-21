// Package mpv provides an IPC adapter for controlling a running mpv process
// via its JSON IPC protocol over a named pipe (Windows) or Unix socket (Linux/Mac).
package mpv

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	gosync "sync"
	"time"

	"watchparty/internal/p2p"
)

// Event represents an event or property change received from mpv.
type Event struct {
	Event string      `json:"event"`
	Name  string      `json:"name"`
	Data  interface{} `json:"data"`
	ID    int         `json:"id"`
	Error string      `json:"error"`
}

// ipcRequest is the JSON structure sent to mpv.
type ipcRequest struct {
	Command   []interface{} `json:"command"`
	RequestID int           `json:"request_id,omitempty"`
}

// ipcResponse is the JSON structure received from mpv.
type ipcResponse struct {
	Data      interface{} `json:"data"`
	Error     string      `json:"error"`
	RequestID int         `json:"request_id"`
	Event     string      `json:"event"`
	Name      string      `json:"name"`
}

// Client manages a connection to a running mpv instance.
type Client struct {
	mu    gosync.Mutex
	conn  net.Conn
	reqID int

	// pending maps request_id → response channel
	pending   map[int]chan ipcResponse
	pendingMu gosync.Mutex

	// Events delivers named mpv events (play, pause, property-change, etc.)
	Events chan Event

	cmd    *exec.Cmd
	cancel context.CancelFunc
}

// Launch starts a new mpv process with the given URL and connects to its IPC.
func Launch(ctx context.Context, mpvPath, url string) (*Client, error) {
	if err := p2p.ValidateStreamURL(url); err != nil {
		return nil, err
	}
	pipePath, cleanup, err := newIPCPath()
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithCancel(ctx)

	args := []string{
		"--no-config",
		"--load-scripts=no",
		"--ytdl=no",
		"--input-ipc-server=" + pipePath,
		"--idle=yes",
		"--keep-open=yes",
		"--force-window=yes",
		"--no-terminal",
		"--", // stop flag interpretation — prevents URL injection
		url,
	}

	cmd := exec.CommandContext(pctx, mpvPath, args...)

	if err := cmd.Start(); err != nil {
		cancel()
		cleanup()
		return nil, fmt.Errorf("launching mpv: %w", err)
	}
	go func() {
		cmd.Wait()
		cancel()
		cleanup()
	}()

	// Wait for mpv to create the IPC socket/pipe (up to 6s)
	var conn net.Conn
	for i := 0; i < 20; i++ {
		time.Sleep(300 * time.Millisecond)
		conn, err = dialIPC(pipePath)
		if err == nil {
			break
		}
	}
	if err != nil {
		cancel()
		cmd.Process.Kill()
		return nil, fmt.Errorf("connecting to mpv IPC after 6s: %w", err)
	}

	c := &Client{
		conn:    conn,
		pending: make(map[int]chan ipcResponse),
		Events:  make(chan Event, 64),
		cmd:     cmd,
		cancel:  cancel,
	}

	go c.readLoop(pctx)
	context.AfterFunc(pctx, func() { conn.Close() })
	go c.observeProps(pctx)

	return c, nil
}

// Close terminates the mpv process and IPC connection.
func (c *Client) Close() {
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		c.conn.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		c.cmd.Process.Kill()
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Public commands
// ─────────────────────────────────────────────────────────────────────────────

// Play resumes playback.
func (c *Client) Play() error { return c.setProperty("pause", false) }

// Pause pauses playback.
func (c *Client) Pause() error { return c.setProperty("pause", true) }

// Seek seeks to the given absolute position in seconds.
func (c *Client) Seek(pos float64) error {
	return c.sendCommand("seek", pos, "absolute")
}

// SetSpeed sets the playback speed (1.0 = normal).
func (c *Client) SetSpeed(speed float64) error {
	return c.setProperty("speed", speed)
}

// GetPosition returns the current playback position in seconds.
func (c *Client) GetPosition() (float64, error) {
	resp, err := c.sendSync("get_property", "time-pos")
	if err != nil {
		return 0, err
	}
	if v, ok := resp.Data.(float64); ok {
		return v, nil
	}
	return 0, nil
}

// GetPaused returns whether mpv is currently paused.
func (c *Client) GetPaused() (bool, error) {
	resp, err := c.sendSync("get_property", "pause")
	if err != nil {
		return true, err
	}
	if v, ok := resp.Data.(bool); ok {
		return v, nil
	}
	return false, nil
}

// IsPaused returns paused state, defaulting to true on error.
func (c *Client) IsPaused() bool {
	v, _ := c.GetPaused()
	return v
}

// IsBuffering returns true if the player is buffering (paused for cache).
func (c *Client) IsBuffering() bool {
	resp, err := c.sendSync("get_property", "paused-for-cache")
	if err == nil {
		if v, ok := resp.Data.(bool); ok {
			return v
		}
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

func (c *Client) nextID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqID++
	return c.reqID
}

func (c *Client) sendCommand(args ...interface{}) error {
	id := c.nextID()
	req := ipcRequest{Command: args, RequestID: id}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	c.mu.Lock()
	_, err = c.conn.Write(data)
	c.mu.Unlock()
	return err
}

func (c *Client) setProperty(name string, value interface{}) error {
	return c.sendCommand("set_property", name, value)
}

// sendSync sends a command and waits synchronously for the response.
func (c *Client) sendSync(args ...interface{}) (ipcResponse, error) {
	id := c.nextID()
	ch := make(chan ipcResponse, 1)

	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	req := ipcRequest{Command: args, RequestID: id}
	data, _ := json.Marshal(req)
	data = append(data, '\n')

	c.mu.Lock()
	_, err := c.conn.Write(data)
	c.mu.Unlock()
	if err != nil {
		return ipcResponse{}, err
	}

	select {
	case resp := <-ch:
		if resp.Error != "" && resp.Error != "success" {
			return resp, fmt.Errorf("mpv: %s", resp.Error)
		}
		return resp, nil
	case <-time.After(2 * time.Second):
		return ipcResponse{}, fmt.Errorf("mpv: timeout waiting for response")
	}
}

// readLoop is the single goroutine that reads all mpv output.
// It dispatches responses to pending channels and events to c.Events.
func (c *Client) readLoop(ctx context.Context) {
	defer close(c.Events)
	scanner := bufio.NewScanner(c.conn)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if !scanner.Scan() {
			return
		}
		var resp ipcResponse
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			continue
		}

		if resp.RequestID > 0 {
			// This is a response to a pending sendSync call
			c.pendingMu.Lock()
			ch, ok := c.pending[resp.RequestID]
			c.pendingMu.Unlock()
			if ok {
				select {
				case ch <- resp:
				default:
				}
			}
			continue
		}

		// It's an event — send to Events channel
		if resp.Event != "" {
			evt := Event{
				Event: resp.Event,
				Name:  resp.Name,
				Data:  resp.Data,
			}
			select {
			case c.Events <- evt:
			default:
			}
		}
	}
}

// observeProps registers property observers so mpv pushes changes to us.
func (c *Client) observeProps(ctx context.Context) {
	time.Sleep(500 * time.Millisecond)
	c.sendCommand("observe_property", 1, "time-pos")
	c.sendCommand("observe_property", 2, "pause")
	<-ctx.Done()
}

// dialIPC connects to the mpv IPC endpoint (platform-specific).
func dialIPC(pipePath string) (net.Conn, error) {
	if runtime.GOOS == "windows" {
		return dialNamedPipe(pipePath)
	}
	return net.Dial("unix", pipePath)
}
