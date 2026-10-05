//go:build !e2e

package e2e

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/cleanup"
)

func TestPauseCycleOptions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		env    map[string]string
		cycles int
		churn  string
		limit  time.Duration
	}{
		{"defaults", nil, 20, "64M", 50 * time.Minute},
		{"idle", map[string]string{"HIB_E2E_CYCLES": "1", "HIB_E2E_CHURN": "0"}, 1, "0", 12 * time.Minute},
		{"maximum", map[string]string{"HIB_E2E_CYCLES": "200", "HIB_E2E_CHURN": "1G"}, 200, "1G", 410 * time.Minute},
		{"bytes", map[string]string{"HIB_E2E_CHURN": "4096"}, 20, "4096", 50 * time.Minute},
		{"kilobytes", map[string]string{"HIB_E2E_CHURN": "128K"}, 20, "128K", 50 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := map[string]int{}
			got, err := pauseCycleOptionsFromEnv(func(key string) (string, bool) {
				reads[key]++
				value, ok := tc.env[key]
				return value, ok
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.Cycles != tc.cycles || got.Churn != tc.churn || got.timeout() != tc.limit {
				t.Errorf("options = %+v, timeout %v, want %d cycles, %s churn, %v", got, got.timeout(), tc.cycles, tc.churn, tc.limit)
			}
			if reads["HIB_E2E_CYCLES"] != 1 || reads["HIB_E2E_CHURN"] != 1 {
				t.Errorf("environment reads = %v, want one per knob", reads)
			}
			wantCommand := "head -c " + tc.churn + " /dev/urandom > /home/user/churn"
			if tc.churn == "0" {
				wantCommand = ""
			}
			if got.churnCommand() != wantCommand {
				t.Errorf("churn command = %q, want %q", got.churnCommand(), wantCommand)
			}
		})
	}
}

func TestPauseCycleOptionsRefuseUnsafeValues(t *testing.T) {
	for key, values := range map[string][]string{
		"HIB_E2E_CYCLES": {"", "0", "201", "-1", "+1", "1.5", " 20", "20\n", "999999999999999999999"},
		"HIB_E2E_CHURN":  {"", "-1", "+1", "1.5M", "1m", "1T", "1MB", " 64M", "1M\n", "1; echo bad", "$(echo bad)"},
	} {
		for _, value := range values {
			t.Run(key+"="+value, func(t *testing.T) {
				_, err := pauseCycleOptionsFromEnv(func(name string) (string, bool) {
					return value, name == key
				})
				if err == nil || !strings.Contains(err.Error(), key) {
					t.Fatalf("error = %v, want a clear error for %s", err, key)
				}
				if strings.ContainsAny(err.Error(), "\n\x1b") {
					t.Errorf("error contains raw input: %q", err)
				}
			})
		}
	}
}

const (
	chainA = "11111111-1111-4111-8111-111111111111"
	chainB = "22222222-2222-4222-8222-222222222222"
	chainC = "33333333-3333-4333-8333-333333333333"
	image  = "44444444-4444-4444-8444-444444444444"
)

func TestSnapshotCommandsCheckHostIDs(t *testing.T) {
	for _, builds := range [][]string{{chainA, chainB}, nil} {
		command, err := buildDirsCommand(builds)
		if err != nil {
			t.Fatal(err)
		}
		if len(builds) == 0 {
			if command != "" {
				t.Errorf("empty directory command = %q, want none", command)
			}
			continue
		}
		if !strings.Contains(command, "for b in "+chainA+" "+chainB) || !strings.Contains(command, `du -sB1 -- "$b"`) {
			t.Errorf("directory command = %q, want a checked build list and byte counts", command)
		}
		command, err = snapshotReadCommand(builds)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`base64 -w0`, `stat -c "%b %B %s"`, `memfile.header rootfs.ext4.header`, `0 512 0\n`} {
			if !strings.Contains(command, want) {
				t.Errorf("snapshot command doesn't contain %q", want)
			}
		}
	}
	for _, invalid := range []string{"", "../build", chainA + ";echo bad", chainA + "\n", "$(echo bad)", "'"} {
		for _, command := range []func([]string) (string, error){buildDirsCommand, snapshotReadCommand} {
			if got, err := command([]string{invalid}); err == nil || got != "" {
				t.Errorf("command accepted an unsafe UUID")
			}
		}
	}
	if _, err := snapshotReadCommand(nil); err == nil {
		t.Fatal("snapshot command accepted an empty build list")
	}
	if _, err := snapshotQuery("sandbox'$(echo bad)"); err == nil {
		t.Fatal("query accepted an unsafe machine ID")
	}
	if query, err := snapshotQuery("sbox123abc"); err != nil || !strings.Contains(query, "s.sandbox_id = 'sbox123abc'") {
		t.Errorf("snapshot query = %q, %v", query, err)
	}
}

