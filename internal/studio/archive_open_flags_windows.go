//go:build windows

package studio

// Windows named pipes cannot appear as ordinary filesystem entries beneath
// configDir in the same way as Unix FIFOs. os.Root still supplies reparse-point
// confinement and the post-open regular-file/identity checks.
const archiveOpenExtraFlags = 0
