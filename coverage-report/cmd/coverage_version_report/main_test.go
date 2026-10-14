package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/example/open-go-runtime-coverage/coverage-hook"
)

func TestBuildReportSeparatesSourceRevisions(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init")
	git("config", "user.email", "coverage-test@example.invalid")
	git("config", "user.name", "Coverage Test")
	if err := os.WriteFile(filepath.Join(repo, "modified.go"), []byte("package sample\nconst Version = 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "removed.go"), []byte("package sample\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "old")
	oldSHA := git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "modified.go"), []byte("package sample\nconst Version = 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "removed.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "added.go"), []byte("package sample\n\nfunc Added() int { return 42 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-m", "final")
	finalSHA := git("rev-parse", "HEAD")

	snapshots := t.TempDir()
	writeCompleteSnapshot(t, snapshots, "old", oldSHA, "deploy-old", time.Unix(1, 0))
	writeCompleteSnapshot(t, snapshots, "final", finalSHA, "deploy-final", time.Unix(2, 0))
	r, err := buildReport(options{repoRoot: repo, snapshotRoot: snapshots, finalSHA: finalSHA})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Deployments) != 2 {
		t.Fatalf("deployments = %#v", r.Deployments)
	}
	if r.Deployments[0].Relation != "historical_source_changed" || r.Deployments[1].Relation != "final" {
		t.Fatalf("relations = %q, %q", r.Deployments[0].Relation, r.Deployments[1].Relation)
	}
	wantMeanings := map[string]bool{
		"modified_in_final":    false,
		"removed_from_final":   false,
		"added_after_snapshot": false,
	}
	for _, change := range r.Deployments[0].Changes {
		if _, ok := wantMeanings[change.Meaning]; ok {
			wantMeanings[change.Meaning] = true
		}
	}
	for meaning, found := range wantMeanings {
		if !found {
			t.Errorf("missing source classification %q in %#v", meaning, r.Deployments[0].Changes)
		}
	}
}

func writeCompleteSnapshot(t *testing.T, root, name, sha, deploymentID string, createdAt time.Time) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := coveragehook.SnapshotManifest{
		SchemaVersion: coveragehook.SnapshotManifestVersion,
		SnapshotID:    name,
		BuildSHA:      sha,
		DeploymentID:  deploymentID,
		PID:           123,
		CreatedAt:     createdAt,
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for file, contents := range map[string][]byte{
		"complete.json":    data,
		"covmeta.test":     []byte("meta"),
		"covcounters.test": []byte("counters"),
	} {
		if err := os.WriteFile(filepath.Join(dir, file), contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestParseNameStatus(t *testing.T) {
	got, err := parseNameStatus([]byte("M\x00app/main.go\x00D\x00old.go\x00A\x00new.go\x00R100\x00before.go\x00after.go\x00"))
	if err != nil {
		t.Fatal(err)
	}
	want := []sourceChange{
		{Status: "M", Meaning: "modified_in_final", NewPath: "app/main.go"},
		{Status: "D", Meaning: "removed_from_final", NewPath: "old.go"},
		{Status: "A", Meaning: "added_after_snapshot", NewPath: "new.go"},
		{Status: "R100", Meaning: "renamed_in_final", OldPath: "before.go", NewPath: "after.go"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changes = %#v, want %#v", got, want)
	}
}

func TestLoadManifestsRejectsLegacyAndIncompleteSnapshots(t *testing.T) {
	root := t.TempDir()
	writeSnapshot := func(name string, manifest interface{}, withData bool) {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "complete.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if withData {
			if err := os.WriteFile(filepath.Join(dir, "covmeta.test"), []byte("meta"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "covcounters.test"), []byte("counters"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}

	writeSnapshot("good", coveragehook.SnapshotManifest{
		SchemaVersion: coveragehook.SnapshotManifestVersion,
		SnapshotID:    "good",
		BuildSHA:      strings.Repeat("a", 40),
		DeploymentID:  "jenkins-1",
		PID:           123,
		CreatedAt:     time.Now(),
	}, true)
	writeSnapshot("legacy", map[string]string{"snapshot_id": "legacy"}, true)
	writeSnapshot("incomplete", coveragehook.SnapshotManifest{
		SchemaVersion: coveragehook.SnapshotManifestVersion,
		SnapshotID:    "incomplete",
		BuildSHA:      strings.Repeat("b", 40),
		DeploymentID:  "jenkins-2",
	}, false)

	manifests, rejected, err := loadManifests(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || manifests[0].SnapshotID != "good" {
		t.Fatalf("accepted manifests = %#v", manifests)
	}
	if len(rejected) != 2 {
		t.Fatalf("rejected = %#v", rejected)
	}
}
