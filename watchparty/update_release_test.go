package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectUpdate(t *testing.T) {
	asset := releaseAsset{
		Name: "watchparty-0.2.0-windows-amd64-installer.exe", Size: 100,
		Digest:      "sha256:" + strings.Repeat("ab", 32),
		DownloadURL: "https://github.com/Rusysa/watch-party-app/releases/download/v0.2.0/watchparty-0.2.0-windows-amd64-installer.exe",
	}
	for _, test := range []struct {
		name, current string
		change        func(*releaseInfo)
		want          bool
		wantError     bool
	}{
		{name: "newer stable release", current: "0.1.0", want: true},
		{name: "equal version", current: "0.2.0"},
		{name: "older release", current: "0.3.0"},
		{name: "development build", current: "0.0.0"},
		{name: "prerelease", current: "0.1.0", change: func(r *releaseInfo) { r.Prerelease = true }},
		{name: "missing digest", current: "0.1.0", change: func(r *releaseInfo) { r.Assets[0].Digest = "" }, wantError: true},
		{name: "redirected asset URL", current: "0.1.0", change: func(r *releaseInfo) { r.Assets[0].DownloadURL = "https://example.org/installer.exe" }, wantError: true},
		{name: "wrong asset size", current: "0.1.0", change: func(r *releaseInfo) { r.Assets[0].Size = maxUpdateSize + 1 }, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			release := releaseInfo{TagName: "v0.2.0", Assets: []releaseAsset{asset}}
			if test.change != nil {
				test.change(&release)
			}
			got, version, err := selectUpdate(release, test.current)
			if (got != nil) != test.want || (err != nil) != test.wantError {
				t.Fatalf("selectUpdate() = %v, %q, %v", got, version, err)
			}
			if test.want && version != "0.2.0" {
				t.Fatalf("unexpected version: %q", version)
			}
		})
	}
}

func TestDownloadUpdateVerifiesSizeAndDigest(t *testing.T) {
	data := []byte("test installer bytes")
	hash := sha256.Sum256(data)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(data)
	}))
	defer server.Close()
	asset := releaseAsset{DownloadURL: server.URL, Size: int64(len(data)), Digest: "sha256:" + hex.EncodeToString(hash[:])}
	for _, test := range []struct {
		name   string
		change func(*releaseAsset)
		ok     bool
	}{
		{name: "verified installer", ok: true},
		{name: "wrong hash", change: func(a *releaseAsset) { a.Digest = "sha256:" + strings.Repeat("ff", 32) }},
		{name: "truncated download", change: func(a *releaseAsset) { a.Size++ }},
		{name: "oversized download", change: func(a *releaseAsset) { a.Size-- }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := asset
			if test.change != nil {
				test.change(&candidate)
			}
			dir := t.TempDir()
			path, err := downloadUpdate(context.Background(), server.Client(), candidate, dir)
			if (err == nil) != test.ok {
				t.Fatalf("downloadUpdate() = %q, %v", path, err)
			}
			if test.ok {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != string(data) {
					t.Fatalf("installer contents = %q, %v", got, err)
				}
			} else {
				matches, err := filepath.Glob(filepath.Join(dir, "*.exe"))
				if err != nil || len(matches) != 0 {
					t.Fatalf("unverified installer left behind: %v, %v", matches, err)
				}
			}
		})
	}
}
