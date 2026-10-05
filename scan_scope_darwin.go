//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Foundation -framework AppKit
#include <dispatch/dispatch.h>
#include <stdlib.h>
#import <AppKit/AppKit.h>

static NSURL *shedActiveScopeURL = nil;
static BOOL shedActiveScopeIsSecurityScoped = NO;

static char *shed_copy_string(NSString *value) {
	if (value == nil) return NULL;
	const char *utf8 = [value UTF8String];
	return utf8 == NULL ? NULL : strdup(utf8);
}

static BOOL shed_is_sandboxed(void) {
	return [[[NSProcessInfo processInfo] environment] objectForKey:@"APP_SANDBOX_CONTAINER_ID"] != nil;
}

static void shed_clear_active_scope(void) {
	if (shedActiveScopeURL != nil) {
		if (shedActiveScopeIsSecurityScoped) {
			[shedActiveScopeURL stopAccessingSecurityScopedResource];
		}
		[shedActiveScopeURL release];
		shedActiveScopeURL = nil;
		shedActiveScopeIsSecurityScoped = NO;
	}
}

static BOOL shed_set_active_scope(NSURL *url, BOOL securityScoped, NSString **errorMessage) {
	if (securityScoped && ![url startAccessingSecurityScopedResource]) {
		if (errorMessage != NULL) *errorMessage = @"macOS가 선택한 폴더 접근 권한을 열지 못했습니다.";
		return NO;
	}
	shed_clear_active_scope();
	shedActiveScopeURL = [url retain];
	shedActiveScopeIsSecurityScoped = securityScoped;
	return YES;
}

static NSData *shed_bookmark_for_url(NSURL *url, BOOL sandboxed, NSError **error) {
	NSURLBookmarkCreationOptions options = sandboxed ? NSURLBookmarkCreationWithSecurityScope : 0;
	return [url bookmarkDataWithOptions:options includingResourceValuesForKeys:nil relativeToURL:nil error:error];
}

static char *shed_choose_folder(const char *initialPath, char **bookmarkOutput, char **errorOutput) {
	__block NSString *selectedPath = nil;
	__block NSString *selectedBookmark = nil;
	__block NSString *failure = nil;
	BOOL sandboxed = shed_is_sandboxed();
	dispatch_sync(dispatch_get_main_queue(), ^{
		@autoreleasepool {
			NSOpenPanel *panel = [NSOpenPanel openPanel];
			panel.title = @"분석할 폴더 선택";
			panel.canChooseFiles = NO;
			panel.canChooseDirectories = YES;
			panel.canCreateDirectories = NO;
			panel.allowsMultipleSelection = NO;
			panel.treatsFilePackagesAsDirectories = YES;
			if (initialPath != NULL && initialPath[0] != '\0') {
				NSString *initial = [NSString stringWithUTF8String:initialPath];
				BOOL isDirectory = NO;
				if ([[NSFileManager defaultManager] fileExistsAtPath:initial isDirectory:&isDirectory] && isDirectory) {
					panel.directoryURL = [NSURL fileURLWithPath:initial isDirectory:YES];
				}
			}
			if ([panel runModal] != NSModalResponseOK || panel.URL == nil) return;

			NSURL *url = panel.URL;
			NSError *bookmarkError = nil;
			NSData *bookmark = shed_bookmark_for_url(url, sandboxed, &bookmarkError);
			if (bookmark == nil) {
				failure = [[bookmarkError localizedDescription] copy];
				return;
			}
			if (!shed_set_active_scope(url, sandboxed, &failure)) return;
			selectedPath = [url.path copy];
			selectedBookmark = [[bookmark base64EncodedStringWithOptions:0] copy];
		}
	});
	if (selectedPath != nil && bookmarkOutput != NULL) *bookmarkOutput = shed_copy_string(selectedBookmark);
	if (failure != nil && errorOutput != NULL) *errorOutput = shed_copy_string(failure);
	char *result = shed_copy_string(selectedPath);
	[selectedPath release];
	[selectedBookmark release];
	[failure release];
	return result;
}

