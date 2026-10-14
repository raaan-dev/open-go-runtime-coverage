//go:build coverage_snapshot
// +build coverage_snapshot

package coveragehook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoverageBuildCanRemainDisabled(t *testing.T) {
	stop, err := Start(Config{})
	if err != nil {
		t.Fatalf("disabled coverage hook should not require coverage settings: %v", err)
	}
	if err := stop(context.Background()); err != nil {
		t.Fatalf("stop disabled hook: %v", err)
	}
}

func TestSnapshotHandler(t *testing.T) {
	root := t.TempDir()
	h := &snapshotHandler{
		root:         root,
		token:        strings.Repeat("x", 32),
		buildSHA:     strings.Repeat("a", 40),
		deploymentID: "jenkins-123",
		writeMeta: func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "covmeta.test"), []byte("meta"), 0600)
		},
		writeCounters: func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "covcounters.test"), []byte("counters"), 0600)
		},
	}

	request := func(id, token string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, snapshotPath, nil)
		r.Header.Set("X-Snapshot-ID", id)
		r.Header.Set("Authorization", "Bearer "+token)
		return r
	}
	for _, tc := range []struct {
		name  string
		id    string
		token string
		want  int
	}{
		{"unauthorized", "run-1", "wrong", http.StatusUnauthorized},
		{"invalid id", "../escape", h.token, http.StatusBadRequest},
		{"created", "run-1", h.token, http.StatusCreated},
		{"duplicate", "run-1", h.token, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request(tc.id, tc.token))
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
	for _, file := range []string{"covmeta.test", "covcounters.test", "complete.json"} {
		if _, err := os.Stat(filepath.Join(root, "run-1", file)); err != nil {
			t.Fatalf("missing %s: %v", file, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "run-1", "complete.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest SnapshotManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != SnapshotManifestVersion || manifest.SnapshotID != "run-1" {
		t.Fatalf("unexpected manifest identity: %+v", manifest)
	}
	if manifest.BuildSHA != h.buildSHA || manifest.DeploymentID != h.deploymentID {
		t.Fatalf("manifest lost code identity: %+v", manifest)
	}
}
