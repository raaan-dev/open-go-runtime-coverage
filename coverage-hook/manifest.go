package coveragehook

import (
	"errors"
	"regexp"
	"time"
)

// SnapshotManifestVersion is incremented when the on-disk manifest contract
// changes incompatibly.
const SnapshotManifestVersion = 1

var snapshotIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
var buildSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
var deploymentIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// SnapshotManifest binds coverage data to the exact code and deployment that
// produced it. Reports must not treat snapshots with different BuildSHA values
// as one source-compatible coverage run.
type SnapshotManifest struct {
	SchemaVersion int       `json:"schema_version"`
	SnapshotID    string    `json:"snapshot_id"`
	BuildSHA      string    `json:"build_sha"`
	DeploymentID  string    `json:"deployment_id"`
	PID           int       `json:"pid"`
	CreatedAt     time.Time `json:"created_at"`
}

// Validate rejects legacy, incomplete, or malformed manifests before they are
// used to associate coverage with source code.
func (m SnapshotManifest) Validate() error {
	if m.SchemaVersion != SnapshotManifestVersion {
		return errors.New("unsupported snapshot manifest schema")
	}
	if !snapshotIDPattern.MatchString(m.SnapshotID) {
		return errors.New("invalid snapshot ID")
	}
	if err := validateBuildIdentity(m.BuildSHA, m.DeploymentID); err != nil {
		return err
	}
	if m.PID <= 0 {
		return errors.New("invalid process ID")
	}
	if m.CreatedAt.IsZero() {
		return errors.New("missing creation time")
	}
	return nil
}

func validateBuildIdentity(buildSHA, deploymentID string) error {
	if !buildSHAPattern.MatchString(buildSHA) {
		return errors.New("BuildSHA must be a full lowercase Git object ID")
	}
	if !deploymentIDPattern.MatchString(deploymentID) {
		return errors.New("DeploymentID contains unsupported characters")
	}
	return nil
}
