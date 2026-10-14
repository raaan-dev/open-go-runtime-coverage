// Command coverage_requirement_report builds a requirement-level runtime
// coverage report across one or more deployments. Coverage counters are only
// subtracted within the same process and source revision; cross-deployment
// results are presented as separate, source-correct segments.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/example/open-go-runtime-coverage/coverage-hook"
)

const reportSchemaVersion = 1

type options struct {
	repoRoot     string
	snapshotRoot string
	startID      string
	finishID     string
	outputDir    string
}

type snapshotRecord struct {
	Manifest     coveragehook.SnapshotManifest `json:"manifest"`
	Directory    string                        `json:"directory"`
	ManifestPath string                        `json:"manifest_path"`
}

type segmentPlan struct {
	BuildSHA     string
	DeploymentID string
	PID          int
	Start        snapshotRecord
	Finish       snapshotRecord
	Changes      []sourceChange
	Relation     string
	Directory    string
	Profile      string
	Functions    string
	HTML         string
	Covered      int
	Total        int
}

type segmentReport struct {
	Index        int                           `json:"index"`
	BuildSHA     string                        `json:"build_sha"`
	DeploymentID string                        `json:"deployment_id"`
	PID          int                           `json:"pid"`
	Start        coveragehook.SnapshotManifest `json:"start"`
	Finish       coveragehook.SnapshotManifest `json:"finish"`
	Relation     string                        `json:"relation_to_final"`
	Changes      []sourceChange                `json:"source_changes_to_final,omitempty"`
	Covered      int                           `json:"covered_statements"`
	Total        int                           `json:"total_statements"`
	Percent      float64                       `json:"coverage_percent"`
	Profile      string                        `json:"coverage_profile"`
	Functions    string                        `json:"functions_report"`
	HTML         string                        `json:"html_report"`
}

type report struct {
	SchemaVersion int             `json:"schema_version"`
	GeneratedAt   time.Time       `json:"generated_at"`
	StartID       string          `json:"start_id"`
	FinishID      string          `json:"finish_id"`
	FinalSHA      string          `json:"final_sha"`
	Segments      []segmentReport `json:"segments"`
}

type sourceChange struct {
	Status  string `json:"status"`
	Meaning string `json:"meaning"`
	OldPath string `json:"old_path,omitempty"`
	NewPath string `json:"new_path"`
}

type profileBlock struct {
	Key        string
	Statements int
	Count      int64
}

type coverageProfile struct {
	Mode   string
	Blocks []profileBlock
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("coverage_requirement_report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var opts options
	fs.StringVar(&opts.repoRoot, "repo", ".", "Git repository containing every deployed revision")
	fs.StringVar(&opts.snapshotRoot, "snapshots", "", "root containing completed snapshot directories")
	fs.StringVar(&opts.startID, "start-id", "", "requirement test-window start snapshot ID")
	fs.StringVar(&opts.finishID, "finish-id", "", "requirement test-window finish snapshot ID")
	fs.StringVar(&opts.outputDir, "out", "", "directory for the summary and per-deployment reports")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if opts.snapshotRoot == "" || opts.startID == "" || opts.finishID == "" || opts.outputDir == "" {
		fmt.Fprintln(stderr, "-snapshots, -start-id, -finish-id, and -out are required")
		return 2
	}
	result, err := buildReport(opts)
	if err != nil {
		fmt.Fprintln(stderr, "coverage requirement report:", err)
		return 1
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, "coverage requirement report:", err)
		return 1
	}
	data = append(data, '\n')
	if err := writeFileAtomically(filepath.Join(opts.outputDir, "report.json"), data, 0600); err != nil {
		fmt.Fprintln(stderr, "coverage requirement report:", err)
		return 1
	}
	if err := writeIndex(filepath.Join(opts.outputDir, "index.html"), result); err != nil {
		fmt.Fprintln(stderr, "coverage requirement report:", err)
		return 1
	}
	for _, segment := range result.Segments {
		fmt.Fprintf(stdout, "segment=%d deployment=%s sha=%s coverage=%.1f%% start=%s finish=%s\n",
			segment.Index, segment.DeploymentID, segment.BuildSHA, segment.Percent,
			segment.Start.SnapshotID, segment.Finish.SnapshotID)
	}
	fmt.Fprintf(stdout, "report=%s\n", filepath.Join(opts.outputDir, "index.html"))
	return 0
}

