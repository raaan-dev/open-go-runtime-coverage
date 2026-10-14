//go:build !coverage_snapshot
// +build !coverage_snapshot

package coveragehook

import (
	"context"
	"testing"
)

func TestOrdinaryBuildDoesNotStartSnapshotListener(t *testing.T) {
	stop, err := Start(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
