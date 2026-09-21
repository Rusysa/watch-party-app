// pipe_windows.go — Windows named pipe dial for mpv IPC
//go:build windows

package mpv

import (
	"crypto/rand"
	"fmt"
	"net"
	"time"

	winio "github.com/Microsoft/go-winio"
)

func newIPCPath() (string, func(), error) {
	var token [24]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", nil, err
	}
	return fmt.Sprintf(`\\.\pipe\mpv-watchparty-%x`, token), func() {}, nil
}

func dialNamedPipe(path string) (net.Conn, error) {
	return winio.DialPipe(path, durationPtr(2*time.Second))
}

func durationPtr(d time.Duration) *time.Duration { return &d }