func buildReport(opts options) (report, error) {
	records, err := loadSnapshots(opts.snapshotRoot)
	if err != nil {
		return report{}, err
	}
	plans, finalSHA, err := planSegments(records, opts.startID, opts.finishID)
	if err != nil {
		return report{}, err
	}
	if _, err := resolveCommit(opts.repoRoot, finalSHA); err != nil {
		return report{}, err
	}
	if err := os.MkdirAll(opts.outputDir, 0755); err != nil {
		return report{}, fmt.Errorf("create output directory: %w", err)
	}

	result := report{
		SchemaVersion: reportSchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		StartID:       opts.startID,
		FinishID:      opts.finishID,
		FinalSHA:      finalSHA,
	}
	for i := range plans {
		plan := &plans[i]
		if _, err := resolveCommit(opts.repoRoot, plan.BuildSHA); err != nil {
			return report{}, fmt.Errorf("deployment %s: %w", plan.DeploymentID, err)
		}
		if plan.BuildSHA == finalSHA {
			plan.Relation = "final"
		} else {
			plan.Relation = "historical_source_changed"
			plan.Changes, err = gitChanges(opts.repoRoot, plan.BuildSHA, finalSHA)
			if err != nil {
				return report{}, err
			}
			if len(plan.Changes) == 0 {
				plan.Relation = "historical_source_equivalent"
			}
		}

		plan.Directory = filepath.Join(opts.outputDir, fmt.Sprintf("segment-%02d-%s", i+1, safeName(plan.DeploymentID)))
		if err := os.MkdirAll(plan.Directory, 0755); err != nil {
			return report{}, err
		}
		if err := buildSegmentArtifacts(opts.repoRoot, plan); err != nil {
			return report{}, fmt.Errorf("deployment %s: %w", plan.DeploymentID, err)
		}
		percent := 0.0
		if plan.Total != 0 {
			percent = float64(plan.Covered) * 100 / float64(plan.Total)
		}
		result.Segments = append(result.Segments, segmentReport{
			Index:        i + 1,
			BuildSHA:     plan.BuildSHA,
			DeploymentID: plan.DeploymentID,
			PID:          plan.PID,
			Start:        plan.Start.Manifest,
			Finish:       plan.Finish.Manifest,
			Relation:     plan.Relation,
			Changes:      plan.Changes,
			Covered:      plan.Covered,
			Total:        plan.Total,
			Percent:      percent,
			Profile:      relativePath(opts.outputDir, plan.Profile),
			Functions:    relativePath(opts.outputDir, plan.Functions),
			HTML:         relativePath(opts.outputDir, plan.HTML),
		})
	}
	return result, nil
}

func loadSnapshots(root string) ([]snapshotRecord, error) {
	var records []snapshotRecord
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
			// Old POC snapshots are deliberately unattributed and cannot be
			// used in a requirement window. Ignore them while scanning the
			// shared retention root; an explicitly requested invalid ID will
			// still fail later as "not found".
			return nil
		}
		if err := manifest.Validate(); err != nil {
			return nil
		}
		dir := filepath.Dir(path)
		meta, _ := filepath.Glob(filepath.Join(dir, "covmeta.*"))
		counters, _ := filepath.Glob(filepath.Join(dir, "covcounters.*"))
		if len(meta) == 0 || len(counters) == 0 {
			return nil
		}
		records = append(records, snapshotRecord{Manifest: manifest, Directory: dir, ManifestPath: path})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan snapshots: %w", err)
	}
	if len(records) == 0 {
		return nil, errors.New("no completed snapshots found")
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Manifest.CreatedAt.Before(records[j].Manifest.CreatedAt) })
	return records, nil
}

