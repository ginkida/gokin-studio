//go:build !windows

package studio

import "os"

func replacePublishedFile(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

func publishNewFile(oldPath, newPath string) error {
	return os.Link(oldPath, newPath)
}

func hideOpenRestoreReviewFile(path string) bool {
	return os.Remove(path) == nil
}
