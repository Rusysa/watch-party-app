package mpv

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCopyLimited(t *testing.T) {
	for _, tc := range []struct {
		data string
		fail bool
	}{{"", true}, {"abcd", false}, {"abcde", true}} {
		var out bytes.Buffer
		err := copyLimited(&out, strings.NewReader(tc.data), 4)
		if (err != nil) != tc.fail {
			t.Errorf("copy %q: %v", tc.data, err)
		}
	}
}

func TestIPCIsolation(t *testing.T) {
	a, cleanA, err := newIPCPath()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanA()
	b, cleanB, err := newIPCPath()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanB()
	if a == b {
		t.Fatal("IPC endpoint reused")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Dir(a))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0700 {
			t.Fatalf("IPC directory is not private: %v", info.Mode())
		}
		cleanA()
		if _, err := os.Stat(filepath.Dir(a)); !os.IsNotExist(err) {
			t.Fatal("IPC directory not removed")
		}
	}
}
