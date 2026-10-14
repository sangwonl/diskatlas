//go:build darwin

package core

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFSEventsReplayAfterMutation(t *testing.T) {
	if os.Getenv("DISKATLAS_NATIVE_JOURNAL_TEST") != "1" {
		t.Skip("native journal test is opt-in")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	start, _, complete, err := readJournal(root, journalCursor{})
	if err != nil || !complete {
		t.Fatalf("cannot establish journal checkpoint: %v", err)
	}
	path := filepath.Join(root, "changed")
	if err := os.WriteFile(path, []byte("changed after checkpoint"), 0644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		_, changes, complete, err := readJournal(root, start)
		if err != nil || !complete {
			t.Fatalf("history read failed: %v", err)
		}
		for _, change := range changes {
			if change.Path == path {
				current, _, complete, err := readJournal(root, journalCursor{})
				if err != nil || !complete {
					t.Fatalf("cannot checkpoint unchanged tree: %v", err)
				}
				_, _, complete, err = readJournal(root, current)
				if err != nil || !complete {
					t.Fatalf("unchanged history must remain usable: %v", err)
				}
				return
			}
		}
	}
	t.Fatal("FSEvents did not replay the changed file")
}
