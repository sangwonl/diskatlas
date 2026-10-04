//go:build !darwin && !linux && !windows

package core

import (
	"fmt"
	"runtime"
)

// readJournal is implemented per operating system. Unsupported platforms
// return complete=false so callers safely fall back to a metadata scan.
func readJournal(root string, cursor journalCursor) (journalCursor, []journalChange, bool, error) {
	return cursor, nil, false, fmt.Errorf("filesystem change journal is not implemented on %s", runtime.GOOS)
}
