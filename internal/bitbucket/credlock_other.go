//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly || windows)

package bitbucket

import "os"

// lockFile is a no-op on platforms without a supported advisory lock. Writes
// stay atomic (temp file + rename); only cross-process serialization of the
// read-modify-write is lost.
func lockFile(*os.File) error { return nil }

func unlockFile(*os.File) error { return nil }

// replaceFile atomically renames src over dst.
func replaceFile(src, dst string) error { return os.Rename(src, dst) }
