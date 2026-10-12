//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Foundation
#import <Foundation/Foundation.h>

static void *shed_begin_folder_map_activity(void) {
	@autoreleasepool {
		id activity = [[NSProcessInfo processInfo]
			beginActivityWithOptions:NSActivityUserInitiatedAllowingIdleSystemSleep
			reason:@"Calculating folder sizes"];
		return (void *)[activity retain];
	}
}

static void shed_end_folder_map_activity(void *activity) {
	if (activity == NULL) return;
	@autoreleasepool {
		[[NSProcessInfo processInfo] endActivity:(id)activity];
		[(id)activity release];
	}
}
*/
import "C"

func beginFolderMapActivity() func() {
	activity := C.shed_begin_folder_map_activity()
	return func() {
		C.shed_end_folder_map_activity(activity)
	}
}
