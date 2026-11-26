//go:build !linux

package confine

import "errors"

var errNotLinux = errors.New("confining a command needs Linux user namespaces")

// IsInner is never true off Linux.
func IsInner() bool { return false }

// Inner is never reached off Linux.
func Inner() int { return 125 }

// Run cannot confine off Linux.
func Run(string, Spec) (int, error) { return 0, errNotLinux }

// Available reports that this host cannot confine a command.
func Available(string) error { return errNotLinux }
