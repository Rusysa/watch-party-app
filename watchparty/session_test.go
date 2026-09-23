package main

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestSessionPersistsCreatorWithoutPasswordOrVideoURL(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("APPDATA", configDir)
	s, private, err := newSession("ABC234", "wss://example.org/")
	if err != nil || len(private) == 0 {
		t.Fatal(err)
	}
	if err := saveSession(s); err != nil {
		t.Fatal(err)
	}
	loaded := readSession()
	if loaded.RoomID != s.RoomID || loaded.PrivateKey != s.PrivateKey || !loaded.Creator {
		t.Fatalf("room identity was not restored: %+v", loaded)
	}
	path, _ := sessionPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "https://example.org/video") || strings.Contains(string(data), "password") {
		t.Fatal("session file contains stream URL or password")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows reports file permissions differently; access follows the user's ACL.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("unexpected session permissions: %v, %v", info, err)
	}
}
