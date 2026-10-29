package taskstore_test

import (
	"os"
	"syscall"
	"testing"
)

func TestMain(m *testing.M) {
	// TempDir inherits a 002 umask as group-writable, which statefile.Root rightly refuses.
	syscall.Umask(0o022)
	os.Exit(m.Run())
}
