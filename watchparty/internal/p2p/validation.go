package p2p

import (
	"fmt"
	"math"
	"net"
	"net/url"
)

// SignalerAddress encodes credentials and requires TLS except on loopback.
func SignalerAddress(raw, room, password string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return "", fmt.Errorf("invalid signaler URL")
	}
	ip := net.ParseIP(u.Hostname())
	local := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "wss" && !(u.Scheme == "ws" && local) {
		return "", fmt.Errorf("signaler requires wss (ws is allowed only on loopback)")
	}
	q := u.Query()
	q.Set("community", room)
	q.Set("password", password)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ValidateStreamURL rejects local file schemes, opaque URLs and credentials.
func ValidateStreamURL(raw string) error {
	u, err := url.Parse(raw)
	if len(raw) > 16384 || err != nil || u.Hostname() == "" || u.User != nil ||
		(u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" {
		return fmt.Errorf("stream must be an absolute http/https URL without user credentials")
	}
	return nil
}

func ValidPosition(pos float64) bool {
	return !math.IsNaN(pos) && !math.IsInf(pos, 0) && pos >= 0 && pos <= 31536000
}
