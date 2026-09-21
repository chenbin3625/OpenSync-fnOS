package service

import (
	"fmt"
	"testing"
)

func snapshotWithEntries(root string, n int) *fullSyncSnapshot {
	items := make(FileListResult, n)
	for i := 0; i < n; i++ {
		items[fmt.Sprintf("f%d.txt", i)] = FileMetadata{Size: 1}
	}
	return &fullSyncSnapshot{root: root, dirs: map[string]FileListResult{"": items}}
}

// Both conditions must trip: a high ratio on a tiny directory is ordinary
// cleanup, and a big-but-small-share purge is legitimate.
func TestUnsafeDeleteVolumeRequiresRatioAndFloor(t *testing.T) {
	cases := []struct {
		name        string
		dstEntries  int
		deleteCount int
		wantUnsafe  bool
	}{
		{"small dir fully cleaned is allowed", 10, 10, false},
		{"below floor is allowed", 1000, 99, false},
		{"large share above floor is blocked", 1000, 900, true},
		{"exactly at ratio is allowed", 1000, 500, false},
		{"just over ratio above floor is blocked", 1000, 501, true},
		{"big purge that is a small share is allowed", 100000, 1000, false},
		{"empty destination is allowed", 0, 0, false},
		{"whole tree wipe is blocked", 500, 500, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := newFullSyncPlan(
				snapshotWithEntries("/src/", 0),
				snapshotWithEntries("/dst/", tc.dstEntries),
				nil,
			)
			reason, unsafe := plan.unsafeDeleteVolume(tc.deleteCount)
			if unsafe != tc.wantUnsafe {
				t.Fatalf("unsafeDeleteVolume(%d) with %d entries = %v (%q), want %v",
					tc.deleteCount, tc.dstEntries, unsafe, reason, tc.wantUnsafe)
			}
			if unsafe && reason == "" {
				t.Fatal("blocked without an explanation")
			}
		})
	}
}
