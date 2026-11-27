//go:build !linux

package workfile

import "os"

// openNoFollow opens path, already resolved through its links by Resolve.
func openNoFollow(path string) (*os.File, error) { return os.Open(path) }

// openedPath is not known here: the check before the open stands alone.
func openedPath(*os.File) (string, bool) { return "", false }
