//go:build !windows

package studio

import "syscall"

// Opening non-regular files must never stall a backup. In particular, opening
// a FIFO read-only blocks until a writer arrives. The archive walk rejects the
// resulting descriptor after fstat; O_NONBLOCK makes reaching that check safe.
const archiveOpenExtraFlags = syscall.O_NONBLOCK
