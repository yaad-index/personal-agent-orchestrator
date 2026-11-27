// Package shell builds text for a POSIX shell.
package shell

import "strings"

// Quote quotes s as one word for a POSIX shell.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
