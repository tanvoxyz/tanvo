//go:build !unix

package dockertest

// alive cannot check other processes here, so it treats them all as
// running: their containers are left to the lifetime cap.
func alive(int) bool { return true }
