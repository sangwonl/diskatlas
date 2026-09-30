package core

import "testing"

func TestProtectedPaths(t *testing.T) {
	if !isProtected("/System/Library") {
		t.Fatal("system path must be protected")
	}
	if isProtected("/tmp/shed-fixture") {
		t.Fatal("temporary fixture must remain scannable")
	}
}