func TestParseSnapshotBuilds(t *testing.T) {
	out := chainA + "|ready\n" + chainB + "|in_progress\n"
	builds, done, err := parseSnapshotBuilds(out)
	if err != nil || done || !slices.Equal(builds, []string{chainA, chainB}) {
		t.Fatalf("builds = %q, done %v, error %v, want two builds still writing", builds, done, err)
	}
	for _, status := range []string{"ready", "failed"} {
		_, done, err := parseSnapshotBuilds(chainA + "|" + status)
		if err != nil || !done {
			t.Errorf("finished status %s: done %v, error %v", status, done, err)
		}
	}
	for _, out := range []string{chainA + "|pending", ""} {
		_, _, err := parseSnapshotBuilds(out)
		if err != nil {
			t.Error(err)
		}
	}
	for _, out := range []string{chainA, "unsafe|ready", chainA + "|unknown", chainA + "|ready|extra", chainA + "|ready\n" + chainA + "|ready"} {
		if _, _, err := parseSnapshotBuilds(out); err == nil {
			t.Error("accepted malformed snapshot listing")
		}
	}
}

func TestParseBuildDirs(t *testing.T) {
	got, err := parseBuildDirs("1048576\t"+chainA+"\n2097152\t"+chainC+"\n", []string{chainA, chainB, chainC})
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(got, map[string]int64{chainA: 1048576, chainC: 2097152}) {
		t.Errorf("directories = %v, want the two surviving builds", got)
	}
	got, err = parseBuildDirs("", []string{chainA})
	if err != nil || len(got) != 0 {
		t.Errorf("gone directory = %v, %v, want none", got, err)
	}
	for _, out := range []string{
		"bad", "-1\t" + chainA, "+1\t" + chainA, "1\tunsafe", "1\t" + chainB,
		"9223372036854775808\t" + chainA, "1\t" + chainA + "\n1\t" + chainA, "1\t" + chainA + "\textra",
	} {
		if _, err := parseBuildDirs(out, []string{chainA}); err == nil {
			t.Error("accepted malformed disk usage")
		}
	}
}

func wireHeader(t *testing.T, build, base string, mapped map[string]uint64) string {
	t.Helper()
	header := make([]byte, 64+40*len(mapped))
	for i, n := range []uint64{3, 4096, 1 << 30, 9} {
		binary.LittleEndian.PutUint64(header[i*8:], n)
	}
	putID := func(dst []byte, id string) {
		t.Helper()
		if id == "" {
			return
		}
		b, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
		if err != nil {
			t.Fatal(err)
		}
		copy(dst, b)
	}
	putID(header[32:48], build)
	putID(header[48:64], base)
	for i, id := range slices.Sorted(maps.Keys(mapped)) {
		m := header[64+i*40:]
		binary.LittleEndian.PutUint64(m[8:], mapped[id])
		putID(m[16:32], id)
	}
	return base64.StdEncoding.EncodeToString(header)
}

func snapshotWire(t *testing.T) string {
	t.Helper()
	var lines []string
	for _, build := range []string{chainA, chainC} {
		lines = append(lines, "dir\t1048576\t"+build)
		for _, name := range cleanup.Headers {
			lines = append(lines, fmt.Sprintf("header\t%s\t%s\t%s", build, name,
				wireHeader(t, build, image, map[string]uint64{build: 4096, "": 4096})))
		}
	}
	return strings.Join(lines, "\n") + "\nbody\t" + chainC + "\tmemfile\t8 512 8192\nbody\t" + chainC + "\trootfs.ext4\t0 512 0\n"
}

func TestParseSnapshotFiles(t *testing.T) {
	files, err := parseSnapshotFiles(snapshotWire(t), []string{chainA, chainB, chainC})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("read %d builds, want two surviving directories", len(files))
	}
	got := files[chainC]
	if got.Disk != 1048576 || got.Headers["memfile.header"].Mappings != 2 {
		t.Errorf("latest stats = %+v, want 1 MiB on disk and 2 mappings", got)
	}
	if got.Bodies["memfile"].Allocated != 4096 || got.Bodies["memfile"].Logical != 8192 {
		t.Errorf("memory body = %+v, want 4096 allocated and 8192 logical bytes", got.Bodies["memfile"])
	}
	if got.Bodies["rootfs.ext4"].Allocated != 0 || got.Bodies["rootfs.ext4"].Logical != 0 {
		t.Errorf("omitted rootfs body = %+v, want zero sizes", got.Bodies["rootfs.ext4"])
	}
	if !maps.Equal(got.Headers["memfile.header"].BytesByBuild, map[string]uint64{chainC: 4096}) {
		t.Error("mapped bytes don't exclude nil ranges")
	}
}

