package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	coveragehook "github.com/example/open-go-runtime-coverage/coverage-hook"
)

type server struct{}

// coverageBuildSHA is replaced at build time for coverage-enabled binaries.
var coverageBuildSHA = "unknown"

type response struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type order struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Amount int    `json:"amount"`
}

func main() {
	stopCoverage, err := coveragehook.Start(coveragehook.Config{
		Enabled:       os.Getenv("COVERAGE_SNAPSHOT_ENABLED") == "true",
		ListenAddress: os.Getenv("COVERAGE_SNAPSHOT_ADDR"),
		SnapshotDir:   os.Getenv("COVERAGE_SNAPSHOT_DIR"),
		Token:         os.Getenv("COVERAGE_SNAPSHOT_TOKEN"),
		BuildSHA:      coverageBuildSHA,
		DeploymentID:  os.Getenv("COVERAGE_DEPLOYMENT_ID"),
	})
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		_ = stopCoverage(context.Background())
	}()
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	s := &server{}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/hello", s.hello)
	mux.HandleFunc("/orders/", s.order)
	mux.HandleFunc("/feature", s.feature)

	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           logging(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("go-coverage-demo listening on %s", httpServer.Addr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, response{Code: 0, Message: "ok", Data: map[string]string{"status": "healthy"}})
}

func (s *server) hello(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = "world"
	}
	writeJSON(w, http.StatusOK, response{Code: 0, Message: "success", Data: map[string]string{"greeting": "hello, " + name}})
}

func (s *server) order(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/orders/")
	if id == "" || strings.Contains(id, "/") {
		writeJSON(w, http.StatusBadRequest, response{Code: 40001, Message: "invalid order id"})
		return
	}

	if id == "not-found" {
		writeJSON(w, http.StatusNotFound, response{Code: 40401, Message: "order not found"})
		return
	}

	status := "pending"
	if strings.HasPrefix(id, "paid-") {
		status = "paid"
	}
	writeJSON(w, http.StatusOK, response{
		Code:    0,
		Message: "success",
		Data:    order{ID: id, Status: status, Amount: 100},
	})
}

func (s *server) feature(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Query().Get("mode") {
	case "fast":
		writeJSON(w, http.StatusOK, response{Code: 0, Message: "fast path", Data: map[string]bool{"enabled": true}})
	case "safe":
		writeJSON(w, http.StatusOK, response{Code: 0, Message: "safe path", Data: map[string]bool{"enabled": false}})
	default:
		writeJSON(w, http.StatusBadRequest, response{Code: 40002, Message: "unsupported mode"})
	}
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("method=%s path=%s duration=%s", r.Method, r.URL.Path, time.Since(start))
	})
}

func writeJSON(w http.ResponseWriter, status int, body response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("write response: %v", err)
	}
}
