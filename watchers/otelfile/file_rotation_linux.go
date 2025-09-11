//go:build linux
// +build linux

package otelfile

import (
	"os"
	"syscall"
)

func (w *OtelFileWatcher) checkInodeRotation(info, lastInfo os.FileInfo) bool {
	// Look at inode first - this is the most reliable method on Linux
	if newStat, ok := info.Sys().(*syscall.Stat_t); ok {
		if oldStat, ok2 := lastInfo.Sys().(*syscall.Stat_t); ok2 {
			if newStat.Ino != oldStat.Ino {
				w.log.Debug("file rotation detected - different inode",
					"path", w.path,
					"oldInode", oldStat.Ino,
					"newInode", newStat.Ino)
				return true
			}
		}
	}
	return false
}