func planSegments(records []snapshotRecord, startID, finishID string) ([]segmentPlan, string, error) {
	start, err := uniqueSnapshot(records, startID)
	if err != nil {
		return nil, "", err
	}
	finish, err := uniqueSnapshot(records, finishID)
	if err != nil {
		return nil, "", err
	}
	if !start.Manifest.CreatedAt.Before(finish.Manifest.CreatedAt) {
		return nil, "", errors.New("start snapshot must be older than finish snapshot")
	}

	type deploymentKey struct {
		sha string
		id  string
		pid int
	}
	var order []deploymentKey
	groups := make(map[deploymentKey][]snapshotRecord)
	for _, record := range records {
		at := record.Manifest.CreatedAt
		if at.Before(start.Manifest.CreatedAt) || at.After(finish.Manifest.CreatedAt) {
			continue
		}
		key := deploymentKey{record.Manifest.BuildSHA, record.Manifest.DeploymentID, record.Manifest.PID}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], record)
	}
	if len(order) == 0 {
		return nil, "", errors.New("test window contains no snapshots")
	}
	if order[0].id != start.Manifest.DeploymentID || order[0].pid != start.Manifest.PID {
		return nil, "", errors.New("start snapshot is not the first deployment in the test window")
	}
	last := order[len(order)-1]
	if last.id != finish.Manifest.DeploymentID || last.pid != finish.Manifest.PID {
		return nil, "", errors.New("finish snapshot is not the last deployment in the test window")
	}

	var plans []segmentPlan
	for i, key := range order {
		items := groups[key]
		sort.Slice(items, func(a, b int) bool { return items[a].Manifest.CreatedAt.Before(items[b].Manifest.CreatedAt) })
		segmentStart := snapshotRecord{}
		segmentFinish := snapshotRecord{}
		if i == 0 {
			segmentStart = start
		} else {
			segmentStart, err = deploymentBoundary(items, "_start", true)
			if err != nil {
				return nil, "", fmt.Errorf("deployment %s: %w", key.id, err)
			}
		}
		if i == len(order)-1 {
			segmentFinish = finish
		} else {
			segmentFinish, err = deploymentBoundary(items, "_prestop", false)
			if err != nil {
				return nil, "", fmt.Errorf("deployment %s: %w", key.id, err)
			}
		}
		if !segmentStart.Manifest.CreatedAt.Before(segmentFinish.Manifest.CreatedAt) {
			return nil, "", fmt.Errorf("deployment %s has an invalid segment time range", key.id)
		}
		plans = append(plans, segmentPlan{
			BuildSHA:     key.sha,
			DeploymentID: key.id,
			PID:          key.pid,
			Start:        segmentStart,
			Finish:       segmentFinish,
		})
	}
	return plans, finish.Manifest.BuildSHA, nil
}

func uniqueSnapshot(records []snapshotRecord, id string) (snapshotRecord, error) {
	var matches []snapshotRecord
	for _, record := range records {
		if record.Manifest.SnapshotID == id {
			matches = append(matches, record)
		}
	}
	if len(matches) == 0 {
		return snapshotRecord{}, fmt.Errorf("snapshot %q was not found", id)
	}
	if len(matches) != 1 {
		return snapshotRecord{}, fmt.Errorf("snapshot %q is not unique", id)
	}
	return matches[0], nil
}

func deploymentBoundary(records []snapshotRecord, suffix string, first bool) (snapshotRecord, error) {
	var matches []snapshotRecord
	for _, record := range records {
		id := record.Manifest.SnapshotID
		if strings.HasPrefix(id, "deploy_") && strings.HasSuffix(id, suffix) {
			matches = append(matches, record)
		}
	}
	if len(matches) == 0 {
		return snapshotRecord{}, fmt.Errorf("missing deployment boundary snapshot %s", suffix)
	}
	if first {
		return matches[0], nil
	}
	return matches[len(matches)-1], nil
}

