package transport

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

// relay implements the Weron server's opaque, same-community broadcast contract.
// It does not decrypt SDP or terminate ICE, DTLS or SCTP: those run in real Pion
// connections between adapters over loopback, without public STUN or credentials.
type relay struct {
	mu       sync.Mutex
	clients  map[*websocket.Conn]bool
	arrivals chan struct{}
}

func newRelay(t *testing.T) (*relay, string) {
	t.Helper()
	r := &relay{clients: make(map[*websocket.Conn]bool), arrivals: make(chan struct{}, 16)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("community") != "test" || req.URL.Query().Get("password") != "test-password" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, req, nil)
		if err != nil {
			return
		}
		r.mu.Lock()
		r.clients[conn] = true
		r.mu.Unlock()
		r.arrivals <- struct{}{}
		defer func() {
			r.mu.Lock()
			delete(r.clients, conn)
			r.mu.Unlock()
			conn.Close()
		}()
		for {
			kind, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			r.mu.Lock()
			for other := range r.clients {
				if other != conn {
					other.SetWriteDeadline(time.Now().Add(time.Second))
					other.WriteMessage(kind, data)
				}
			}
			r.mu.Unlock()
		}
	}))
	t.Cleanup(func() { r.disconnect(); server.Close() })
	return r, "ws" + strings.TrimPrefix(server.URL, "http") + "?community=test&password=test-password"
}

func (r *relay) disconnect() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for conn := range r.clients {
		conn.Close()
	}
}

func testAdapter(t *testing.T, address, id string) *Adapter {
	t.Helper()
	a, err := New(context.Background(), address, "test-password", nil)
	if err != nil {
		t.Fatal(err)
	}
	a.id = id
	a.retryDelay = 25 * time.Millisecond
	settings := webrtc.SettingEngine{}
	settings.DetachDataChannels()
	settings.SetIncludeLoopbackCandidate(true)
	settings.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	a.api = webrtc.NewAPI(webrtc.WithSettingEngine(settings))
	if err := a.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

func acceptPeer(t *testing.T, a *Adapter) *Peer {
	t.Helper()
	select {
	case peer, ok := <-a.Accept():
		if !ok {
			t.Fatal("adapter stopped before connecting")
		}
		t.Cleanup(func() { peer.Conn.Close() })
		return peer
	case <-time.After(10 * time.Second):
		t.Fatal("WebRTC connection timed out")
		return nil
	}
}

func exchange(t *testing.T, from, to *Peer) {
	t.Helper()
	payload := []byte("{\"type\":\"play\",\"pos\":42}\n")
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, len(payload))
		_, err := io.ReadFull(to.Conn, buf)
		if err == nil && !bytes.Equal(payload, buf) {
			err = io.ErrUnexpectedEOF
		}
		done <- err
	}()
	if _, err := from.Conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("data did not cross the DTLS/SCTP connection")
	}
}

func TestWebRTCExchangeAndReconnect(t *testing.T) {
	// Both arrival orders exercise the offer election and targeted introduction.
	for _, ids := range [][2]string{{"a", "z"}, {"z", "a"}} {
		t.Run(ids[0]+"-first", func(t *testing.T) {
			r, address := newRelay(t)
			a := testAdapter(t, address, ids[0])
			<-r.arrivals
			b := testAdapter(t, address, ids[1])
			<-r.arrivals
			ap, bp := acceptPeer(t, a), acceptPeer(t, b)
			if ap.PeerID != b.ID() || bp.PeerID != a.ID() || ap.ChannelID != Channel {
				t.Fatal("incorrect remote identity or channel")
			}
			exchange(t, ap, bp)
			exchange(t, bp, ap)
			r.disconnect()
			ap, bp = acceptPeer(t, a), acceptPeer(t, b)
			if ap.PeerID != ids[1] || bp.PeerID != ids[0] {
				t.Fatal("identity changed after reconnect")
			}
			exchange(t, ap, bp)
		})
	}
}

