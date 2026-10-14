//go:build !coverage_snapshot
// +build !coverage_snapshot

package coveragehook

import "context"

// Start is a no-op in ordinary builds; no snapshot listener is opened.
func Start(_ Config) (StopFunc, error) {
	return func(context.Context) error { return nil }, nil
}
