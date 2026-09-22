package mpv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/bodgit/sevenzip"
)

// githubRelease models the JSON response from GitHub API.
type githubRelease struct {
	Assets []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Digest             string `json:"digest"`
	} `json:"assets"`
}

// Manager handles detection and automatic installation of mpv on Windows.
type Manager struct {
	mu     sync.Mutex
	binDir string
}

var downloadClient = &http.Client{
	Timeout: 5 * time.Minute,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) >= 10 {
			return fmt.Errorf("unsafe or excessive download redirects")
		}
		return nil
	},
}

func NewManager() (*Manager, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("getting user config dir: %w", err)
	}
	binDir := filepath.Join(configDir, "WatchParty", "bin")
	if err := os.MkdirAll(binDir, 0700); err != nil {
		return nil, fmt.Errorf("creating bin dir: %w", err)
	}
	return &Manager{binDir: binDir}, nil
}

// GetMPVPath returns the absolute path to mpv.exe.
// If it doesn't exist, it returns an empty string.
func (m *Manager) GetMPVPath() string {
	if runtime.GOOS != "windows" {
		path, _ := exec.LookPath("mpv")
		return path
	}
	path := filepath.Join(m.binDir, "mpv.exe")
	// A marker means the executable and its runtime DLLs were extracted as a
	// complete, verified bundle. Older releases of WatchParty extracted only
	// mpv.exe, which cannot start with the Windows builds that depend on DLLs.
	marker := filepath.Join(m.binDir, ".mpv-installed")
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
		if markerInfo, markerErr := os.Lstat(marker); markerErr == nil && markerInfo.Mode().IsRegular() {
			return path
		}
	}
	return ""
}

// Install downloads and extracts mpv.exe and its Windows runtime if needed.
func (m *Manager) Install(ctx context.Context, onProgress func(string)) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if onProgress == nil {
		onProgress = func(string) {}
	}
	path := m.GetMPVPath()
	if path != "" {
		return path, nil // Already installed
	}
	if runtime.GOOS != "windows" {
		return "", fmt.Errorf("install mpv with your system package manager and ensure it is in PATH")
	}
	if runtime.GOARCH != "amd64" {
		return "", fmt.Errorf("automatic mpv installation supports Windows amd64 only")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	onProgress("Buscando última versión de mpv...")

	// 1. Fetch latest release from shinchiro
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/shinchiro/mpv-winbuild-cmake/releases/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching release info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api returned status %d", resp.StatusCode)
	}

	var rel githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&rel); err != nil {
		return "", fmt.Errorf("decoding release info: %w", err)
	}

	var downloadURL string
	var digest string
	for _, asset := range rel.Assets {
		// Baseline x86_64 also works on CPUs without x86-64-v3 instructions.
		if strings.HasPrefix(asset.Name, "mpv-x86_64-") && !strings.Contains(asset.Name, "-v3-") && strings.HasSuffix(asset.Name, ".7z") {
			downloadURL = asset.BrowserDownloadURL
			digest = strings.TrimPrefix(asset.Digest, "sha256:")
			break
		}
	}

	if downloadURL == "" {
		return "", fmt.Errorf("could not find a suitable mpv windows build in latest release")
	}
	expected, err := hex.DecodeString(digest)
	if err != nil || len(expected) != sha256.Size || !strings.HasPrefix(downloadURL, "https://github.com/shinchiro/mpv-winbuild-cmake/releases/download/") {
		return "", fmt.Errorf("release lacks a valid SHA-256 digest or trusted download URL")
	}

	onProgress("Descargando mpv (esto tomará un momento)...")

	// 2. Download the 7z archive to a temp file
	tmpFile, err := os.CreateTemp("", "mpv-*.7z")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	dlReq, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return "", err
	}
	dlResp, err := downloadClient.Do(dlReq)
	if err != nil {
		return "", fmt.Errorf("downloading mpv: %w", err)
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned status %d", dlResp.StatusCode)
	}

	hash := sha256.New()
	if err := copyLimited(io.MultiWriter(tmpFile, hash), dlResp.Body, 256<<20); err != nil {
		return "", fmt.Errorf("saving download: %w", err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return "", fmt.Errorf("mpv archive SHA-256 mismatch")
	}
	if err := tmpFile.Close(); err != nil {
		return "", err
	}

	onProgress("Extrayendo archivos...")

	// 3. Extract the executable and its DLL runtime from the verified archive.
	// mpv's Windows build is not a standalone executable: extracting only
	// mpv.exe leaves it unable to start when a required DLL is missing.
	r, err := sevenzip.OpenReader(tmpFile.Name())
	if err != nil {
		return "", fmt.Errorf("opening 7z archive: %w", err)
	}
	defer r.Close()

	var mpvExeFound bool
	var runtimeFiles int

	for _, f := range r.File {
		name := filepath.Base(f.Name)
		// Accept only flat runtime files. This prevents path traversal and avoids
		// installing configuration, scripts, documentation, or unrelated tools.
		if f.FileInfo().IsDir() || name != f.Name ||
			(name != "mpv.exe" && !strings.HasSuffix(strings.ToLower(name), ".dll")) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("opening %s in archive: %w", name, err)
		}
		out, err := os.CreateTemp(m.binDir, ".mpv-runtime-*")
		if err != nil {
			rc.Close()
			return "", fmt.Errorf("creating %s: %w", name, err)
		}
		err = copyLimited(out, rc, 512<<20)
		closeErr := out.Close()
		rc.Close()
		if err != nil {
			os.Remove(out.Name())
			return "", fmt.Errorf("extracting %s: %w", name, err)
		}
		if closeErr != nil {
			os.Remove(out.Name())
			return "", closeErr
		}
		target := filepath.Join(m.binDir, name)
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			os.Remove(out.Name())
			return "", fmt.Errorf("replacing %s: %w", name, err)
		}
		if err := os.Rename(out.Name(), target); err != nil {
			os.Remove(out.Name())
			return "", err
		}
		runtimeFiles++
		if name == "mpv.exe" {
			mpvExeFound = true
		}
	}

	if !mpvExeFound || runtimeFiles < 2 {
		return "", fmt.Errorf("mpv.exe or its Windows runtime DLLs were not found in archive")
	}
	if err := os.WriteFile(filepath.Join(m.binDir, ".mpv-installed"), []byte("verified\n"), 0600); err != nil {
		return "", fmt.Errorf("writing mpv installation marker: %w", err)
	}

	onProgress("¡Listo!")
	return filepath.Join(m.binDir, "mpv.exe"), nil
}

func copyLimited(dst io.Writer, src io.Reader, limit int64) error {
	n, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("file exceeds size limit")
	}
	if n == 0 {
		return fmt.Errorf("empty file")
	}
	return nil
}
