//go:build windows

package core

func diskFree(path string) (uint64, error) {
	return 0, nil
}
