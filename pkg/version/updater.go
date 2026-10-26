package version

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultGitHubLatestReleaseURL points to the GitHub API endpoint for the latest release
var DefaultGitHubLatestReleaseURL = "https://api.github.com/repos/yorukaze-luru/amongus-capture-v2/releases/latest"

// UpdateInfo holds the result of the startup release check
type UpdateInfo struct {
	HasUpdate     bool   `json:"has_update"`
	LatestVersion string `json:"latest_version"`
	ReleaseURL    string `json:"release_url"`
	DownloadURL   string `json:"download_url,omitempty"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

type githubRelease struct {
	TagName string        `json:"tag_name"`
	HTMLURL string        `json:"html_url"`
	Assets  []githubAsset `json:"assets"`
}

var (
	cachedUpdate UpdateInfo
	updateMu     sync.RWMutex
	checkOnce    sync.Once
)

// CanonicalExecutablePath returns the resolved path to the main executable,
// stripping any ".old" suffix that Windows may report after an in-place rename.
func CanonicalExecutablePath() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}
	for strings.HasSuffix(strings.ToLower(exePath), ".old") {
		exePath = exePath[:len(exePath)-4]
	}
	return exePath, nil
}

// CleanupOldBinary removes any leftover .old or .new files from a previous self-update
func CleanupOldBinary() {
	SyncInstalledVersionRegistry()

	exePath, err := CanonicalExecutablePath()
	if err != nil {
		return
	}

	oldPath := exePath + ".old"
	newPath := exePath + ".new"

	_ = os.Remove(newPath)
	if err := os.Remove(oldPath); err != nil && !os.IsNotExist(err) {
		// The previous process might still be releasing its file handle; retry in background
		go func(p string) {
			for _, delay := range []time.Duration{400 * time.Millisecond, 1000 * time.Millisecond, 2500 * time.Millisecond} {
				time.Sleep(delay)
				if err := os.Remove(p); err == nil || os.IsNotExist(err) {
					return
				}
			}
		}(oldPath)
	}
}

// FetchLatestRelease queries GitHub API synchronously and updates the cached UpdateInfo
func FetchLatestRelease(apiURL string) (UpdateInfo, error) {
	if apiURL == "" {
		apiURL = DefaultGitHubLatestReleaseURL
	}

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return UpdateInfo{}, err
	}
	req.Header.Set("User-Agent", "AmongUsCapture-v2/"+CurrentVersion)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return UpdateInfo{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return UpdateInfo{}, fmt.Errorf("github api returned status %d", resp.StatusCode)
	}

	var ghRelease githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&ghRelease); err != nil {
		return UpdateInfo{}, err
	}

	tag := strings.TrimSpace(ghRelease.TagName)
	hasUpdate := IsNewerVersion(tag, CurrentVersion)

	url := ghRelease.HTMLURL
	if url == "" {
		url = "https://github.com/yorukaze-luru/amongus-capture-v2/releases/latest"
	}

	downloadURL := ""
	for _, asset := range ghRelease.Assets {
		if strings.EqualFold(asset.Name, "AmongUsCapture-v2.exe") {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		for _, asset := range ghRelease.Assets {
			lowerName := strings.ToLower(asset.Name)
			if strings.HasSuffix(lowerName, ".exe") &&
				!strings.Contains(lowerName, "setup") &&
				!strings.Contains(lowerName, "install") {
				downloadURL = asset.BrowserDownloadURL
				break
			}
		}
	}

	info := UpdateInfo{
		HasUpdate:     hasUpdate,
		LatestVersion: tag,
		ReleaseURL:    url,
		DownloadURL:   downloadURL,
	}

	updateMu.Lock()
	cachedUpdate = info
	updateMu.Unlock()

	return info, nil
}

// ApplyBinaryUpdate downloads the new executable from downloadURL and replaces targetExePath safely
func ApplyBinaryUpdate(targetExePath, downloadURL string) error {
	if targetExePath == "" || downloadURL == "" {
		return fmt.Errorf("targetExePath or downloadURL is empty")
	}

	client := &http.Client{Timeout: 90 * time.Second}
	req, err := http.NewRequest(http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "AmongUsCapture-v2/"+CurrentVersion)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download update: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	newPath := targetExePath + ".new"
	oldPath := targetExePath + ".old"

	_ = os.Remove(newPath)
	outFile, err := os.OpenFile(newPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	written, copyErr := io.Copy(outFile, resp.Body)
	closeErr := outFile.Close()
	if copyErr != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("failed to write update binary: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("failed to close update binary: %w", closeErr)
	}

	// Sanity check: minimum size (1MB) and valid Windows PE ("MZ") header
	if written < 1024*1024 {
		_ = os.Remove(newPath)
		return fmt.Errorf("downloaded file too small (%d bytes)", written)
	}
	if err := verifyPEHeader(newPath); err != nil {
		_ = os.Remove(newPath)
		return err
	}

	// Remove any existing .old file
	_ = os.Remove(oldPath)

	// Rename current running executable to .old
	if err := os.Rename(targetExePath, oldPath); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("failed to rename current binary to .old: %w", err)
	}

	// Move new binary into place
	if err := os.Rename(newPath, targetExePath); err != nil {
		// Rollback!
		_ = os.Rename(oldPath, targetExePath)
		_ = os.Remove(newPath)
		return fmt.Errorf("failed to move new binary into place: %w", err)
	}

	return nil
}

func verifyPEHeader(filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	var magic [2]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return fmt.Errorf("failed to read PE header: %w", err)
	}
	if magic[0] != 'M' || magic[1] != 'Z' {
		return fmt.Errorf("invalid PE header: expected MZ, got %q", string(magic[:]))
	}
	return nil
}

// CheckAndApplyStartupUpdate checks for a newer release on GitHub at startup,
// and if found, downloads and replaces the current executable in-place.
func CheckAndApplyStartupUpdate() (bool, string, error) {
	CleanupOldBinary()

	// Prevent infinite update loops if just restarted from an update
	if os.Getenv("AUCAPTURE_UPDATED_FROM") != "" {
		return false, "", nil
	}

	info, err := FetchLatestRelease(DefaultGitHubLatestReleaseURL)
	if err != nil {
		log.Printf("[AutoUpdate] Release check skipped: %v", err)
		return false, "", err
	}

	if !info.HasUpdate || info.DownloadURL == "" {
		return false, info.LatestVersion, nil
	}

	exePath, err := CanonicalExecutablePath()
	if err != nil {
		return false, info.LatestVersion, err
	}

	log.Printf("[AutoUpdate] New version detected (%s -> %s). Downloading %s...", CurrentVersion, info.LatestVersion, info.DownloadURL)
	if err := ApplyBinaryUpdate(exePath, info.DownloadURL); err != nil {
		log.Printf("[AutoUpdate] Failed to apply update: %v", err)
		return false, info.LatestVersion, err
	}

	log.Printf("[AutoUpdate] Successfully updated binary to %s!", info.LatestVersion)
	return true, info.LatestVersion, nil
}

// CheckStartupUpdate launches a background query to GitHub API to check for updates
func CheckStartupUpdate() {
	checkOnce.Do(func() {
		go func() {
			_, _ = FetchLatestRelease(DefaultGitHubLatestReleaseURL)
		}()
	})
}

// GetUpdateInfo returns the current cached update status
func GetUpdateInfo() UpdateInfo {
	updateMu.RLock()
	defer updateMu.RUnlock()
	return cachedUpdate
}

// SetUpdateInfoForTest overrides the update status (useful for UI testing and mock)
func SetUpdateInfoForTest(info UpdateInfo) {
	updateMu.Lock()
	cachedUpdate = info
	updateMu.Unlock()
}

// IsNewerVersion returns true if remote version is strictly newer than current version
func IsNewerVersion(remote, current string) bool {
	remote = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(remote, "v"), "V"))
	current = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(current, "v"), "V"))

	if remote == "" || current == "" {
		return false
	}

	rParts := strings.Split(remote, ".")
	cParts := strings.Split(current, ".")

	maxLen := len(rParts)
	if len(cParts) > maxLen {
		maxLen = len(cParts)
	}

	for i := 0; i < maxLen; i++ {
		var rNum, cNum int
		if i < len(rParts) {
			rNum, _ = strconv.Atoi(rParts[i])
		}
		if i < len(cParts) {
			cNum, _ = strconv.Atoi(cParts[i])
		}

		if rNum > cNum {
			return true
		}
		if rNum < cNum {
			return false
		}
	}

	return false
}
