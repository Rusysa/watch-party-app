// pipe_unix.go — Unix socket path for mpv IPC (Linux/Mac)
//go:build !windows

package mpv

import (
	"net"
	"os"
	"path/filepath"
)

func newIPCPath() (string, func(), error) {
	dir, err := os.MkdirTemp("", "watchparty-ipc-")
	if err != nil {
		return "", nil, err
	}
	return filepath.Join(dir, "mpv.sock"), func() { os.RemoveAll(dir) }, nil
}

func dialNamedPipe(path string) (net.Conn, error) {
	// Not called on Unix — dialIPC uses net.Dial("unix", ...) directly.
	return net.Dial("unix", path)
}
