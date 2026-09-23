package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Overridden by the Windows release build. Development builds never self-update.
var appVersion = "0.0.0"

const (
	releasesAPI   = "https://api.github.com/repos/Rusysa/watch-party-app/releases/latest"
	maxUpdateSize = 150 << 20
)

type UpdateStatus struct {
	Available bool   `json:"available"`
	Version   string `json:"version"`
	Error     string `json:"error"`
}

type availableUpdate struct {
	Version string
	Asset   releaseAsset
}

type pendingUpdate struct {
	Version string
	Path    string
	Digest  string
}

type releaseAsset struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	Digest      string `json:"digest"`
	DownloadURL string `json:"browser_download_url"`
}

type releaseInfo struct {
	TagName    string         `json:"tag_name"`
	Prerelease bool           `json:"prerelease"`
	Draft      bool           `json:"draft"`
	Assets     []releaseAsset `json:"assets"`
}

var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

func newerVersion(current, candidate string) bool {
	if !versionPattern.MatchString(current) || !versionPattern.MatchString(candidate) || current == "0.0.0" {
		return false
	}
	a, b := strings.Split(current, "."), strings.Split(candidate, ".")
	for i := 0; i < 3; i++ {
		x, e1 := strconv.ParseUint(a[i], 10, 32)
		y, e2 := strconv.ParseUint(b[i], 10, 32)
		if e1 != nil || e2 != nil {
			return false
		}
		if x != y {
			return y > x
		}
	}
	return false
}

func selectUpdate(release releaseInfo, current string) (*releaseAsset, string, error) {
	version := strings.TrimPrefix(release.TagName, "v")
	if release.Draft || release.Prerelease || !strings.HasPrefix(release.TagName, "v") || !newerVersion(current, version) {
		return nil, "", nil
	}
	// A distinct asset name keeps v0.2.0's unprompted updater from finding
	// these releases. Migrating from that version requires a manual install.
	name := "watchparty-" + version + "-windows-amd64-consent-installer.exe"
	for _, asset := range release.Assets {
		if asset.Name != name {
			continue
		}
		if asset.Size < 1 || asset.Size > maxUpdateSize {
			return nil, "", fmt.Errorf("invalid installer size")
		}
		if _, err := expectedDigest(asset.Digest); err != nil {
			return nil, "", err
		}
		u, err := url.Parse(asset.DownloadURL)
		if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.RawQuery != "" || u.Fragment != "" ||
			u.Path != "/Rusysa/watch-party-app/releases/download/"+release.TagName+"/"+name {
			return nil, "", fmt.Errorf("untrusted installer URL")
		}
		return &asset, version, nil
	}
	return nil, "", fmt.Errorf("release %s has no Windows installer", release.TagName)
}

func expectedDigest(raw string) ([]byte, error) {
	digest, err := hex.DecodeString(strings.TrimPrefix(raw, "sha256:"))
	if !strings.HasPrefix(raw, "sha256:") || err != nil || len(digest) != sha256.Size {
		return nil, fmt.Errorf("release has no valid SHA-256 digest")
	}
	return digest, nil
}

func latestUpdate(ctx context.Context, client *http.Client, current string) (*releaseAsset, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesAPI, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "WatchParty/"+current)
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("GitHub releases: HTTP %d", resp.StatusCode)
	}
	var release releaseInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return nil, "", err
	}
	return selectUpdate(release, current)
}

func downloadUpdate(ctx context.Context, client *http.Client, asset releaseAsset, dir string) (string, error) {
	want, err := expectedDigest(asset.Digest)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.DownloadURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "WatchParty/"+appVersion)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("installer download: HTTP %d", resp.StatusCode)
	}
	file, err := os.CreateTemp(dir, "watchparty-installer-*.exe")
	if err != nil {
		return "", err
	}
	defer func() {
		file.Close()
		if err != nil {
			os.Remove(file.Name())
		}
	}()
	hash := sha256.New()
	var count int64
	count, err = io.Copy(io.MultiWriter(file, hash), io.LimitReader(resp.Body, asset.Size+1))
	if err != nil {
		return "", err
	}
	if count != asset.Size || !bytes.Equal(want, hash.Sum(nil)) {
		err = fmt.Errorf("installer size or SHA-256 mismatch")
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	return filepath.Clean(file.Name()), nil
}