func buildSegmentArtifacts(repoRoot string, plan *segmentPlan) error {
	baselinePath := filepath.Join(plan.Directory, "baseline.out")
	finishPath := filepath.Join(plan.Directory, "finish.out")
	if err := covdataTextfmt(plan.Start.Directory, baselinePath); err != nil {
		return fmt.Errorf("format start snapshot: %w", err)
	}
	if err := covdataTextfmt(plan.Finish.Directory, finishPath); err != nil {
		return fmt.Errorf("format finish snapshot: %w", err)
	}
	baseline, err := readProfile(baselinePath)
	if err != nil {
		return err
	}
	finish, err := readProfile(finishPath)
	if err != nil {
		return err
	}
	delta, err := subtractProfiles(baseline, finish)
	if err != nil {
		return err
	}
	sourceRoot := filepath.Join(plan.Directory, "source")
	if err := materializeAndRewrite(repoRoot, plan.BuildSHA, sourceRoot, &delta); err != nil {
		return err
	}
	plan.Profile = filepath.Join(plan.Directory, "coverage.out")
	if err := writeProfile(plan.Profile, delta); err != nil {
		return err
	}
	for _, block := range delta.Blocks {
		plan.Total += block.Statements
		if block.Count > 0 {
			plan.Covered += block.Statements
		}
	}
	plan.Functions = filepath.Join(plan.Directory, "coverage-functions.txt")
	functions, err := commandOutput("go", "tool", "cover", "-func="+plan.Profile)
	if err != nil {
		return err
	}
	if err := writeFileAtomically(plan.Functions, functions, 0644); err != nil {
		return err
	}
	plan.HTML = filepath.Join(plan.Directory, "coverage.html")
	cmd := exec.Command("go", "tool", "cover", "-html="+plan.Profile, "-o="+plan.HTML)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("generate HTML coverage: %w: %s", err, output)
	}
	return styleCoverageHTML(plan.HTML)
}

// styleCoverageHTML keeps Go's source/coverage rendering and replaces its
// developer-oriented dark chrome with the same light GitLab-like visual
// language as the requirement report index.
func styleCoverageHTML(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	start := bytes.Index(b, []byte("<style>"))
	end := bytes.Index(b, []byte("</style>"))
	if start < 0 || end < start {
		return fmt.Errorf("coverage HTML has no style block")
	}
	css := `<style>
:root{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC",sans-serif;color:#172033;background:#f4f7fb}*{box-sizing:border-box}body{margin:0;background:#f4f7fb;color:#172033;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-weight:400}#topbar{position:sticky;top:0;z-index:2;min-height:58px;padding:10px 18px;background:#fff;border-bottom:1px solid #e5eaf1;box-shadow:0 2px 8px #17255412}#nav,#legend{float:none;margin:0;display:inline-flex;align-items:center;gap:12px}#nav{margin-right:20px}#files{max-width:62vw;padding:7px 10px;border:1px solid #d0d7e2;border-radius:6px;background:#fff;color:#172033;font:13px ui-monospace,SFMono-Regular,Menlo,monospace}.legend-title{color:#667085;font:12px -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}.cov0{color:#c2413b}.cov1{color:#667085}.cov2{color:#a16207}.cov3{color:#15803d}.cov4,.cov5,.cov6,.cov7,.cov8,.cov9,.cov10{color:#087f5b}#legend span{margin:0 3px;font:12px -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}#content{margin:18px auto;max-width:1400px;padding:0 18px;background:#fff;border:1px solid #e5eaf1;border-radius:8px;box-shadow:0 4px 14px #1725540b}pre{margin:0;padding:18px 0;overflow:auto;line-height:1.65;font:13px/1.65 ui-monospace,SFMono-Regular,Menlo,monospace;font-weight:400}.cov0,.cov1,.cov2,.cov3,.cov4,.cov5,.cov6,.cov7,.cov8,.cov9,.cov10{font-weight:500}@media(max-width:760px){#topbar{padding:8px 10px}#nav{display:block;margin:0 0 8px}#files{max-width:100%;width:100%}#content{margin:10px 6px;padding:0 8px}}
</style>`
	out := append(append([]byte{}, b[:start]...), []byte(css)...)
	out = append(out, b[end+len("</style>"):]...)
	return writeFileAtomically(path, out, 0644)
}

func covdataTextfmt(inputDir, output string) error {
	cmd := exec.Command("go", "tool", "covdata", "textfmt", "-i="+inputDir, "-o="+output)
	if data, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go tool covdata textfmt: %w: %s", err, data)
	}
	return nil
}

func readProfile(path string) (coverageProfile, error) {
	file, err := os.Open(path)
	if err != nil {
		return coverageProfile{}, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), "mode: ") {
		return coverageProfile{}, fmt.Errorf("invalid coverage profile %s", path)
	}
	profile := coverageProfile{Mode: strings.TrimSpace(scanner.Text())}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 3 {
			return coverageProfile{}, fmt.Errorf("invalid coverage row %q", line)
		}
		statements, err := strconv.Atoi(parts[1])
		if err != nil {
			return coverageProfile{}, fmt.Errorf("invalid statement count in %q", line)
		}
		count, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return coverageProfile{}, fmt.Errorf("invalid counter in %q", line)
		}
		profile.Blocks = append(profile.Blocks, profileBlock{Key: parts[0], Statements: statements, Count: count})
	}
	if err := scanner.Err(); err != nil {
		return coverageProfile{}, err
	}
	return profile, nil
}

