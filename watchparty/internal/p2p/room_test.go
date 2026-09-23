package p2p

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/url"
	"testing"
	"time"

	"watchparty/internal/transport"
)

func TestSignalerCredentialsCannotInjectQuery(t *testing.T) {
	password := "a&community=other+#?ñ"
	address, err := SignalerAddress("wss://example.org/signal?keep=yes", "ABC234", password)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(address)
	if u.Query().Get("password") != password || u.Query().Get("community") != "ABC234" || u.Query().Get("keep") != "yes" {
		t.Fatalf("credentials were not preserved: %v", u.Query())
	}
	for _, raw := range []string{"ws://example.org", "https://example.org", "wss:///path", "wss://user:secret@example.org", "wss://example.org/#fragment"} {
		if _, err := SignalerAddress(raw, "r", "p"); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, raw := range []string{"ws://localhost:15325", "ws://127.0.0.1:15325", "ws://[::1]:15325", "wss://example.org"} {
		if _, err := SignalerAddress(raw, "r", "p"); err != nil {
			t.Errorf("rejected %q: %v", raw, err)
		}
	}
}

func TestHostAuthorizationAndTransfer(t *testing.T) {
	r := NewRoom("", "", "", "peer", "")
	public, _, _ := ed25519.GenerateKey(rand.Reader)
	r.selfID = "self"
	for _, id := range []string{"host", "attacker", "next"} {
		r.peers[id] = &PeerInfo{ID: id, Role: "peer"}
	}
	if !r.acceptMessage("host", Message{Type: MsgHello, Role: "host", URL: "https://example.org/video", CreatorKey: base64.RawStdEncoding.EncodeToString(public)}) {
		t.Fatal("initial host not accepted")
	}
	for _, msg := range []Message{
		{Type: MsgHello, Role: "host", URL: "https://evil.example/video"},
		{Type: MsgHello, Role: "peer", URL: "https://evil.example/video"},
		{Type: MsgTransfer, TargetID: "attacker"},
		{Type: MsgPlay}, {Type: MsgPause}, {Type: MsgSeek},
		{Type: MsgSync, Position: math.Inf(1)},
		{Type: MsgSync, Position: -1},
	} {
		if r.acceptMessage("attacker", msg) {
			t.Errorf("accepted attack: %+v", msg)
		}
	}
	if r.HostID() != "host" {
		t.Fatal("attacker changed host")
	}
	if r.acceptMessage("host", Message{Type: MsgTransfer, TargetID: "absent"}) {
		t.Fatal("accepted nonexistent target")
	}
	if !r.acceptMessage("host", Message{Type: MsgTransfer, TargetID: "self", Term: 1}) || !r.IsHost() {
		t.Fatal("valid transfer failed")
	}
	if r.acceptMessage("host", Message{Type: MsgSeek}) {
		t.Fatal("old host still authorized")
	}
	if err := r.TransferControl("next"); err != nil {
		t.Fatal(err)
	}
	if r.IsHost() || r.HostID() != "next" || !r.acceptMessage("next", Message{Type: MsgPlay}) {
		t.Fatal("local transfer did not update authority")
	}
	if err := r.TransferControl("attacker"); err == nil {
		t.Fatal("non-host transferred control")
	}
}

func TestCreatorReturnRequiresProof(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRoom("", "ABC234", "", "peer", "")
	r.selfID = "self"
	r.SetCreatorIdentity(public, nil)
	r.hostID = "interim"
	for _, id := range []string{"interim", "creator", "attacker"} {
		r.peers[id] = &PeerInfo{ID: id}
	}
	now := time.Now().UnixMilli()
	claim := Message{Type: MsgClaim, Timestamp: now, Signature: base64.RawStdEncoding.EncodeToString(
		ed25519.Sign(private, []byte(fmt.Sprintf("ABC234:creator:%d", now))))}
	if r.acceptMessage("attacker", claim) || r.HostID() != "interim" {
		t.Fatal("replayed creator proof accepted for another peer")
	}
	if !r.acceptMessage("creator", claim) || r.HostID() != "creator" {
		t.Fatal("creator did not recover control")
	}
	if r.acceptMessage("interim", Message{Type: MsgPlay}) {
		t.Fatal("interim host still authorized")
	}
	if r.acceptMessage("attacker", Message{Type: MsgSnapshot, URL: "https://example.org/video"}) {
		t.Fatal("third party supplied a creator snapshot")
	}
}

func TestHostDisconnectElectsConnectedParticipant(t *testing.T) {
	r := NewRoom("", "ABC234", "", "peer", "")
	r.selfID, r.hostID = "b", "host"
	r.peers["host"] = &PeerInfo{ID: "host"}
	r.peers["c"] = &PeerInfo{ID: "c"}
	local, remote := net.Pipe()
	done := make(chan struct{})
	go func() {
		r.readPeer(context.Background(), "host", &transport.Peer{PeerID: "host", Conn: local})
		close(done)
	}()
	remote.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("peer reader did not notice disconnection")
	}
	if !r.IsHost() || r.HostID() != "b" {
		t.Fatal("surviving participant did not assume control")
	}
}

