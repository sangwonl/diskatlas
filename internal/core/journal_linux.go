//go:build linux

package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

type inotifyState struct {
	fd         int
	root       string
	mu         sync.Mutex
	paths      map[int]string
	generation uint64
}

var inotifyStates sync.Mutex
var inotifyByRoot = map[string]*inotifyState{}

func readJournal(root string, cursor journalCursor) (journalCursor, []journalChange, bool, error) {
	root = filepath.Clean(root)
	inotifyStates.Lock()
	state := inotifyByRoot[root]
	fresh := state == nil
	if state == nil {
		var err error
		state, err = startInotify(root)
		if err != nil {
			inotifyStates.Unlock()
			return cursor, nil, false, err
		}
		inotifyByRoot[root] = state
	}
	inotifyStates.Unlock()

	state.mu.Lock()
	defer state.mu.Unlock()
	if cursor.ID == 0 {
		state.generation++
		return journalCursor{Kind: "inotify", Volume: root, ID: state.generation}, nil, true, nil
	}
	if fresh {
		return journalCursor{Kind: "inotify", Volume: root, ID: state.generation}, nil, false, fmt.Errorf("inotify history is not persistent across process restarts")
	}
	if cursor.Kind != "inotify" || cursor.Volume != root {
		return journalCursor{Kind: "inotify", Volume: root, ID: state.generation}, nil, false, fmt.Errorf("inotify cursor is not valid for %s", root)
	}
	changes, overflow := drainInotify(state)
	if overflow {
		return journalCursor{Kind: "inotify", Volume: root, ID: state.generation}, nil, false, fmt.Errorf("inotify queue overflowed")
	}
	if len(changes) > 0 {
		state.generation++
	}
	return journalCursor{Kind: "inotify", Volume: root, ID: state.generation}, changes, true, nil
}

func startInotify(root string) (*inotifyState, error) {
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		return nil, err
	}
	state := &inotifyState{fd: fd, root: root, paths: map[int]string{}}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		watch, watchErr := unix.InotifyAddWatch(fd, path, unix.IN_CREATE|unix.IN_DELETE|unix.IN_MODIFY|unix.IN_MOVED_FROM|unix.IN_MOVED_TO|unix.IN_ATTRIB|unix.IN_CLOSE_WRITE|unix.IN_DELETE_SELF|unix.IN_MOVE_SELF)
		if watchErr != nil {
			return watchErr
		}
		state.paths[watch] = path
		return nil
	})
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	return state, nil
}

func drainInotify(state *inotifyState) ([]journalChange, bool) {
	changes := []journalChange{}
	buffer := make([]byte, 64*1024)
	eventSize := int(unsafe.Sizeof(unix.InotifyEvent{}))
	for {
		n, err := unix.Read(state.fd, buffer)
		if err != nil {
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
				return changes, false
			}
			return changes, true
		}
		if n == 0 {
			return changes, false
		}
		for offset := 0; offset+eventSize <= n; {
			event := (*unix.InotifyEvent)(unsafe.Pointer(&buffer[offset]))
			nameStart := offset + eventSize
			nameEnd := nameStart + int(event.Len)
			if nameEnd > n {
				return changes, true
			}
			name := strings.TrimRight(string(buffer[nameStart:nameEnd]), "\x00")
			if event.Mask&unix.IN_Q_OVERFLOW != 0 {
				return changes, true
			}
			if directory, ok := state.paths[int(event.Wd)]; ok {
				path := directory
				if name != "" {
					path = filepath.Join(directory, name)
				}
				changes = append(changes, journalChange{Path: path, Flags: event.Mask})
				if event.Mask&unix.IN_ISDIR != 0 && event.Mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0 {
					if watch, err := unix.InotifyAddWatch(state.fd, path, unix.IN_CREATE|unix.IN_DELETE|unix.IN_MODIFY|unix.IN_MOVED_FROM|unix.IN_MOVED_TO|unix.IN_ATTRIB|unix.IN_CLOSE_WRITE|unix.IN_DELETE_SELF|unix.IN_MOVE_SELF); err == nil {
						state.paths[watch] = path
					}
				}
			}
			offset = nameEnd
		}
	}
}
