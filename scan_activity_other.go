//go:build !darwin

package main

func beginFolderMapActivity() func() {
	return func() {}
}
