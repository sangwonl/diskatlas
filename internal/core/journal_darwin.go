//go:build darwin

package core

/*
#cgo darwin LDFLAGS: -framework CoreServices -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
#include <CoreServices/CoreServices.h>
#include <dispatch/dispatch.h>
#include <stdint.h>
#include <stdlib.h>

extern void shedGoFSEventCallback(uintptr_t token, size_t count, char **paths, uint32_t *flags, uint64_t *ids);

static void shed_fsevent_callback(
    ConstFSEventStreamRef stream,
    void *info,
    size_t count,
    void *event_paths,
    const FSEventStreamEventFlags flags[],
    const FSEventStreamEventId ids[]) {
    (void)stream;
    char **paths = (char **)event_paths;
    shedGoFSEventCallback((uintptr_t)info, count, paths, (uint32_t *)flags, (uint64_t *)ids);
}

static int shed_read_fsevents(const char *root, uint64_t since, uintptr_t token, uint64_t *latest) {
    CFStringRef root_string = CFStringCreateWithCString(NULL, root, kCFStringEncodingUTF8);
    if (root_string == NULL) {
        return -1;
    }
    const void *values[] = { root_string };
    CFArrayRef paths = CFArrayCreate(NULL, values, 1, &kCFTypeArrayCallBacks);
    if (paths == NULL) {
        CFRelease(root_string);
        return -1;
    }
    FSEventStreamContext context = {0, (void *)token, NULL, NULL, NULL};
    FSEventStreamCreateFlags create_flags =
        kFSEventStreamCreateFlagFileEvents |
        kFSEventStreamCreateFlagNoDefer |
        kFSEventStreamCreateFlagWatchRoot;
    FSEventStreamRef stream = FSEventStreamCreate(
        NULL,
        shed_fsevent_callback,
        &context,
        paths,
        (FSEventStreamEventId)since,
        0.05,
        create_flags);
    CFRelease(paths);
    CFRelease(root_string);
    if (stream == NULL) {
        return -2;
    }

    // FSEvents expects a stable serial delivery queue. A process-global
    // concurrent queue can make FSEventStreamStart fail on recent macOS.
    dispatch_queue_t queue = dispatch_queue_create("dev.shed.fsevents", DISPATCH_QUEUE_SERIAL);
    if (queue == NULL) {
        FSEventStreamInvalidate(stream);
        FSEventStreamRelease(stream);
        return -3;
    }
    FSEventStreamSetDispatchQueue(stream, queue);
    if (!FSEventStreamStart(stream)) {
        FSEventStreamInvalidate(stream);
        FSEventStreamRelease(stream);
        dispatch_release(queue);
        return -4;
    }

    FSEventStreamFlushSync(stream);
    if (latest != NULL) {
        *latest = (uint64_t)FSEventStreamGetLatestEventId(stream);
    }
    FSEventStreamStop(stream);
    FSEventStreamInvalidate(stream);
    FSEventStreamRelease(stream);
    dispatch_release(queue);
    return 0;
}

static uint64_t shed_current_fsevent_id(void) {
    return (uint64_t)FSEventsGetCurrentEventId();
}
*/
import "C"

import (
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"
)

var (
	fseventMu      sync.Mutex
	fseventBatches = map[uintptr][]journalChange{}
	fseventToken   atomic.Uint64
)

//export shedGoFSEventCallback
func shedGoFSEventCallback(token C.uintptr_t, count C.size_t, paths **C.char, flags *C.uint32_t, ids *C.uint64_t) {
	pathSlice := unsafe.Slice(paths, int(count))
	flagSlice := unsafe.Slice(flags, int(count))
	_ = ids
	changes := make([]journalChange, 0, int(count))
	for i, path := range pathSlice {
		if path == nil {
			continue
		}
		if uint32(flagSlice[i])&uint32(C.kFSEventStreamEventFlagHistoryDone) != 0 {
			continue
		}
		changes = append(changes, journalChange{Path: C.GoString(path), Flags: uint32(flagSlice[i])})
	}
	fseventMu.Lock()
	fseventBatches[uintptr(token)] = append(fseventBatches[uintptr(token)], changes...)
	fseventMu.Unlock()
}

func readJournal(root string, cursor journalCursor) (journalCursor, []journalChange, bool, error) {
	if cursor.Kind != "" && cursor.Kind != "fsevents" {
		return cursor, nil, false, fmt.Errorf("incompatible journal cursor: %s", cursor.Kind)
	}
	if cursor.ID == 0 {
		id := uint64(C.shed_current_fsevent_id())
		if id == 0 {
			return cursor, nil, false, fmt.Errorf("FSEvents current event ID is unavailable")
		}
		return journalCursor{Kind: "fsevents", ID: id}, nil, true, nil
	}
	token := uintptr(fseventToken.Add(1))
	fseventMu.Lock()
	fseventBatches[token] = nil
	fseventMu.Unlock()
	defer func() {
		fseventMu.Lock()
		delete(fseventBatches, token)
		fseventMu.Unlock()
	}()

	var latest C.uint64_t
	since := C.uint64_t(cursor.ID)
	croot := C.CString(root)
	defer C.free(unsafe.Pointer(croot))
	if status := C.shed_read_fsevents(croot, since, C.uintptr_t(token), &latest); status != 0 {
		return cursor, nil, false, fmt.Errorf("FSEventStream failed (%d)", int(status))
	}
	fseventMu.Lock()
	changes := append([]journalChange(nil), fseventBatches[token]...)
	fseventMu.Unlock()
	for _, change := range changes {
		if change.Flags&uint32(C.kFSEventStreamEventFlagUserDropped|C.kFSEventStreamEventFlagKernelDropped|C.kFSEventStreamEventFlagEventIdsWrapped|C.kFSEventStreamEventFlagRootChanged) != 0 {
			return cursor, nil, false, fmt.Errorf("FSEvents history is incomplete (flags: %d)", change.Flags)
		}
	}
	nextID := uint64(latest)
	if nextID == 0 {
		nextID = cursor.ID
	}
	return journalCursor{Kind: "fsevents", ID: nextID}, changes, true, nil
}
