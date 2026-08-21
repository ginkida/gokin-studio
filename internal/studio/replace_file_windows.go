//go:build windows

package studio

import "golang.org/x/sys/windows"

func replacePublishedFile(oldPath, newPath string) error {
	return windows.Rename(oldPath, newPath)
}

func publishNewFile(oldPath, newPath string) error {
	from, err := windows.UTF16PtrFromString(oldPath)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(newPath)
	if err != nil {
		return err
	}
	// MoveFileEx without REPLACE_EXISTING fails when newPath already exists,
	// which preserves every published backup. WRITE_THROUGH keeps publication
	// crash-durable after the archive itself has already been fsynced.
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}

func hideOpenRestoreReviewFile(string) bool {
	return false
}
