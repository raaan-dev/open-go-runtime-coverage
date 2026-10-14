//go:build coverage_snapshot
// +build coverage_snapshot

package coveragehook

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/coverage"
	"sync"
	"time"
)

const snapshotPath = "/internal/coverage/snapshot"

type snapshotHandler struct {
	root          string
	token         string
	buildSHA      string
	deploymentID  string
	writeMeta     func(string) error
	writeCounters func(string) error
	mu            sync.Mutex
}

// Start opens a loopback-only control endpoint. Snapshot failures do not stop
// the business service; startup configuration failures are returned to main.
func Start(cfg Config) (StopFunc, error) {
	if !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}
	if os.Getenv("GOCOVERDIR") == "" {
		return nil, errors.New("GOCOVERDIR is required in coverage builds")
	}
	if !filepath.IsAbs(cfg.SnapshotDir) {
		return nil, errors.New("SnapshotDir must be an absolute path")
	}
	if len(cfg.Token) < 32 {
		return nil, errors.New("Token must contain at least 32 bytes")
	}
	if err := validateBuildIdentity(cfg.BuildSHA, cfg.DeploymentID); err != nil {
		return nil, err
	}
	host, port, err := net.SplitHostPort(cfg.ListenAddress)
	if err != nil || port == "" || (host != "127.0.0.1" && host != "::1") {
		return nil, errors.New("ListenAddress must use 127.0.0.1 or ::1 and an explicit port")
	}
	if err := coverage.WriteMeta(io.Discard); err != nil {
		return nil, fmt.Errorf("coverage instrumentation unavailable: %w", err)
	}
	if err := os.MkdirAll(cfg.SnapshotDir, 0700); err != nil {
		return nil, fmt.Errorf("create snapshot root: %w", err)
	}
	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		return nil, fmt.Errorf("listen on coverage control address: %w", err)
	}

	h := &snapshotHandler{
		root:          cfg.SnapshotDir,
		token:         cfg.Token,
		buildSHA:      cfg.BuildSHA,
		deploymentID:  cfg.DeploymentID,
		writeMeta:     coverage.WriteMetaDir,
		writeCounters: coverage.WriteCountersDir,
	}
	mux := http.NewServeMux()
	mux.Handle(snapshotPath, h)
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("coverage snapshot listener stopped: %v", err)
		}
	}()
	return server.Shutdown, nil
}

func (h *snapshotHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	want := "Bearer " + h.token
	got := r.Header.Get("Authorization")
	if len(got) != len(want) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := r.Header.Get("X-Snapshot-ID")
	if !snapshotIDPattern.MatchString(id) {
		http.Error(w, "invalid X-Snapshot-ID", http.StatusBadRequest)
		return
	}

	// Do not overlap writes from deployment-boundary and QA-finish requests.
	h.mu.Lock()
	defer h.mu.Unlock()

	dir := filepath.Join(h.root, id)
	if err := os.Mkdir(dir, 0700); err != nil {
		if os.IsExist(err) {
			http.Error(w, "snapshot ID already exists", http.StatusConflict)
		} else {
			http.Error(w, "cannot create snapshot directory", http.StatusInternalServerError)
		}
		return
	}
	if err := h.writeMeta(dir); err != nil {
		log.Printf("coverage snapshot %s meta write failed: %v", id, err)
		http.Error(w, "cannot write coverage metadata", http.StatusInternalServerError)
		return
	}
	if err := h.writeCounters(dir); err != nil {
		log.Printf("coverage snapshot %s counters write failed: %v", id, err)
		http.Error(w, "cannot write coverage counters", http.StatusInternalServerError)
		return
	}

	manifest := SnapshotManifest{
		SchemaVersion: SnapshotManifestVersion,
		SnapshotID:    id,
		BuildSHA:      h.buildSHA,
		DeploymentID:  h.deploymentID,
		PID:           os.Getpid(),
		CreatedAt:     time.Now().UTC(),
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		http.Error(w, "cannot encode snapshot manifest", http.StatusInternalServerError)
		return
	}
	tmp := filepath.Join(dir, "complete.json.tmp")
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		http.Error(w, "cannot write snapshot manifest", http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmp, filepath.Join(dir, "complete.json")); err != nil {
		http.Error(w, "cannot complete snapshot manifest", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(data)
}