func TestThreePeerMesh(t *testing.T) {
	r, address := newRelay(t)
	a := testAdapter(t, address, "b")
	<-r.arrivals
	b := testAdapter(t, address, "c")
	<-r.arrivals
	ap, bp := acceptPeer(t, a), acceptPeer(t, b)
	exchange(t, ap, bp)
	c := testAdapter(t, address, "a")
	ac, bc := acceptPeer(t, a), acceptPeer(t, b)
	for i := 0; i < 2; i++ {
		cp := acceptPeer(t, c)
		switch cp.PeerID {
		case a.ID():
			exchange(t, ac, cp)
		case b.ID():
			exchange(t, bc, cp)
		default:
			t.Fatalf("unexpected peer: %q", cp.PeerID)
		}
	}
}

func TestSignalingWireCompatibility(t *testing.T) {
	// Independent legacy codec, to verify the deployed Weron envelope format.
	password := "test-password"
	hash := sha256.Sum224([]byte(password))
	key := make([]byte, 32)
	copy(key, hash[:])
	block, _ := aes.NewCipher(key)
	legacy, _ := cipher.NewGCM(block)
	current, err := signalingCipher(password)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(`{"type":"introduction","from":"test"}`)
	encrypted := current.Seal(nil, nil, plain, nil)
	decoded, err := legacy.Open(nil, encrypted[:12], encrypted[12:], nil)
	if err != nil || !bytes.Equal(decoded, plain) {
		t.Fatalf("new envelope incompatible with Weron: %v", err)
	}
	nonce := bytes.Repeat([]byte{7}, 12)
	legacyEnvelope := legacy.Seal(nonce, nonce, plain, nil)
	decoded, err = current.Open(nil, nil, legacyEnvelope, nil)
	if err != nil || !bytes.Equal(decoded, plain) {
		t.Fatalf("cannot decode legacy envelope: %v", err)
	}
	for size := 0; size < current.Overhead(); size++ {
		if _, err := current.Open(nil, nil, make([]byte, size), nil); err == nil {
			t.Fatalf("accepted truncated envelope of %d bytes", size)
		}
	}
	wrong, _ := signalingCipher("wrong-password")
	if _, err := wrong.Open(nil, nil, encrypted, nil); err == nil {
		t.Fatal("accepted wrong signaling key")
	}
	encrypted[len(encrypted)-1] ^= 1
	if _, err := current.Open(nil, nil, encrypted, nil); err == nil {
		t.Fatal("accepted tampered signaling")
	}
}

func TestOpenFailureAndCancellation(t *testing.T) {
	_, address := newRelay(t)
	a, _ := New(context.Background(), address+"wrong", "test-password", nil)
	if err := a.Open(); err == nil || strings.Contains(err.Error(), "password") {
		t.Fatalf("expected redacted connection error, got %v", err)
	}
	a.Close()
	b := testAdapter(t, address, "alone")
	done := make(chan struct{})
	go func() { b.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation blocked on WebSocket read")
	}
	if _, ok := <-b.Accept(); ok {
		t.Fatal("accept channel not closed")
	}
}

// Optional integration against the actual pinned Weron container. The normal
// suite remains offline; see docs/render.md for running this check locally.
func TestWeronSignaler(t *testing.T) {
	address := os.Getenv("WATCHPARTY_TEST_SIGNALER_URL")
	if address == "" {
		t.Skip("set WATCHPARTY_TEST_SIGNALER_URL to test a running Weron signaler")
	}
	u, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("community", "integration-"+uuid.NewString())
	q.Set("password", "test-password")
	u.RawQuery = q.Encode()
	a := testAdapter(t, u.String(), "z")
	b := testAdapter(t, u.String(), "a")
	ap, bp := acceptPeer(t, a), acceptPeer(t, b)
	exchange(t, ap, bp)
	exchange(t, bp, ap)
}