func TestPartitionedInterimHostsConverge(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := base64.RawStdEncoding.EncodeToString(public)
	a := NewRoom("", "ABC234", "", "host", "https://example.org/a")
	b := NewRoom("", "ABC234", "", "host", "https://example.org/b")
	a.selfID, a.hostID, a.term = "a", "a", 3
	b.selfID, b.hostID, b.term = "b", "b", 2
	a.SetCreatorIdentity(public, nil)
	b.SetCreatorIdentity(public, nil)
	a.peers["b"] = &PeerInfo{ID: "b"}
	b.peers["a"] = &PeerInfo{ID: "a"}
	if !b.acceptMessage("a", Message{Type: MsgHello, Role: "host", CreatorKey: key, Term: 3, URL: "https://example.org/a"}) || b.HostID() != "a" || b.IsHost() {
		t.Fatal("older partition did not follow newer host")
	}
	if a.acceptMessage("b", Message{Type: MsgHello, Role: "host", CreatorKey: key, Term: 2, URL: "https://example.org/b"}) || a.HostID() != "a" {
		t.Fatal("stale partition replaced controller")
	}
	// Two temporary hosts at the same revision choose the smaller ID.
	c := NewRoom("", "ABC234", "", "host", "")
	c.selfID, c.hostID, c.term = "c", "c", 3
	c.SetCreatorIdentity(public, nil)
	c.peers["a"] = &PeerInfo{ID: "a"}
	if !c.acceptMessage("a", Message{Type: MsgHello, Role: "host", CreatorKey: key, Term: 3}) || c.HostID() != "a" {
		t.Fatal("equal-revision hosts did not break the tie")
	}
	// A valid creator proof supersedes even a higher interim revision.
	c.peers["creator"] = &PeerInfo{ID: "creator"}
	now := time.Now().UnixMilli()
	proof := Message{Type: MsgClaim, Timestamp: now, Term: 1, Signature: base64.RawStdEncoding.EncodeToString(
		ed25519.Sign(private, []byte(fmt.Sprintf("ABC234:creator:%d", now))))}
	if !c.acceptMessage("creator", proof) || c.HostID() != "creator" {
		t.Fatal("signed creator proof did not supersede interim leader")
	}
	if c.acceptMessage("a", Message{Type: MsgHello, Role: "host", CreatorKey: key, Term: 999}) {
		t.Fatal("interim host displaced the proven creator")
	}
}

func TestCreatorClaimsOnlyOnReturn(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	created := NewRoom("", "ABC234", "", "host", "")
	created.SetCreatorIdentity(public, private)
	if created.claimOnJoin {
		t.Fatal("new room should not reclaim on first peer arrival")
	}
	returning := NewRoom("", "ABC234", "", "peer", "")
	returning.SetCreatorIdentity(public, private)
	if !returning.claimOnJoin {
		t.Fatal("returning creator cannot reclaim control")
	}
}

func TestLateGuestReceivesPausedPlayback(t *testing.T) {
	public, _, _ := ed25519.GenerateKey(rand.Reader)
	r := NewRoom("", "ABC234", "", "host", "")
	r.selfID, r.hostID = "host", "host"
	r.SetCreatorIdentity(public, nil)
	r.SetStreamURL("https://example.org/video")
	var sent Message
	r.peerConns["guest"] = func(data []byte) error { return json.Unmarshal(data, &sent) }
	r.Broadcast(Message{Type: MsgPause, Position: 87})
	r.sendHello("guest")
	if sent.Type != MsgHello || !sent.Paused || sent.Position != 87 || sent.URL != "https://example.org/video" {
		t.Fatalf("late guest received wrong playback state: %+v", sent)
	}
}

func TestStreamURLValidation(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "--script=evil", "http:relative", "https:///video", "https://user:password@example.org/video", "ftp://example.org/video"} {
		if ValidateStreamURL(raw) == nil {
			t.Errorf("accepted unsafe URL %q", raw)
		}
	}
	if err := ValidateStreamURL("https://example.org/video?token=abc%26def"); err != nil {
		t.Fatal(err)
	}
}