static char *shed_start_folder_scope(const char *bookmarkString, char **renewedBookmarkOutput, char **errorOutput) {
	__block NSString *resolvedPath = nil;
	__block NSString *renewedBookmark = nil;
	__block NSString *failure = nil;
	NSString *encoded = bookmarkString == NULL ? nil : [NSString stringWithUTF8String:bookmarkString];
	BOOL sandboxed = shed_is_sandboxed();
	dispatch_sync(dispatch_get_main_queue(), ^{
		@autoreleasepool {
			NSData *bookmark = [[NSData alloc] initWithBase64EncodedString:encoded options:0];
			if (bookmark == nil) {
				failure = [@"저장된 폴더 권한을 읽을 수 없습니다." copy];
				return;
			}
			BOOL stale = NO;
			NSError *resolveError = nil;
			NSURLBookmarkResolutionOptions options = sandboxed ? NSURLBookmarkResolutionWithSecurityScope : 0;
			NSURL *url = [NSURL URLByResolvingBookmarkData:bookmark options:options relativeToURL:nil bookmarkDataIsStale:&stale error:&resolveError];
			[bookmark release];
			if (url == nil) {
				failure = [[resolveError localizedDescription] copy];
				return;
			}
			if (!shed_set_active_scope(url, sandboxed, &failure)) return;
			resolvedPath = [url.path copy];
			if (stale) {
				NSError *renewalError = nil;
				NSData *renewed = shed_bookmark_for_url(url, sandboxed, &renewalError);
				if (renewed != nil) renewedBookmark = [[renewed base64EncodedStringWithOptions:0] copy];
			}
		}
	});
	if (renewedBookmark != nil && renewedBookmarkOutput != NULL) *renewedBookmarkOutput = shed_copy_string(renewedBookmark);
	if (failure != nil && errorOutput != NULL) *errorOutput = shed_copy_string(failure);
	char *result = shed_copy_string(resolvedPath);
	[resolvedPath release];
	[renewedBookmark release];
	[failure release];
	return result;
}

static void shed_stop_folder_scope(void) {
	dispatch_sync(dispatch_get_main_queue(), ^{
		@autoreleasepool { shed_clear_active_scope(); }
	});
}
*/
import "C"

import (
	"context"
	"errors"
	"unsafe"
)

func chooseFolder(_ context.Context, initial string) (string, string, error) {
	initialPath := C.CString(initial)
	defer C.free(unsafe.Pointer(initialPath))
	var bookmarkOutput *C.char
	var errorOutput *C.char
	pathOutput := C.shed_choose_folder(initialPath, &bookmarkOutput, &errorOutput)
	defer freeCString(pathOutput)
	defer freeCString(bookmarkOutput)
	defer freeCString(errorOutput)
	if message := goString(errorOutput); message != "" {
		return "", "", errors.New(message)
	}
	return goString(pathOutput), goString(bookmarkOutput), nil
}

func startFolderScope(bookmark string) (string, string, error) {
	bookmarkInput := C.CString(bookmark)
	defer C.free(unsafe.Pointer(bookmarkInput))
	var renewedOutput *C.char
	var errorOutput *C.char
	pathOutput := C.shed_start_folder_scope(bookmarkInput, &renewedOutput, &errorOutput)
	defer freeCString(pathOutput)
	defer freeCString(renewedOutput)
	defer freeCString(errorOutput)
	if message := goString(errorOutput); message != "" {
		return "", "", errors.New(message)
	}
	return goString(pathOutput), goString(renewedOutput), nil
}

func stopFolderScope() {
	C.shed_stop_folder_scope()
}

func goString(value *C.char) string {
	if value == nil {
		return ""
	}
	return C.GoString(value)
}

func freeCString(value *C.char) {
	if value != nil {
		C.free(unsafe.Pointer(value))
	}
}
