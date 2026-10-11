package version

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// UpdateInfo holds the result of the startup release check
type UpdateInfo struct {
	HasUpdate     bool   `json:"has_update"`
	LatestVersion string `json:"latest_version"`
	ReleaseURL    string `json:"release_url"`
}

var (
	cachedUpdate UpdateInfo
	updateMu     sync.RWMutex
	checkOnce    sync.Once
)

// CheckStartupUpdate launches a background query to GitHub API to check for updates
func CheckStartupUpdate() {
	checkOnce.Do(func() {
		go func() {
			client := &http.Client{Timeout: 4 * time.Second}
			req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/yorukaze-luru/amongus-capture-v2/releases/latest", nil)
			if err != nil {
				return
			}
			req.Header.Set("User-Agent", "AmongUsCapture-v2/"+CurrentVersion)
			req.Header.Set("Accept", "application/vnd.github+json")

			resp, err := client.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return
			}

			var ghRelease struct {
				TagName string `json:"tag_name"`
				HTMLURL string `json:"html_url"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&ghRelease); err != nil {
				return
			}

			tag := strings.TrimSpace(ghRelease.TagName)
			hasUpdate := IsNewerVersion(tag, CurrentVersion)

			url := ghRelease.HTMLURL
			if url == "" {
				url = "https://github.com/yorukaze-luru/amongus-capture-v2/releases/latest"
			}

			updateMu.Lock()
			cachedUpdate = UpdateInfo{
				HasUpdate:     hasUpdate,
				LatestVersion: tag,
				ReleaseURL:    url,
			}
			updateMu.Unlock()
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
