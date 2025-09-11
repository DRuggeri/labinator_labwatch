//go:build windows
// +build windows

package otelfile

import "os"

func (w *OtelFileWatcher) checkInodeRotation(info, lastInfo os.FileInfo) bool {
	// Windows doesn't have inodes, so we can't use this method
	return false
}
