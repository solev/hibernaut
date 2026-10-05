//go:build !e2e

package e2e

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/engine"
)

const (
	memorySum = "1111111111111111111111111111111111111111111111111111111111111111"
	diskSum   = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
)

func TestSentinelCommands(t *testing.T) {
	for _, want := range []string{
		"head -c 64M /dev/urandom > /dev/shm/hib-sentinel",
		"dd if=/dev/urandom of=/home/user/hib-sentinel bs=1M count=64 iflag=fullblock conv=fsync status=none",
		"sha256sum /dev/shm/hib-sentinel /home/user/hib-sentinel",
	} {
		if !strings.Contains(writeSentinelsCommand, want) {
			t.Errorf("write command %q doesn't contain %q", writeSentinelsCommand, want)
		}
	}
	drop := strings.Index(readSentinelsCommand, "sync && echo 3 > /proc/sys/vm/drop_caches && ")
	sum := strings.Index(readSentinelsCommand, "sha256sum /dev/shm/hib-sentinel /home/user/hib-sentinel")
	if drop < 0 || sum < drop {
		t.Errorf("read command %q doesn't drop the page cache before reading the sentinels", readSentinelsCommand)
	}
}

func TestParseSentinelSums(t *testing.T) {
	for _, out := range []string{
		memorySum + "  /dev/shm/hib-sentinel\n" + diskSum + "  /home/user/hib-sentinel\n",
		memorySum + "  /dev/shm/hib-sentinel\n" + diskSum + "  /home/user/hib-sentinel",
		memorySum + " */dev/shm/hib-sentinel\n" + diskSum + " */home/user/hib-sentinel\n",
	} {
		got, err := parseSentinelSums(out)
		if err != nil || got != (sentinelSums{Memory: memorySum, Disk: diskSum}) {
			t.Errorf("sums = %+v, %v, want the memory and disk sums", got, err)
		}
	}
}

func TestParseSentinelSumsRefusesMalformedOutput(t *testing.T) {
	good := memorySum + "  /dev/shm/hib-sentinel\n" + diskSum + "  /home/user/hib-sentinel\n"
	for name, out := range map[string]string{
		"empty":        "",
		"one line":     memorySum + "  /dev/shm/hib-sentinel\n",
		"three lines":  good + diskSum + "  /home/user/other\n",
		"swapped":      diskSum + "  /home/user/hib-sentinel\n" + memorySum + "  /dev/shm/hib-sentinel\n",
		"short sum":    strings.Replace(good, memorySum, memorySum[1:], 1),
		"upper case":   strings.Replace(good, diskSum, strings.ToUpper(diskSum), 1),
		"other path":   strings.Replace(good, "/home/user/hib-sentinel", "/home/user/churn", 1),
		"missing file": strings.Replace(good, diskSum+"  ", "", 1),
		"extra field":  strings.Replace(good, "/dev/shm/hib-sentinel", "/dev/shm/hib-sentinel extra", 1),
		"escape":       "\x1b[31m" + good,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseSentinelSums(out); err == nil {
				t.Fatal("accepted malformed sentinel sums")
			} else if strings.ContainsAny(err.Error(), "\n\x1b") {
				t.Errorf("error prints raw output: %q", err)
			}
		})
	}
}

func TestSentinelMismatches(t *testing.T) {
	want := sentinelSums{Memory: memorySum, Disk: diskSum}
	if got := want.mismatches(want); len(got) != 0 {
		t.Errorf("matching sums report %q", got)
	}
	got := sentinelSums{Memory: diskSum, Disk: memorySum}.mismatches(want)
	if len(got) != 2 ||
		got[0] != "the memory sentinel's SHA-256 is "+diskSum+", want "+memorySum ||
		got[1] != "the disk sentinel's SHA-256 is "+memorySum+", want "+diskSum {
		t.Errorf("mismatches = %q, want both sentinels with both sums", got)
	}
}

type sentinelLog struct {
	fatal  string
	errors []string
}

func (*sentinelLog) Helper() {}

func (l *sentinelLog) Fatalf(format string, args ...any) {
	l.fatal = fmt.Sprintf(format, args...)
}

func (l *sentinelLog) Errorf(format string, args ...any) {
	l.errors = append(l.errors, fmt.Sprintf(format, args...))
}

func TestCleanupSentinelReadErrorEscapesEngineMessage(t *testing.T) {
	var log sentinelLog
	checkSentinelResult(&log, "e2e-machine", engine.ExecResult{}, errors.New("E2B: \x1b]52;c;ZXZpbA==\x07"), sentinelSums{})
	want := `reading the sentinels in e2e-machine: E2B: \x1b]52;c;ZXZpbA==\x07`
	if log.fatal != want || len(log.errors) != 0 {
		t.Fatalf("fatal = %q, errors = %q, want %q", log.fatal, log.errors, want)
	}
}

func TestCleanupSentinelResultReportsFailuresAndMatches(t *testing.T) {
	want := sentinelSums{Memory: memorySum, Disk: diskSum}
	for _, tc := range []struct {
		name   string
		result engine.ExecResult
		fatal  string
		errors []string
	}{
		{
			name:   "command failed",
			result: engine.ExecResult{ExitCode: 1, Stderr: []byte("\x1b]52;c;ZXZpbA==\x07")},
			fatal:  `reading the sentinels in e2e-machine exited 1: \x1b]52;c;ZXZpbA==\x07`,
		},
		{
			name:   "invalid sums",
			result: engine.ExecResult{Stdout: []byte("\x1b]52;c;ZXZpbA==\x07")},
			fatal:  "reading the sentinels in e2e-machine: sentinel sums must be 2 lines, got 1",
		},
		{
			name:   "matching",
			result: engine.ExecResult{Stdout: []byte(memorySum + "  " + memorySentinel + "\n" + diskSum + "  " + diskSentinel + "\n")},
		},
		{
			name:   "mismatch",
			result: engine.ExecResult{Stdout: []byte(diskSum + "  " + memorySentinel + "\n" + memorySum + "  " + diskSentinel + "\n")},
			errors: []string{
				"in e2e-machine, the memory sentinel's SHA-256 is " + diskSum + ", want " + memorySum,
				"in e2e-machine, the disk sentinel's SHA-256 is " + memorySum + ", want " + diskSum,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var log sentinelLog
			checkSentinelResult(&log, "e2e-machine", tc.result, nil, want)
			if log.fatal != tc.fatal || strings.Join(log.errors, "\n") != strings.Join(tc.errors, "\n") {
				t.Fatalf("fatal = %q, errors = %q, want %q, %q", log.fatal, log.errors, tc.fatal, tc.errors)
			}
		})
	}
}
