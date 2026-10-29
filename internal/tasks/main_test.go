package tasks

import (
	"os"
	"syscall"
	"testing"
)

func TestMain(m *testing.M) {
	// t.TempDir makes directories with mode 0777 less the umask. Under Ubuntu's default of 002
	// they are group-writable, and the state directory checks rightly refuse them.
	syscall.Umask(0o022)
	os.Exit(m.Run())
}
