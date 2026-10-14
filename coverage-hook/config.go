// Package coveragehook provides an opt-in runtime coverage snapshot endpoint.
package coveragehook

import "context"

// Config is used only by coverage_snapshot builds. Ordinary builds ignore it.
type Config struct {
	// Enabled must be true to start the control listener. Keep it false outside
	// the test environment, including when a coverage-tagged binary is reused.
	Enabled bool
	// ListenAddress must bind to 127.0.0.1 or ::1 with an explicit port.
	ListenAddress string
	// SnapshotDir is an absolute, writable directory for separate snapshots.
	SnapshotDir string
	// Token is a deployment secret of at least 32 bytes.
	Token string
	// BuildSHA identifies the exact source revision embedded in this binary.
	// Coverage builds must inject it at build time instead of reading a mutable
	// branch name at runtime.
	BuildSHA string
	// DeploymentID identifies the deployment/process generation that produced
	// the snapshot. It is supplied by the deployment system at runtime.
	DeploymentID string
}

// StopFunc shuts down the snapshot listener without stopping the business service.
type StopFunc func(context.Context) error
