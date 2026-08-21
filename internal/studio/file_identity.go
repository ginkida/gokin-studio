package studio

import "os"

// sameOpenedFile reports whether the descriptor just opened is still the exact
// file that was stat'd a moment earlier — the second half of every
// stat-then-open check in this package.
//
// os.SameFile on its own compares device + inode, which is not sufficient on
// filesystems that recycle inode numbers eagerly. On ext4 a delete followed by
// a create in the same directory routinely hands the freed inode straight back,
// so a substituted file can present the identity of the one it replaced and
// pass the check. APFS does not reuse inodes that quickly, which is why the gap
// is invisible on macOS and only shows up on a Linux CI runner.
//
// Comparing size and modification time as well closes the window: a swap now
// has to reproduce device, inode, byte length, AND mtime to slip through.
// Callers use this against tampering, and every one of them takes its "before"
// stat microseconds earlier while holding the relevant lock, so a legitimate
// file is never rejected by the added strictness.
//
// Three stat-then-open sites deliberately keep bare os.SameFile and must not be
// converted:
//   - replay.go openReplayForTruncate opens with O_TRUNC, which zeroes size and
//     stamps mtime as part of the open itself, so the stricter comparison could
//     never hold.
//   - replay.go openReplayForAppend is a write path for our own log rather than
//     a tamper check on someone else's file.
//   - data_archive.go tolerates churn by design: it skips a file that moved
//     under the walk instead of failing, and events.log is appended to while the
//     backup runs, so comparing mtime would silently drop it from the archive.
func sameOpenedFile(before, after os.FileInfo) bool {
	if before == nil || after == nil {
		return false
	}
	return os.SameFile(before, after) &&
		before.Size() == after.Size() &&
		before.ModTime().Equal(after.ModTime())
}
