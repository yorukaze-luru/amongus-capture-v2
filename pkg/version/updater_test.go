package version

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestIsNewerVersion(t *testing.T) {
	tests := []struct {
		remote   string
		current  string
		expected bool
	}{
		{"v2.2.9", "v2.2.8", true},
		{"v2.3.0", "v2.2.8", true},
		{"v3.0.0", "v2.2.8", true},
		{"v2.2.8", "v2.2.8", false},
		{"v2.2.7", "v2.2.8", false},
		{"2.2.9", "v2.2.8", true},
		{"v2.2.8.1", "v2.2.8", true},
		{"", "v2.2.8", false},
		{"v2.2.8", "", false},
	}

	for _, tc := range tests {
		actual := IsNewerVersion(tc.remote, tc.current)
		if actual != tc.expected {
			t.Errorf("IsNewerVersion(%q, %q) = %v; want %v", tc.remote, tc.current, actual, tc.expected)
		}
	}
}

func TestFetchLatestRelease_And_ApplyBinaryUpdate(t *testing.T) {
	// Create a fake 1.1MB Windows PE binary ("MZ" header + padding)
	fakeNewBinary := make([]byte, 1100*1024)
	fakeNewBinary[0] = 'M'
	fakeNewBinary[1] = 'Z'
	copy(fakeNewBinary[2:], []byte("NEW_VERSION_PAYLOAD_v99.0.0"))

	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/test/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		rel := githubRelease{
			TagName: "v99.0.0",
			HTMLURL: serverURL + "/releases/tag/v99.0.0",
			Assets: []githubAsset{
				{
					Name:               "AmongUsCapture-v2.exe",
					BrowserDownloadURL: serverURL + "/download/AmongUsCapture-v2.exe",
					Size:               int64(len(fakeNewBinary)),
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rel)
	})
	mux.HandleFunc("/download/AmongUsCapture-v2.exe", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(fakeNewBinary)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()
	serverURL = ts.URL

	// 1. Test FetchLatestRelease
	info, err := FetchLatestRelease(ts.URL + "/repos/test/releases/latest")
	if err != nil {
		t.Fatalf("FetchLatestRelease failed: %v", err)
	}
	if !info.HasUpdate {
		t.Fatalf("expected HasUpdate=true, got false")
	}
	if info.LatestVersion != "v99.0.0" {
		t.Fatalf("expected LatestVersion='v99.0.0', got %q", info.LatestVersion)
	}
	if info.DownloadURL != ts.URL+"/download/AmongUsCapture-v2.exe" {
		t.Fatalf("unexpected DownloadURL: %q", info.DownloadURL)
	}

	// 2. Test ApplyBinaryUpdate on a temporary dummy executable
	tmpDir := t.TempDir()
	dummyExe := filepath.Join(tmpDir, "AmongUsCapture-v2.exe")
	oldContent := []byte("MZ_OLD_VERSION_BINARY")
	if err := os.WriteFile(dummyExe, oldContent, 0755); err != nil {
		t.Fatalf("failed to write dummy exe: %v", err)
	}

	if err := ApplyBinaryUpdate(dummyExe, info.DownloadURL); err != nil {
		t.Fatalf("ApplyBinaryUpdate failed: %v", err)
	}

	// Verify that dummyExe now contains fakeNewBinary
	updatedBytes, err := os.ReadFile(dummyExe)
	if err != nil {
		t.Fatalf("failed to read updated exe: %v", err)
	}
	if !bytes.Equal(updatedBytes, fakeNewBinary) {
		t.Fatalf("updated binary content does not match downloaded binary")
	}

	// Verify that .old backup was created with oldContent
	oldBackupBytes, err := os.ReadFile(dummyExe + ".old")
	if err != nil {
		t.Fatalf("expected .old backup file to exist: %v", err)
	}
	if !bytes.Equal(oldBackupBytes, oldContent) {
		t.Fatalf(".old backup content does not match original binary")
	}
}
