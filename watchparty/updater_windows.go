package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const userInstallKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\rusysaWatch Party`

// Only user-scoped NSIS installations can be replaced silently without UAC.
// Portable builds and the older machine-wide installer are left alone.
func installedForUser() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER, userInstallKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	location, _, err := key.GetStringValue("InstallLocation")
	if err != nil || location == "" {
		return false
	}
	executable, err := os.Executable()
	if err != nil || !strings.EqualFold(filepath.Base(executable), "watchparty.exe") {
		return false
	}
	return strings.EqualFold(filepath.Clean(filepath.Dir(executable)), filepath.Clean(location))
}

func updateCacheDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "watchparty", "updates")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

func cleanOldUpdates(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !(strings.HasPrefix(name, "watchparty-updater-") || strings.HasPrefix(name, "watchparty-installer-")) || !strings.HasSuffix(name, ".exe") {
			continue
		}
		info, err := entry.Info()
		if err == nil && time.Since(info.ModTime()) > 7*24*time.Hour {
			os.Remove(filepath.Join(dir, name))
		}
	}
}

func (a *App) startAutoUpdater(ctx context.Context) {
	if !installedForUser() || !versionPattern.MatchString(appVersion) || appVersion == "0.0.0" {
		return
	}
	updateCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.updateCancel = cancel
	a.mu.Unlock()
	go func() {
		if dir, err := updateCacheDir(); err == nil {
			cleanOldUpdates(dir)
		}
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			if err := a.stageLatestUpdate(updateCtx); err != nil && updateCtx.Err() == nil {
				log.Printf("automatic update: %v", err)
			}
			select {
			case <-updateCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (a *App) stageLatestUpdate(ctx context.Context) error {
	a.mu.Lock()
	ready := a.updatePending != nil
	a.mu.Unlock()
	if ready {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	asset, version, err := latestUpdate(ctx, &http.Client{Timeout: 20 * time.Second}, appVersion)
	if err != nil || asset == nil {
		return err
	}
	dir, err := updateCacheDir()
	if err != nil {
		return err
	}
	client := &http.Client{CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		host := req.URL.Hostname()
		if req.URL.Scheme != "https" || (host != "github.com" && host != "release-assets.githubusercontent.com" && host != "objects.githubusercontent.com") {
			return fmt.Errorf("untrusted installer redirect")
		}
		return nil
	}}
	path, err := downloadUpdate(ctx, client, *asset, dir)
	if err != nil {
		return err
	}
	a.mu.Lock()
	if ctx.Err() != nil || a.updatePending != nil {
		a.mu.Unlock()
		os.Remove(path)
		return ctx.Err()
	}
	a.updatePending = &pendingUpdate{Version: version, Path: path, Digest: asset.Digest}
	a.mu.Unlock()
	runtime.EventsEmit(a.ctx, "update:ready", UpdateStatus{Ready: true, Version: version})
	return nil
}

// Wails calls this while its window is closing. The helper waits for this
// process to exit before NSIS replaces the executable, then restarts the app.
func (a *App) finishAutoUpdate() {
	a.mu.Lock()
	pending, cancel := a.updatePending, a.updateCancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if pending == nil {
		return
	}
	self, err := os.Executable()
	if err != nil {
		log.Printf("automatic update: %v", err)
		return
	}
	dir, err := updateCacheDir()
	if err != nil {
		log.Printf("automatic update: %v", err)
		return
	}
	helper, err := copyUpdateHelper(self, dir)
	if err != nil {
		log.Printf("automatic update: %v", err)
		return
	}
	cmd := exec.Command(helper, "--watchparty-update-helper", strconv.Itoa(os.Getpid()), pending.Path, pending.Digest, self)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		os.Remove(helper)
		log.Printf("automatic update: %v", err)
		return
	}
	cmd.Process.Release()
}

func copyUpdateHelper(self, dir string) (string, error) {
	src, err := os.Open(self)
	if err != nil {
		return "", err
	}
	defer src.Close()
	dst, err := os.CreateTemp(dir, "watchparty-updater-*.exe")
	if err != nil {
		return "", err
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(dst.Name())
		return "", err
	}
	if err := dst.Close(); err != nil {
		os.Remove(dst.Name())
		return "", err
	}
	return dst.Name(), nil
}

func applyUpdate(args []string) int {
	if err := installUpdate(args); err != nil {
		log.Printf("automatic update: %v", err)
		if dir, dirErr := updateCacheDir(); dirErr == nil {
			os.WriteFile(filepath.Join(dir, "last-error.txt"), []byte(err.Error()), 0600)
		}
		return 1
	}
	return 0
}

func consumeUpdateError() string {
	dir, err := updateCacheDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(dir, "last-error.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	os.Remove(path)
	message := strings.TrimSpace(string(data))
	if len(message) > 256 {
		message = message[:256]
	}
	return message
}

func installUpdate(args []string) error {
	if len(args) != 4 {
		return fmt.Errorf("invalid update helper arguments")
	}
	pid, err := strconv.ParseUint(args[0], 10, 32)
	if err != nil || pid == 0 {
		return fmt.Errorf("invalid parent PID")
	}
	installer, digest, executable := args[1], args[2], args[3]
	if err := waitForExit(uint32(pid)); err != nil {
		return err
	}
	if err := verifyUpdateFile(installer, digest); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, installer, "/S")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("silent installer failed: %w", err)
	}
	os.Remove(installer)
	if dir, err := updateCacheDir(); err == nil {
		os.Remove(filepath.Join(dir, "last-error.txt"))
	}
	cmd = exec.Command(executable)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("restarting Watch Party: %w", err)
	}
	return cmd.Process.Release()
}

func waitForExit(pid uint32) error {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil // Parent exited before the helper opened its handle.
	}
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	result, err := windows.WaitForSingleObject(handle, 2*60*1000)
	if err != nil || result != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("timed out waiting for Watch Party to close: %v", err)
	}
	return nil
}

func verifyUpdateFile(path, digest string) error {
	want, err := expectedDigest(digest)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, maxUpdateSize+1))
	if err != nil || n < 1 || n > maxUpdateSize || !bytes.Equal(want, hash.Sum(nil)) {
		return fmt.Errorf("installer changed after download")
	}
	return nil
}
