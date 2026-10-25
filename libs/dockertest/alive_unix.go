//go:build unix

package dockertest

import (
	"errors"
	"syscall"
)

// alive reports whether process pid exists: signal 0 checks it without
// sending anything, and EPERM means it exists under another user.
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