func subtractProfiles(baseline, finish coverageProfile) (coverageProfile, error) {
	if baseline.Mode != finish.Mode {
		return coverageProfile{}, errors.New("coverage mode mismatch")
	}
	base := make(map[string]profileBlock, len(baseline.Blocks))
	for _, block := range baseline.Blocks {
		base[block.Key] = block
	}
	if len(base) != len(finish.Blocks) {
		return coverageProfile{}, errors.New("coverage block mismatch")
	}
	result := coverageProfile{Mode: finish.Mode}
	for _, block := range finish.Blocks {
		before, ok := base[block.Key]
		if !ok || before.Statements != block.Statements {
			return coverageProfile{}, fmt.Errorf("coverage block mismatch for %s", block.Key)
		}
		block.Count -= before.Count
		if block.Count < 0 {
			return coverageProfile{}, fmt.Errorf("coverage counter decreased for %s", block.Key)
		}
		result.Blocks = append(result.Blocks, block)
	}
	return result, nil
}

func writeProfile(path string, profile coverageProfile) error {
	var out bytes.Buffer
	fmt.Fprintln(&out, profile.Mode)
	for _, block := range profile.Blocks {
		fmt.Fprintf(&out, "%s %d %d\n", block.Key, block.Statements, block.Count)
	}
	return writeFileAtomically(path, out.Bytes(), 0644)
}

func materializeAndRewrite(repoRoot, sha, sourceRoot string, profile *coverageProfile) error {
	paths, err := gitFiles(repoRoot, sha)
	if err != nil {
		return err
	}
	extracted := make(map[string]string)
	for i := range profile.Blocks {
		key := profile.Blocks[i].Key
		colon := strings.LastIndex(key, ":")
		if colon < 1 {
			return fmt.Errorf("coverage block has no source range: %s", key)
		}
		original := filepath.ToSlash(key[:colon])
		repoPath, err := matchRepoPath(original, paths)
		if err != nil {
			return err
		}
		target, ok := extracted[repoPath]
		if !ok {
			target = filepath.Join(sourceRoot, filepath.FromSlash(repoPath))
			data, err := gitShow(repoRoot, sha, repoPath)
			if err != nil {
				return err
			}
			if err := writeFileAtomically(target, data, 0644); err != nil {
				return err
			}
			extracted[repoPath] = target
		}
		absolute, err := filepath.Abs(target)
		if err != nil {
			return err
		}
		profile.Blocks[i].Key = filepath.ToSlash(absolute) + key[colon:]
	}
	return nil
}

