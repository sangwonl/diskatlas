//go:build darwin

package core

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Foundation
#include <stdlib.h>
#include <string.h>
#import <Foundation/Foundation.h>

static char *diskatlas_trash_item(const char *path, char **errorOutput) {
	@autoreleasepool {
		NSString *sourcePath = [NSString stringWithUTF8String:path];
		if (sourcePath == nil) {
			if (errorOutput != NULL) *errorOutput = strdup("경로를 읽을 수 없습니다.");
			return NULL;
		}
		NSURL *sourceURL = [NSURL fileURLWithPath:sourcePath];
		NSURL *resultURL = nil;
		NSError *error = nil;
		if (![[NSFileManager defaultManager] trashItemAtURL:sourceURL resultingItemURL:&resultURL error:&error]) {
			if (errorOutput != NULL) *errorOutput = strdup([[error localizedDescription] UTF8String]);
			return NULL;
		}
		const char *resultPath = [[resultURL path] UTF8String];
		return resultPath == NULL ? NULL : strdup(resultPath);
	}
}
*/
import "C"

import (
	"errors"
	"unsafe"
)

func platformMoveToTrash(path string) (string, error) {
	input := C.CString(path)
	defer C.free(unsafe.Pointer(input))
	var errorOutput *C.char
	result := C.diskatlas_trash_item(input, &errorOutput)
	defer freeTrashCString(errorOutput)
	defer freeTrashCString(result)
	if result == nil {
		if errorOutput != nil {
			if detail := C.GoString(errorOutput); detail != "" {
				return "", errors.New(detail)
			}
		}
		return "", errors.New("macOS에서 항목을 휴지통으로 옮기지 못했습니다")
	}
	resultPath := C.GoString(result)
	if resultPath == "" {
		return "", errors.New("macOS에서 항목을 휴지통으로 옮기지 못했습니다")
	}
	return resultPath, nil
}

func freeTrashCString(value *C.char) {
	if value != nil {
		C.free(unsafe.Pointer(value))
	}
}
