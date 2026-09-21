package p2p

import (
	"math"
	"net/url"
	"testing"
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
	r.selfID = "self"
	for _, id := range []string{"host", "attacker", "next"} {
		r.peers[id] = &PeerInfo{ID: id, Role: "peer"}
	}
	if !r.acceptMessage("host", Message{Type: MsgHello, Role: "host", URL: "https://example.org/video"}) {
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
	if !r.acceptMessage("host", Message{Type: MsgTransfer, TargetID: "self"}) || !r.IsHost() {
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
