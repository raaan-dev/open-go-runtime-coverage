package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/example/open-go-runtime-coverage/coverage-hook"
)

func record(id, sha, deployment string, pid int, second int64) snapshotRecord {
	return snapshotRecord{
		Manifest: coveragehook.SnapshotManifest{
			SchemaVersion: coveragehook.SnapshotManifestVersion,
			SnapshotID:    id,
			BuildSHA:      sha,
			DeploymentID:  deployment,
			PID:           pid,
			CreatedAt:     time.Unix(second, 0),
		},
		Directory: filepath.Join("snapshots", id),
	}
}

func TestPlanSegmentsAcrossRedeploy(t *testing.T) {
	oldSHA := strings.Repeat("a", 40)
	newSHA := strings.Repeat("b", 40)
	records := []snapshotRecord{
		record("deploy_14_start", oldSHA, "jenkins-14", 10, 1),
		record("requirement_a_start", oldSHA, "jenkins-14", 10, 2),
		record("deploy_15_prestop", oldSHA, "jenkins-14", 10, 3),
		record("deploy_15_start", newSHA, "jenkins-15", 20, 4),
		record("requirement_a_finish", newSHA, "jenkins-15", 20, 5),
	}
	plans, finalSHA, err := planSegments(records, "requirement_a_start", "requirement_a_finish")
	if err != nil {
		t.Fatal(err)
	}
	if finalSHA != newSHA || len(plans) != 2 {
		t.Fatalf("final=%s plans=%#v", finalSHA, plans)
	}
	want := [][3]string{
		{"jenkins-14", "requirement_a_start", "deploy_15_prestop"},
		{"jenkins-15", "deploy_15_start", "requirement_a_finish"},
	}
	for i, expected := range want {
		got := [3]string{plans[i].DeploymentID, plans[i].Start.Manifest.SnapshotID, plans[i].Finish.Manifest.SnapshotID}
		if got != expected {
			t.Fatalf("segment %d = %#v, want %#v", i, got, expected)
		}
	}
}

func TestPlanSegmentsWithoutRedeploy(t *testing.T) {
	sha := strings.Repeat("a", 40)
	records := []snapshotRecord{
		record("start", sha, "jenkins-1", 10, 1),
		record("finish", sha, "jenkins-1", 10, 2),
	}
	plans, _, err := planSegments(records, "start", "finish")
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].Start.Manifest.SnapshotID != "start" || plans[0].Finish.Manifest.SnapshotID != "finish" {
		t.Fatalf("plans = %#v", plans)
	}
}

func TestPlanSegmentsRequiresDeploymentBoundaries(t *testing.T) {
	oldSHA := strings.Repeat("a", 40)
	newSHA := strings.Repeat("b", 40)
	records := []snapshotRecord{
		record("start", oldSHA, "jenkins-1", 10, 1),
		record("unrelated", oldSHA, "jenkins-1", 10, 2),
		record("deploy_2_start", newSHA, "jenkins-2", 20, 3),
		record("finish", newSHA, "jenkins-2", 20, 4),
	}
	if _, _, err := planSegments(records, "start", "finish"); err == nil || !strings.Contains(err.Error(), "_prestop") {
		t.Fatalf("expected missing prestop error, got %v", err)
	}
}

func TestSubtractProfiles(t *testing.T) {
	baseline := coverageProfile{Mode: "mode: atomic", Blocks: []profileBlock{
		{Key: "app/a.go:1.1,2.2", Statements: 2, Count: 5},
		{Key: "app/a.go:3.1,4.2", Statements: 1, Count: 0},
	}}
	finish := coverageProfile{Mode: "mode: atomic", Blocks: []profileBlock{
		{Key: "app/a.go:1.1,2.2", Statements: 2, Count: 7},
		{Key: "app/a.go:3.1,4.2", Statements: 1, Count: 1},
	}}
	got, err := subtractProfiles(baseline, finish)
	if err != nil {
		t.Fatal(err)
	}
	want := []profileBlock{
		{Key: "app/a.go:1.1,2.2", Statements: 2, Count: 2},
		{Key: "app/a.go:3.1,4.2", Statements: 1, Count: 1},
	}
	if !reflect.DeepEqual(got.Blocks, want) {
		t.Fatalf("blocks = %#v, want %#v", got.Blocks, want)
	}
}

func TestSubtractProfilesRejectsCounterDecrease(t *testing.T) {
	baseline := coverageProfile{Mode: "mode: atomic", Blocks: []profileBlock{{Key: "a.go:1.1,2.2", Statements: 1, Count: 2}}}
	finish := coverageProfile{Mode: "mode: atomic", Blocks: []profileBlock{{Key: "a.go:1.1,2.2", Statements: 1, Count: 1}}}
	if _, err := subtractProfiles(baseline, finish); err == nil {
		t.Fatal("expected counter decrease error")
	}
}

func TestMatchRepoPath(t *testing.T) {
	paths := []string{"app/main.go", "vendor/example/app/main.go"}
	got, err := matchRepoPath("/workspace/example-service/app/main.go", paths)
	if err != nil {
		t.Fatal(err)
	}
	if got != "app/main.go" {
		t.Fatalf("path = %q", got)
	}
}

func TestLoadSnapshotsSkipsLegacyAndIncompleteData(t *testing.T) {
	root := t.TempDir()
	write := func(name string, value interface{}, complete bool) {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "complete.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if complete {
			if err := os.WriteFile(filepath.Join(dir, "covmeta.test"), []byte("meta"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "covcounters.test"), []byte("counter"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("legacy", map[string]string{"snapshot_id": "legacy"}, true)
	write("incomplete", record("incomplete", strings.Repeat("b", 40), "jenkins-2", 2, 2).Manifest, false)
	write("valid", record("valid", strings.Repeat("a", 40), "jenkins-1", 1, 1).Manifest, true)

	records, err := loadSnapshots(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Manifest.SnapshotID != "valid" {
		t.Fatalf("records = %#v", records)
	}
}
