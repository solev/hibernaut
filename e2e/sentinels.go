package e2e

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	memorySentinel = "/dev/shm/hib-sentinel"
	diskSentinel   = "/home/user/hib-sentinel"
	sentinelPaths  = memorySentinel + " " + diskSentinel

	writeSentinelsCommand = "head -c 64M /dev/urandom > " + memorySentinel +
		" && dd if=/dev/urandom of=" + diskSentinel + " bs=1M count=64 iflag=fullblock conv=fsync status=none" +
		" && sha256sum " + sentinelPaths
	readSentinelsCommand = "sync && echo 3 > /proc/sys/vm/drop_caches && sha256sum " + sentinelPaths
)

var sentinelSum = regexp.MustCompile(`^([0-9a-f]{64}) [ *](\S+)$`)

type sentinelSums struct {
	Memory, Disk string
}

func parseSentinelSums(out string) (sentinelSums, error) {
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	paths := [...]string{memorySentinel, diskSentinel}
	if len(lines) != len(paths) {
		return sentinelSums{}, fmt.Errorf("sentinel sums must be %d lines, got %d", len(paths), len(lines))
	}
	var sums [len(paths)]string
	for i, line := range lines {
		match := sentinelSum.FindStringSubmatch(line)
		if match == nil || match[2] != paths[i] {
			return sentinelSums{}, fmt.Errorf("line %d isn't the SHA-256 of %s", i+1, paths[i])
		}
		sums[i] = match[1]
	}
	return sentinelSums{Memory: sums[0], Disk: sums[1]}, nil
}

func (s sentinelSums) mismatches(want sentinelSums) []string {
	var problems []string
	if s.Memory != want.Memory {
		problems = append(problems, fmt.Sprintf("the memory sentinel's SHA-256 is %s, want %s", s.Memory, want.Memory))
	}
	if s.Disk != want.Disk {
		problems = append(problems, fmt.Sprintf("the disk sentinel's SHA-256 is %s, want %s", s.Disk, want.Disk))
	}
	return problems
}
