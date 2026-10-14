// Command coverage_version_report inventories runtime coverage snapshots by
// source revision and reports how each historical revision differs from the
// final revision. It deliberately does not merge coverage from different
// revisions.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/example/open-go-runtime-coverage/coverage-hook"
)

const reportSchemaVersion = 1

type options struct {
	repoRoot     string
	snapshotRoot string
	finalSHA     string
	output       string
}

type report struct {
	SchemaVersion int                `json:"schema_version"`
	GeneratedAt   time.Time          `json:"generated_at"`
	FinalSHA      string             `json:"final_sha"`
	Deployments   []deploymentReport `json:"deployments"`
	Rejected      []rejectedSnapshot `json:"rejected_snapshots,omitempty"`
}

type deploymentReport struct {
	BuildSHA     string                          `json:"build_sha"`
	DeploymentID string                          `json:"deployment_id"`
	Relation     string                          `json:"relation_to_final"`
	Snapshots    []coveragehook.SnapshotManifest `json:"snapshots"`
	Changes      []sourceChange                  `json:"source_changes_to_final,omitempty"`
}

type rejectedSnapshot struct {
	ManifestPath string `json:"manifest_path"`
	Reason       string `json:"reason"`
}

type sourceChange struct {
	Status  string `json:"status"`
	Meaning string `json:"meaning"`
	OldPath string `json:"old_path,omitempty"`
	NewPath string `json:"new_path"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("coverage_version_report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var opts options
	fs.StringVar(&opts.repoRoot, "repo", ".", "Git repository containing every deployed revision")
	fs.StringVar(&opts.snapshotRoot, "snapshots", "", "root directory containing versioned coverage snapshots")
	fs.StringVar(&opts.finalSHA, "final-sha", "", "final deployed Git commit used as the report authority")
	fs.StringVar(&opts.output, "out", "", "write JSON report to this path instead of stdout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if opts.snapshotRoot == "" || opts.finalSHA == "" {
		fmt.Fprintln(stderr, "-snapshots and -final-sha are required")
		return 2
	}

	r, err := buildReport(opts)
	if err != nil {
		fmt.Fprintln(stderr, "coverage version report:", err)
		return 1
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, "coverage version report:", err)
		return 1
	}
	data = append(data, '\n')
	if opts.output == "" {
		_, err = stdout.Write(data)
	} else {
		err = writeFileAtomically(opts.output, data)
	}
	if err != nil {
		fmt.Fprintln(stderr, "coverage version report:", err)
		return 1
	}
	if len(r.Rejected) != 0 {
		fmt.Fprintf(stderr, "coverage version report: rejected %d unattributed or incomplete snapshot(s)\n", len(r.Rejected))
		return 1
	}
	return 0
}

func buildReport(opts options) (report, error) {
	finalSHA, err := resolveCommit(opts.repoRoot, opts.finalSHA)
	if err != nil {
		return report{}, fmt.Errorf("resolve final SHA: %w", err)
	}
	manifests, rejected, err := loadManifests(opts.snapshotRoot)
	if err != nil {
		return report{}, err
	}

	type deploymentKey struct{ sha, id string }
	groups := make(map[deploymentKey][]coveragehook.SnapshotManifest)
	for _, manifest := range manifests {
		key := deploymentKey{sha: manifest.BuildSHA, id: manifest.DeploymentID}
		groups[key] = append(groups[key], manifest)
	}

	result := report{
		SchemaVersion: reportSchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		FinalSHA:      finalSHA,
		Rejected:      rejected,
	}
	for key, snapshots := range groups {
		sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].CreatedAt.Before(snapshots[j].CreatedAt) })
		resolved, resolveErr := resolveCommit(opts.repoRoot, key.sha)
		if resolveErr != nil {
			result.Rejected = append(result.Rejected, rejectedSnapshot{
				Reason: fmt.Sprintf("deployment %s references unavailable build SHA %s", key.id, key.sha),
			})
			continue
		}
		entry := deploymentReport{
			BuildSHA:     resolved,
			DeploymentID: key.id,
			Snapshots:    snapshots,
		}
		if resolved == finalSHA {
			entry.Relation = "final"
		} else {
			entry.Relation = "historical_source_changed"
			entry.Changes, err = gitChanges(opts.repoRoot, resolved, finalSHA)
			if err != nil {
				return report{}, err
			}
			if len(entry.Changes) == 0 {
				entry.Relation = "historical_source_equivalent"
			}
		}
		result.Deployments = append(result.Deployments, entry)
	}
	sort.Slice(result.Deployments, func(i, j int) bool {
		if result.Deployments[i].Snapshots[0].CreatedAt.Equal(result.Deployments[j].Snapshots[0].CreatedAt) {
			return result.Deployments[i].DeploymentID < result.Deployments[j].DeploymentID
		}
		return result.Deployments[i].Snapshots[0].CreatedAt.Before(result.Deployments[j].Snapshots[0].CreatedAt)
	})
	sort.Slice(result.Rejected, func(i, j int) bool { return result.Rejected[i].ManifestPath < result.Rejected[j].ManifestPath })
	return result, nil
}

func loadManifests(root string) ([]coveragehook.SnapshotManifest, []rejectedSnapshot, error) {
	var manifests []coveragehook.SnapshotManifest
	var rejected []rejectedSnapshot
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || info.Name() != "complete.json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var manifest coveragehook.SnapshotManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			rejected = append(rejected, rejectedSnapshot{ManifestPath: path, Reason: "invalid JSON manifest"})
			return nil
		}
		if err := manifest.Validate(); err != nil {
			rejected = append(rejected, rejectedSnapshot{ManifestPath: path, Reason: "manifest has no trustworthy code identity: " + err.Error()})
			return nil
		}
		dir := filepath.Dir(path)
		meta, _ := filepath.Glob(filepath.Join(dir, "covmeta.*"))
		counters, _ := filepath.Glob(filepath.Join(dir, "covcounters.*"))
		if len(meta) == 0 || len(counters) == 0 {
			rejected = append(rejected, rejectedSnapshot{ManifestPath: path, Reason: "coverage data is incomplete"})
			return nil
		}
		manifests = append(manifests, manifest)
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("scan snapshots: %w", err)
	}
	if len(manifests) == 0 && len(rejected) == 0 {
		return nil, nil, errors.New("no completed snapshots found")
	}
	return manifests, rejected, nil
}

func resolveCommit(repoRoot, revision string) (string, error) {
	cmd := exec.Command("git", "-C", repoRoot, "rev-parse", "--verify", revision+"^{commit}")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("Git commit %q is unavailable", revision)
	}
	return strings.TrimSpace(string(out)), nil
}

func gitChanges(repoRoot, oldSHA, finalSHA string) ([]sourceChange, error) {
	cmd := exec.Command("git", "-C", repoRoot, "diff", "--name-status", "-z", "--find-renames", oldSHA, finalSHA)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("compare %s to %s: %w", oldSHA, finalSHA, err)
	}
	return parseNameStatus(out)
}

func parseNameStatus(data []byte) ([]sourceChange, error) {
	if len(data) == 0 {
		return nil, nil
	}
	fields := strings.Split(string(data), "\x00")
	if fields[len(fields)-1] == "" {
		fields = fields[:len(fields)-1]
	}
	var changes []sourceChange
	for i := 0; i < len(fields); {
		status := fields[i]
		i++
		if i >= len(fields) {
			return nil, errors.New("truncated Git name-status output")
		}
		change := sourceChange{Status: status, Meaning: changeMeaning(status)}
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			if i+1 >= len(fields) {
				return nil, errors.New("truncated Git rename/copy output")
			}
			change.OldPath, change.NewPath = fields[i], fields[i+1]
			i += 2
		} else {
			change.NewPath = fields[i]
			i++
		}
		changes = append(changes, change)
	}
	return changes, nil
}

func changeMeaning(status string) string {
	if status == "" {
		return "unknown"
	}
	switch status[0] {
	case 'A':
		return "added_after_snapshot"
	case 'D':
		return "removed_from_final"
	case 'M':
		return "modified_in_final"
	case 'R':
		return "renamed_in_final"
	case 'C':
		return "copied_in_final"
	case 'T':
		return "type_changed_in_final"
	default:
		return "changed_in_final"
	}
}

func writeFileAtomically(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