func TestParseSnapshotFilesRefusesMalformedOutput(t *testing.T) {
	wire := snapshotWire(t)
	for name, out := range map[string]string{
		"empty":                 "",
		"missing latest":        strings.ReplaceAll(wire, chainC, chainB),
		"unexpected build":      wire + "dir\t0\t" + image + "\n",
		"duplicate directory":   wire + "dir\t0\t" + chainA + "\n",
		"invalid base64":        strings.Replace(wire, wireHeader(t, chainA, image, map[string]uint64{chainA: 4096, "": 4096}), "!", 1),
		"missing header":        strings.Replace(wire, "\tmemfile.header\t", "\tother.header\t", 1),
		"wrong header build":    strings.Replace(wire, wireHeader(t, chainA, image, map[string]uint64{chainA: 4096, "": 4096}), wireHeader(t, image, image, nil), 1),
		"missing body":          strings.Split(wire, "\nbody\t")[0],
		"duplicate body":        wire + "body\t" + chainC + "\tmemfile\t0 512 0\n",
		"negative blocks":       strings.Replace(wire, "8 512 8192", "-8 512 8192", 1),
		"allocation overflow":   strings.Replace(wire, "8 512 8192", "18446744073709551615 512 8192", 1),
		"invalid logical size":  strings.Replace(wire, "8 512 8192", "8 512 x", 1),
		"extra stat field":      strings.Replace(wire, "8 512 8192", "8 512 8192 1", 1),
		"unknown record":        wire + "unsafe\t\x1b[31m\n",
		"unsafe directory ID":   strings.ReplaceAll(wire, chainA, "../bad"),
		"header before listing": "header\t" + chainA + "\tmemfile.header\t!\n" + wire,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseSnapshotFiles(out, []string{chainA, chainC}); err == nil {
				t.Fatal("accepted malformed snapshot stats")
			} else if strings.ContainsAny(err.Error(), "\n\x1b") {
				t.Errorf("error prints raw host output: %q", err)
			}
		})
	}
}

func TestSnapshotMetricsSeparateMappedSourcesFromCleanup(t *testing.T) {
	files := map[string]snapshotFiles{
		chainA: {
			Disk: 1 << 20,
			Headers: map[string]cleanup.HeaderStats{
				"memfile.header": {Build: chainA, BaseBuild: image, BytesByBuild: map[string]uint64{chainB: 10}},
			},
		},
		chainB: {
			Disk: 2 << 20,
			Headers: map[string]cleanup.HeaderStats{
				"rootfs.ext4.header": {Build: chainB, BytesByBuild: map[string]uint64{chainA: 20}},
			},
		},
		chainC: {
			Disk: 3 << 20,
			Headers: map[string]cleanup.HeaderStats{
				"memfile.header":     {Build: chainC, BaseBuild: image, Mappings: 4, BytesByBuild: map[string]uint64{chainA: 100, chainC: 200, image: 400}},
				"rootfs.ext4.header": {Build: chainC, BaseBuild: image, Mappings: 2, BytesByBuild: map[string]uint64{chainC: 300, image: 700}},
			},
			Bodies: map[string]bodyStats{
				"memfile":     {Allocated: 64 << 20, Logical: 80 << 20},
				"rootfs.ext4": {Allocated: 8 << 20, Logical: 16 << 20},
			},
		},
		"55555555-5555-4555-8555-555555555555": {Disk: 4 << 20},
	}
	got, err := measureSnapshot(chainC, files)
	if err != nil {
		t.Fatal(err)
	}
	if got.MemoryNeeds != (sourceUsage{Builds: 2, Bytes: 300}) ||
		got.RootfsNeeds != (sourceUsage{Builds: 1, Bytes: 300}) ||
		got.OwnNeeds != (sourceUsage{Builds: 2, Bytes: 600}) {
		t.Errorf("own sources = %+v, want memory 2/300 B, rootfs 1/300 B, union 2/600 B", got)
	}
	if got.NeededDisk != 4<<20 || got.Kept != (directoryUsage{Builds: 3, Bytes: 6 << 20}) || got.All != (directoryUsage{Builds: 4, Bytes: 10 << 20}) {
		t.Errorf("disk usage = %+v, want direct 4 MiB, closure 3/6 MiB, all 4/10 MiB", got)
	}
	if got.Memory.Mappings != 4 || got.Rootfs.Mappings != 2 || got.Memory.Logical != 80<<20 {
		t.Errorf("body stats = %+v, want header mapping counts and body logical sizes", got)
	}
	line := formatPauseCycle(pauseCycle{Cycle: 3, Pause: 2 * time.Second, Write: 5 * time.Second, Resume: 1100 * time.Millisecond, Snapshot: got})
	want := "cycle  3: pause 2s. write 5s. resume 1.1s. " +
		"memfile allocated 64 MiB, logical 80 MiB, 4 mappings. rootfs.ext4 allocated 8 MiB, logical 16 MiB, 2 mappings. " +
		"own needed memory 2/300 B, rootfs 1/300 B, both 2/600 B mapped, 4 MiB on disk. " +
		"cleanup keeps 3/6 MiB. snapshots 4/10 MiB."
	if line != want {
		t.Errorf("cycle line = %q, want %q", line, want)
	}
}