func matchRepoPath(original string, paths []string) (string, error) {
	original = strings.TrimPrefix(filepath.ToSlash(original), "./")
	var matches []string
	for _, path := range paths {
		if original == path || strings.HasSuffix(original, "/"+path) {
			matches = append(matches, path)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("source file %q is unavailable in Git revision", original)
	}
	sort.Slice(matches, func(i, j int) bool { return len(matches[i]) > len(matches[j]) })
	if len(matches) > 1 && len(matches[0]) == len(matches[1]) {
		return "", fmt.Errorf("source file %q is ambiguous in Git revision", original)
	}
	return matches[0], nil
}

func gitFiles(repoRoot, sha string) ([]string, error) {
	data, err := gitOutput(repoRoot, "ls-tree", "-r", "--name-only", sha)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, path := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasSuffix(path, ".go") {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func gitShow(repoRoot, sha, path string) ([]byte, error) {
	return gitOutput(repoRoot, "show", sha+":"+path)
}

func resolveCommit(repoRoot, revision string) (string, error) {
	data, err := gitOutput(repoRoot, "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("Git commit %q is unavailable: %w", revision, err)
	}
	return strings.TrimSpace(string(data)), nil
}

func gitChanges(repoRoot, oldSHA, finalSHA string) ([]sourceChange, error) {
	data, err := gitOutput(repoRoot, "diff", "--name-status", "-z", "--find-renames", oldSHA, finalSHA)
	if err != nil {
		return nil, err
	}
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

func commandOutput(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	data, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, data)
	}
	return data, nil
}

// gitOutput uses Cmd.Dir instead of "git -C" so the report also works with
// the older Git client installed on the current Jenkins host.
func gitOutput(repoRoot string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot
	data, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s in %s: %w: %s", strings.Join(args, " "), repoRoot, err, data)
	}
	return data, nil
}

func writeFileAtomically(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func relativePath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

func safeName(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, value)
}

var indexTemplate = template.Must(template.New("index").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>需求级运行覆盖率报告</title><style>
:root{--bg:#f4f7fb;--panel:#fff;--ink:#172033;--muted:#667085;--line:#e5eaf1;--blue:#2563eb;--green:#12a150;--amber:#b7791f}*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC",sans-serif}.top{background:linear-gradient(120deg,#172554,#2563eb);color:#fff}.wrap{max-width:1180px;margin:auto;padding:30px 24px}.top h1{margin:0;font-size:28px}.top p{margin:6px 0 0;color:#dbeafe}.summary{display:grid;grid-template-columns:repeat(4,1fr);gap:14px;margin-top:22px}.metric,.panel{background:var(--panel);border:1px solid var(--line);border-radius:12px;box-shadow:0 4px 14px #1725540b}.metric{padding:18px}.metric label{display:block;color:var(--muted);font-size:12px}.metric strong{display:block;margin-top:4px;font-size:24px}.metric .good{color:var(--green)}.panel{margin-top:18px;padding:22px}.panel h2{font-size:17px;margin:0 0 16px}.info{display:grid;grid-template-columns:repeat(2,1fr);gap:10px;color:var(--muted)}.info b{color:var(--ink);font-weight:600}.segment{border-top:1px solid var(--line);padding:20px 0}.segment:first-of-type{border-top:0;padding-top:0}.segment-head{display:flex;align-items:center;gap:10px;flex-wrap:wrap}.tag{padding:3px 9px;border-radius:999px;background:#eff6ff;color:var(--blue);font-size:12px}.sha{font:12px ui-monospace,SFMono-Regular,Menlo,monospace;color:var(--muted)}.coverage{display:flex;align-items:end;gap:14px;margin:16px 0 8px}.pct{font-size:34px;font-weight:750;color:var(--green)}.count{color:var(--muted)}.bar{height:10px;background:#e8edf4;border-radius:99px;overflow:hidden}.bar i{display:block;height:100%;background:linear-gradient(90deg,#16a05d,#34d399);border-radius:99px}.links{margin-top:14px;display:flex;gap:18px;flex-wrap:wrap}.links a{color:var(--blue);text-decoration:none}.links a:hover{text-decoration:underline}.warn{margin-top:12px;padding:10px 12px;background:#fffbeb;border-left:3px solid #eab308;color:var(--amber)}.changes{margin-top:10px;color:var(--muted)}.footer{color:var(--muted);font-size:12px;margin-top:18px}@media(max-width:760px){.summary{grid-template-columns:repeat(2,1fr)}.info{grid-template-columns:1fr}}@media(max-width:480px){.summary{grid-template-columns:1fr}.wrap{padding:22px 14px}}
</style></head><body><header class="top"><div class="wrap"><h1>需求级运行覆盖率报告</h1><p>基于真实测试窗口统计运行时覆盖率，按部署版本安全分段。</p></div></header><main class="wrap">
<section class="summary"><div class="metric"><label>覆盖率分段</label><strong>{{len .Segments}}</strong></div><div class="metric"><label>测试窗口</label><strong class="good">已完成</strong></div><div class="metric"><label>起始快照</label><strong>{{.StartID}}</strong></div><div class="metric"><label>结束快照</label><strong>{{.FinishID}}</strong></div></section>
<section class="panel"><h2>测试窗口信息</h2><div class="info"><div>最终版本 SHA：<b class="sha">{{.FinalSHA}}</b></div><div>生成时间：<b>{{.GeneratedAt.Format "2006-01-02 15:04:05 MST"}}</b></div><div>起始快照：<b>{{.StartID}}</b></div><div>结束快照：<b>{{.FinishID}}</b></div></div></section>
<section class="panel"><h2>覆盖率分段结果</h2>{{range .Segments}}<article class="segment"><div class="segment-head"><span class="tag">分段 {{.Index}}</span><strong>{{.DeploymentID}}</strong><span class="sha">{{slice .BuildSHA 0 12}}</span><span class="sha">PID {{.PID}}</span></div><div class="coverage"><span class="pct">{{printf "%.1f" .Percent}}%</span><span class="count">{{.Covered}} / {{.Total}} statements<br>{{.Start.SnapshotID}} → {{.Finish.SnapshotID}}</span></div><div class="bar"><i style="width:{{printf "%.1f" .Percent}}%"></i></div><div class="links"><a href="{{.HTML}}">源码覆盖明细</a><a href="{{.Functions}}">函数统计</a><a href="{{.Profile}}">原始 profile</a></div>{{if ne .Relation "final"}}<div class="warn">历史代码版本：覆盖率按当时源码计算，未与后续版本计数器直接合并。</div>{{end}}{{if .Changes}}<div class="changes">到最终版本共有 {{len .Changes}} 个源码路径变化。</div>{{end}}</article>{{end}}</section>
<div class="footer">报告由 go-coverage-report 生成 · 覆盖率数据来自 go-coverage-hook</div></main></body></html>`))

var dashboardTemplate = template.Must(template.New("dashboard").Funcs(template.FuncMap{"add": add, "mul": mul, "sub": sub}).Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>需求覆盖率总览</title><style>
:root{--ink:#344054;--muted:#667085;--line:#e4e7ec;--blue:#4285d4;--panel:#fff;--bg:#f8fafc}*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC",sans-serif}.top{height:64px;background:#fff;border-bottom:1px solid var(--line);display:flex;align-items:center;padding:0 30px;justify-content:space-between}.top h1{font-size:24px;margin:0;font-weight:650}.tools{color:#667085;letter-spacing:12px}.wrap{max-width:1280px;margin:auto;padding:12px 30px 34px}.filters{display:flex;gap:10px;flex-wrap:wrap;margin:0 0 14px}.filter{background:#fff;border:1px solid #d0d5dd;border-radius:7px;padding:7px 12px;color:#667085}.filter b{color:var(--blue);font-weight:600}.notice{background:#fff;border:1px solid var(--line);border-radius:8px;padding:15px 18px;margin-bottom:10px;font-size:15px}.grid4{display:grid;grid-template-columns:repeat(4,1fr);gap:8px}.card{background:var(--panel);border:1px solid var(--line);border-radius:8px;padding:14px 18px;min-height:128px}.card h3{font-size:14px;margin:0;color:#475467}.card .num{font-size:38px;font-weight:650;text-align:center;margin:20px 0 0}.card .blue{color:var(--blue)}.layout{display:grid;grid-template-columns:1.4fr 1fr;gap:8px;margin-top:8px}.panel{background:#fff;border:1px solid var(--line);border-radius:8px;padding:14px 18px;min-height:260px}.panel h2{font-size:15px;margin:0 0 8px}.chart{width:100%;height:190px}.axis{fill:#667085;font-size:11px}.line{fill:none;stroke:#67c7d4;stroke-width:3}.dot{fill:#fff;stroke:#67c7d4;stroke-width:3}.dash{stroke:#667085;stroke-dasharray:4 4}.legend{display:flex;gap:12px;justify-content:center;color:#667085;font-size:12px}.legend i{display:inline-block;width:9px;height:9px;border-radius:50%;margin-right:4px}.seg{margin-top:8px;border-top:1px solid var(--line);padding-top:10px}.seg:first-of-type{border-top:0}.seghead{display:flex;justify-content:space-between;gap:8px;align-items:center}.tag{background:#eef4ff;color:#3b73c5;border-radius:12px;padding:2px 8px;font-size:12px}.bar{height:16px;background:#edf1f5;border-radius:3px;margin:10px 0;overflow:hidden}.bar i{display:block;height:100%;background:#67c7d4}.links a{color:var(--blue);text-decoration:none;margin-right:16px}.muted{color:var(--muted);font-size:12px}.footer{margin-top:12px;color:var(--muted);font-size:12px}@media(max-width:800px){.grid4,.layout{grid-template-columns:1fr 1fr}.top{padding:0 16px}.wrap{padding:12px 16px}}@media(max-width:520px){.grid4,.layout{grid-template-columns:1fr}.top h1{font-size:20px}}
</style></head><body><header class="top"><h1>01 · 需求覆盖率总览</h1><div class="tools">✎　↗　◷　⋮</div></header><main class="wrap"><div class="filters"><span class="filter">服务：<b>Go 服务</b> ×</span><span class="filter">仓库：<b>{{.FinalSHA}}</b> ×</span><span class="filter">开始快照：<b>{{.StartID}}</b> ×</span><span class="filter">结束快照：<b>{{.FinishID}}</b>⌄</span></div><div class="notice"><b>需求级运行覆盖率</b> · 基于真实测试窗口统计，按构建 SHA 和部署实例安全分段；点击下方代码明细查看 GitLab 风格源码覆盖。</div><section class="grid4"><div class="card"><h3>覆盖率分段</h3><div class="num blue">{{len .Segments}}</div></div><div class="card"><h3>测试窗口</h3><div class="num">已完成</div></div><div class="card"><h3>报告分段</h3><div class="num">{{len .Segments}}</div></div><div class="card"><h3>最终版本</h3><div class="num" style="font-size:20px;margin-top:30px">{{slice .FinalSHA 0 12}}</div></div></section><section class="layout"><div class="panel"><h2>增量覆盖率趋势 · 按分段统计</h2><svg class="chart" viewBox="0 0 700 210" role="img" aria-label="覆盖率趋势"><line x1="55" y1="25" x2="55" y2="175" stroke="#d0d5dd"/><line x1="55" y1="175" x2="670" y2="175" stroke="#d0d5dd"/><line class="dash" x1="55" y1="55" x2="670" y2="55"/><text class="axis" x="8" y="59">80%</text><text class="axis" x="20" y="179">0%</text><polyline class="line" points="{{range $i,$s:=.Segments}}{{if $i}} {{end}}{{add 80 (mul $i 150)}},{{sub 175 (mul $s.Percent 1.5)}}{{end}}"/>{{range $i,$s:=.Segments}}<circle class="dot" cx="{{add 80 (mul $i 150)}}" cy="{{sub 175 (mul $s.Percent 1.5)}}" r="5"/><text class="axis" x="{{sub (add 80 (mul $i 150)) 18}}" y="198">分段 {{$s.Index}}</text>{{end}}</svg><div class="legend"><span><i style="background:#67c7d4"></i>增量覆盖率</span><span><i style="background:#667085"></i>演示阈值 80%</span></div></div><div class="panel"><h2>各部署分段状态</h2>{{range .Segments}}<div class="seg"><div class="seghead"><span class="tag">分段 {{.Index}}</span><b>{{printf "%.1f" .Percent}}%</b></div><div class="bar"><i style="width:{{printf "%.1f" .Percent}}%"></i></div><div class="muted">{{.DeploymentID}} · PID {{.PID}}</div></div>{{end}}</div></section><section class="panel" style="margin-top:8px"><h2>覆盖率分段明细</h2>{{range .Segments}}<div class="seg"><div class="seghead"><div><span class="tag">分段 {{.Index}}</span> <b>{{.DeploymentID}}</b> <span class="muted">{{slice .BuildSHA 0 12}}</span></div><span><b>{{printf "%.1f" .Percent}}%</b> · {{.Covered}} / {{.Total}} statements</span></div><div class="links" style="margin-top:8px"><a href="{{.HTML}}">代码明细</a><a href="{{.Functions}}">函数统计</a><a href="{{.Profile}}">原始 profile</a></div><div class="muted">{{.Start.SnapshotID}} → {{.Finish.SnapshotID}}</div></div>{{end}}</section><div class="footer">报告生成时间：{{.GeneratedAt.Format "2006-01-02 15:04:05 MST"}} · go-coverage-report</div></main></body></html>`))

func number(v interface{}) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case float64:
		return n
	default:
		return 0
	}
}
func add(a, b interface{}) float64 { return number(a) + number(b) }
func mul(a, b interface{}) float64 { return number(a) * number(b) }
func sub(a, b float64) float64     { return a - b }

func writeIndex(path string, result report) error {
	var out bytes.Buffer
	if err := dashboardTemplate.Execute(&out, result); err != nil {
		return err
	}
	return writeFileAtomically(path, out.Bytes(), 0644)
}
