package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"watchparty/internal/p2p"
)

// Session stores room identity, never a password or a stream URL.
type Session struct {
	RoomID      string `json:"roomId"`
	SignalerURL string `json:"signalerUrl"`
	Creator     bool   `json:"creator"`
	CreatorKey  string `json:"creatorKey"`
	PrivateKey  string `json:"privateKey"`
}

func sessionPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "watchparty", "session.json"), nil
}

func readSession() Session {
	path, err := sessionPath()
	if err != nil {
		return Session{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Session{}
	}
	var s Session
	if json.Unmarshal(data, &s) != nil || !validRoomCode(s.RoomID) {
		return Session{}
	}
	if _, err := p2p.SignalerAddress(s.SignalerURL, "", ""); err != nil {
		return Session{}
	}
	if s.Creator {
		public, pubErr := base64.RawStdEncoding.DecodeString(s.CreatorKey)
		private, privErr := base64.RawStdEncoding.DecodeString(s.PrivateKey)
		if pubErr != nil || privErr != nil || len(public) != ed25519.PublicKeySize ||
			len(private) != ed25519.PrivateKeySize || !ed25519.PublicKey(public).Equal(ed25519.PublicKey(private[32:])) {
			s.Creator = false
			s.PrivateKey = ""
		}
	}
	return s
}

func validRoomCode(code string) bool {
	return len(code) == 6 && strings.Trim(code, roomCodeChars) == ""
}

func saveSession(s Session) error {
	path, err := sessionPath()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".session-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err = file.Chmod(0600); err != nil {
		return err
	}
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func newSession(roomID, signaler string) (Session, ed25519.PrivateKey, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Session{}, nil, err
	}
	return Session{RoomID: roomID, SignalerURL: signaler, Creator: true,
		CreatorKey: base64.RawStdEncoding.EncodeToString(public),
		PrivateKey: base64.RawStdEncoding.EncodeToString(private)}, private, nil
}