func TestCleanupClosureKeepsMetadataAndSkipsGoneBuilds(t *testing.T) {
	files := map[string]snapshotFiles{
		chainC: {
			Disk: 3 << 20,
			Headers: map[string]cleanup.HeaderStats{
				"memfile.header": {Build: chainC, BaseBuild: chainA, BytesByBuild: map[string]uint64{chainB: 100, image: 200}},
			},
		},
		chainA: {
			Disk: 1 << 20,
			Headers: map[string]cleanup.HeaderStats{
				"memfile.header": {Build: chainA, BaseBuild: chainC},
			},
		},
	}
	got, err := measureSnapshot(chainC, files)
	if err != nil {
		t.Fatal(err)
	}
	if got.OwnNeeds != (sourceUsage{}) || got.Kept != (directoryUsage{Builds: 2, Bytes: 4 << 20}) {
		t.Errorf("metrics = %+v, want no surviving direct sources but two metadata-retained builds", got)
	}
	if _, err := measureSnapshot(chainB, files); err == nil {
		t.Error("accepted a missing latest build")
	}
}

func TestTailSlope(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []float64
		want   float64
	}{
		{"none", nil, 0},
		{"one", []float64{10}, 0},
		{"two", []float64{10, 12}, 2},
		{"flat", []float64{50, 50, 50, 50, 50}, 0},
		{"falling", []float64{50, 40, 30, 20, 10}, -10},
		{"last five least squares", []float64{999, 10, 30, 20, 60, 40}, 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tailSlope(tc.values); got != tc.want {
				t.Errorf("slope = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPauseCycleSummary(t *testing.T) {
	var cycles []pauseCycle
	for i, resume := range []time.Duration{900 * time.Millisecond, 1100 * time.Millisecond, 1500 * time.Millisecond, time.Second, 1200 * time.Millisecond, 1300 * time.Millisecond} {
		cycles = append(cycles, pauseCycle{
			Cycle: i + 1, Resume: resume,
			Snapshot: snapshotMetrics{
				OwnNeeds: sourceUsage{Builds: i + 1, Bytes: uint64(i+1) * 64 << 20},
				Kept:     directoryUsage{Builds: i + 1, Bytes: int64(i+1) * 280 << 20},
			},
		})
	}
	got := pauseCycleSummary(pauseCycleOptions{Cycles: 6, Churn: "64M"}, cycles)
	want := "pause cycles summary: cycles=6 churn=64M. own needed first 1/64 MiB, last 6/384 MiB. " +
		"cleanup kept first 1/280 MiB, last 6/1.6 GiB. resume first 900ms, last 1.3s. " +
		"last 5 slopes own mapped +64.00 MiB/cycle, kept disk +280.00 MiB/cycle. slowest resume 1.5s."
	if got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
	got = pauseCycleSummary(pauseCycleOptions{Cycles: 1, Churn: "0"}, cycles[:1])
	if !strings.Contains(got, "last 1 slopes own mapped +0.00 MiB/cycle, kept disk +0.00 MiB/cycle") ||
		!strings.Contains(got, "slowest resume 900ms") || strings.ContainsAny(got, "\n\r") {
		t.Errorf("single-cycle summary = %q, want finite slopes and one line", got)
	}
}

func TestDiskUsageRefusesOverflow(t *testing.T) {
	for _, dirs := range []map[string]int64{
		{chainA: -1},
		{chainA: math.MaxInt64, chainB: 1},
	} {
		if _, err := diskUsage(dirs); err == nil {
			t.Fatal("accepted a disk total outside the nonnegative int64 range")
		}
	}
	if got, err := diskUsage(nil); err != nil || got != (directoryUsage{}) {
		t.Errorf("empty usage = %+v, %v, want zero", got, err)
	}
}
