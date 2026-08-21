//go:build !windows

package fileutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func durableReplace(oldPath, newPath string) error {
	return durableMove(oldPath, newPath)
}

func durableMove(oldPath, newPath string) error {
	if err := os.Rename(oldPath, newPath); err != nil {
		return err
	}

	oldDir := filepath.Dir(oldPath)
	newDir := filepath.Dir(newPath)
	if err := syncDirectory(newDir); err != nil {
		return fmt.Errorf("sync destination directory: %w", err)
	}
	if oldDir != newDir {
		if err := syncDirectory(oldDir); err != nil {
			return fmt.Errorf("sync source directory: %w", err)
		}
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	return errors.Join(syncErr, dir.Close())
}
