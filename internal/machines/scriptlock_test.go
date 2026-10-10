package machines

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSetupScriptsTakeSharedLockBeforeWriting(t *testing.T) {
	for _, tc := range []struct {
		name, script string
	}{
		{"git", gitScript},
		{"env CAS", casScript},
		{"hosts CAS", hostsFile.script},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			for name, script := range map[string]string{
				"flock":     "#!/bin/sh\nprintf '%s' \"$1\" > \"$LOCK_LOG\"\nexit 83\n",
				"git":       "#!/bin/sh\nexit 0\n",
				"sha256sum": "#!/bin/sh\nexit 84\n",
				"awk":       "#!/bin/sh\nexit 85\n",
			} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			lock := filepath.Join(dir, "setup.lock")
			log := filepath.Join(dir, "lock.log")
			cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", tc.script, "hibd-test", "first", "second", lock)
			cmd.Env = []string{"PATH=" + bin, "LOCK_LOG=" + log}
			err := cmd.Run()
			exit, ok := errors.AsType[*exec.ExitError](err)
			if !ok || exit.ExitCode() != 83 {
				t.Fatalf("script didn't stop at the lock: %v", err)
			}
			if _, err := os.Stat(lock); err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(log); err != nil || string(got) != "9" {
				t.Errorf("flock didn't receive descriptor 9")
			}
		})
	}
}
