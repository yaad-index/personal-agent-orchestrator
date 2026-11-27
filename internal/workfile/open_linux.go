//go:build linux

package workfile

import (
	"os"
	"strconv"
	"syscall"
)

func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}

// openedPath is where an open file really is, as the kernel names it.
func openedPath(f *os.File) (string, bool) {
	p, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(int(f.Fd())))
	return p, err == nil
}
