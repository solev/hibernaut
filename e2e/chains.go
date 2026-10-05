package e2e

import (
	"encoding/base64"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/solev/hibernaut/internal/cleanup"
	"github.com/solev/hibernaut/internal/hibapi"
)

var (
	cycleDigits      = regexp.MustCompile(`^[0-9]+$`)
	churnSize        = regexp.MustCompile(`^[0-9]+[KMG]?$`)
	buildUUID        = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	machineIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
)

type pauseCycleOptions struct {
	Cycles int
	Churn  string
}

func pauseCycleOptionsFromEnv(lookup func(string) (string, bool)) (pauseCycleOptions, error) {
	cycles, hasCycles := lookup("HIB_E2E_CYCLES")
	churn, hasChurn := lookup("HIB_E2E_CHURN")
	if !hasCycles {
		cycles = "20"
	}
	if !hasChurn {
		churn = "64M"
	}
	n, err := strconv.Atoi(cycles)
	if err != nil || !cycleDigits.MatchString(cycles) || n < 1 || n > 200 {
		return pauseCycleOptions{}, fmt.Errorf("HIB_E2E_CYCLES must be an integer from 1 to 200")
	}
	if !churnSize.MatchString(churn) {
		return pauseCycleOptions{}, fmt.Errorf("HIB_E2E_CHURN must be digits with an optional K, M or G suffix")
	}
	return pauseCycleOptions{Cycles: n, Churn: churn}, nil
}

func (o pauseCycleOptions) timeout() time.Duration {
	return time.Duration(2*o.Cycles+10) * time.Minute
}

func (o pauseCycleOptions) churnCommand() string {
	if o.Churn == "0" {
		return ""
	}
	return "head -c " + o.Churn + " /dev/urandom > /home/user/churn"
}

func snapshotQuery(id string) (string, error) {
	if !machineIDPattern.MatchString(id) {
		return "", fmt.Errorf("machine id isn't safe to use in a snapshot query")
	}
	return fmt.Sprintf("SELECT b.id, b.status_group FROM snapshots s "+
		"JOIN env_build_assignments a ON a.env_id = s.env_id JOIN env_builds b ON b.id = a.build_id "+
		"WHERE s.sandbox_id = '%s' GROUP BY b.id, b.status_group ORDER BY max(a.created_at)", id), nil
}

func parseSnapshotBuilds(out string) ([]string, bool, error) {
	var builds []string
	seen := map[string]bool{}
	done := true
	for line := range strings.Lines(out) {
		fields := strings.Split(strings.TrimSuffix(line, "\n"), "|")
		if len(fields) != 2 || !buildUUID.MatchString(fields[0]) || seen[fields[0]] {
			return nil, false, fmt.Errorf("invalid snapshot build listing")
		}
		switch fields[1] {
		case "ready", "failed":
		case "pending", "in_progress":
			done = false
		default:
			return nil, false, fmt.Errorf("snapshot build has an unknown status")
		}
		builds = append(builds, fields[0])
		seen[fields[0]] = true
	}
	return builds, done, nil
}

func checkedBuilds(builds []string) (map[string]bool, error) {
	own := map[string]bool{}
	for _, id := range builds {
		if !buildUUID.MatchString(id) || own[id] {
			return nil, fmt.Errorf("snapshot builds must be distinct UUIDs")
		}
		own[id] = true
	}
	return own, nil
}

func buildDirsCommand(builds []string) (string, error) {
	if _, err := checkedBuilds(builds); err != nil {
		return "", err
	}
	if len(builds) == 0 {
		return "", nil
	}
	return `sudo sh -c 'set -eu; cd /var/lib/e2b/local-template-storage
for b in ` + strings.Join(builds, " ") + `; do
	[ -d "$b" ] || continue
	du -sB1 -- "$b"
done'`, nil
}

func snapshotReadCommand(builds []string) (string, error) {
	if _, err := checkedBuilds(builds); err != nil {
		return "", err
	}
	if len(builds) == 0 {
		return "", fmt.Errorf("the machine has no snapshots")
	}
	return `sudo sh -c 'set -eu; cd /var/lib/e2b/local-template-storage
for b in ` + strings.Join(builds, " ") + `; do
	[ -d "$b" ] || continue
	printf "dir\t"
	du -sB1 -- "$b"
	for f in memfile.header rootfs.ext4.header; do
		printf "header\t%s\t%s\t" "$b" "$f"
		base64 -w0 -- "$b/$f"
		printf "\n"
	done
	if [ "$b" = "` + builds[len(builds)-1] + `" ]; then
		for f in memfile rootfs.ext4; do
			printf "body\t%s\t%s\t" "$b" "$f"
			if [ -f "$b/$f" ]; then
				stat -c "%b %B %s" -- "$b/$f"
			else
				printf "0 512 0\n"
			fi
		done
	fi
done'`, nil
}

func parseBuildDirs(out string, builds []string) (map[string]int64, error) {
	own, err := checkedBuilds(builds)
	if err != nil {
		return nil, err
	}
	dirs := map[string]int64{}
	for line := range strings.Lines(out) {
		fields := strings.Fields(line)
		if len(fields) != 2 || !own[fields[1]] {
			return nil, fmt.Errorf("invalid snapshot disk usage")
		}
		if _, duplicate := dirs[fields[1]]; duplicate {
			return nil, fmt.Errorf("duplicate snapshot disk usage")
		}
		n, err := parseDiskBytes(fields[0])
		if err != nil {
			return nil, err
		}
		dirs[fields[1]] = n
	}
	return dirs, nil
}

func parseDiskBytes(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || !cycleDigits.MatchString(s) {
		return 0, fmt.Errorf("invalid snapshot disk byte count")
	}
	return n, nil
}

type bodyStats struct {
	Allocated, Logical uint64
	Mappings           int
}

type snapshotFiles struct {
	Disk    int64
	Headers map[string]cleanup.HeaderStats
	Bodies  map[string]bodyStats
}

func parseBodyStats(out string) (bodyStats, error) {
	fields := strings.Fields(out)
	if len(fields) != 3 {
		return bodyStats{}, fmt.Errorf("invalid snapshot body stat")
	}
	var values [3]uint64
	for i, s := range fields {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil || !cycleDigits.MatchString(s) {
			return bodyStats{}, fmt.Errorf("invalid snapshot body byte count")
		}
		values[i] = n
	}
	if values[1] == 0 || values[0] > math.MaxUint64/values[1] {
		return bodyStats{}, fmt.Errorf("invalid snapshot body allocation")
	}
	return bodyStats{Allocated: values[0] * values[1], Logical: values[2]}, nil
}

func parseSnapshotFiles(out string, builds []string) (map[string]snapshotFiles, error) {
	own, err := checkedBuilds(builds)
	if err != nil {
		return nil, err
	}
	if len(builds) == 0 {
		return nil, fmt.Errorf("the machine has no snapshots")
	}
	latest := builds[len(builds)-1]
	files := map[string]snapshotFiles{}
	for line := range strings.Lines(out) {
		fields := strings.Split(strings.TrimSuffix(line, "\n"), "\t")
		if len(fields) == 3 && fields[0] == "dir" {
			if !own[fields[2]] {
				return nil, fmt.Errorf("snapshot directory isn't one of the machine's builds")
			}
			if _, exists := files[fields[2]]; exists {
				return nil, fmt.Errorf("duplicate snapshot directory")
			}
			disk, err := parseDiskBytes(fields[1])
			if err != nil {
				return nil, err
			}
			files[fields[2]] = snapshotFiles{Disk: disk, Headers: map[string]cleanup.HeaderStats{}, Bodies: map[string]bodyStats{}}
			continue
		}
		if len(fields) != 4 {
			return nil, fmt.Errorf("invalid snapshot file record")
		}
		file, exists := files[fields[1]]
		if !exists {
			return nil, fmt.Errorf("snapshot file has no directory listing")
		}
		switch fields[0] {
		case "header":
			if err := readSnapshotHeader(file.Headers, fields[1], fields[2], fields[3]); err != nil {
				return nil, err
			}
		case "body":
			if fields[1] != latest || fields[2] != "memfile" && fields[2] != "rootfs.ext4" {
				return nil, fmt.Errorf("unexpected snapshot body")
			}
			if _, duplicate := file.Bodies[fields[2]]; duplicate {
				return nil, fmt.Errorf("duplicate snapshot body")
			}
			body, err := parseBodyStats(fields[3])
			if err != nil {
				return nil, err
			}
			file.Bodies[fields[2]] = body
		default:
			return nil, fmt.Errorf("unknown snapshot file record")
		}
	}
	for id, file := range files {
		if len(file.Headers) != 2 || id == latest && len(file.Bodies) != 2 {
			return nil, fmt.Errorf("snapshot files are incomplete")
		}
	}
	if _, exists := files[latest]; !exists {
		return nil, fmt.Errorf("the latest snapshot directory is missing")
	}
	return files, nil
}

func readSnapshotHeader(headers map[string]cleanup.HeaderStats, id, name, encoded string) error {
	if name != "memfile.header" && name != "rootfs.ext4.header" {
		return fmt.Errorf("unexpected snapshot header")
	}
	if _, duplicate := headers[name]; duplicate {
		return fmt.Errorf("duplicate snapshot header")
	}
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("invalid snapshot header encoding")
	}
	stats, err := cleanup.ParseHeaderStats(b)
	if err != nil {
		return fmt.Errorf("snapshot %s %s: %w", id, name, err)
	}
	if stats.Build != id {
		return fmt.Errorf("snapshot header names a different build")
	}
	headers[name] = stats
	return nil
}

type sourceUsage struct {
	Builds int
	Bytes  uint64
}

type directoryUsage struct {
	Builds int
	Bytes  int64
}

type snapshotMetrics struct {
	Memory, Rootfs                     bodyStats
	MemoryNeeds, RootfsNeeds, OwnNeeds sourceUsage
	NeededDisk                         int64
	Kept, All                          directoryUsage
}

func measureSnapshot(latest string, files map[string]snapshotFiles) (snapshotMetrics, error) {
	file, exists := files[latest]
	if !exists {
		return snapshotMetrics{}, fmt.Errorf("the latest snapshot directory is missing")
	}
	mem, root := file.Headers["memfile.header"], file.Headers["rootfs.ext4.header"]
	memory, rootfs := file.Bodies["memfile"], file.Bodies["rootfs.ext4"]
	memory.Mappings, rootfs.Mappings = mem.Mappings, root.Mappings
	memorySources := ownSources(files, mem)
	rootfsSources := ownSources(files, root)
	both := ownSources(files, mem, root)
	directDisk := map[string]int64{}
	for id := range both {
		directDisk[id] = files[id].Disk
	}
	all := map[string]int64{}
	for id, file := range files {
		all[id] = file.Disk
	}
	kept, err := diskUsage(cleanupClosure(latest, files))
	if err != nil {
		return snapshotMetrics{}, err
	}
	direct, err := diskUsage(directDisk)
	if err != nil {
		return snapshotMetrics{}, err
	}
	total, err := diskUsage(all)
	if err != nil {
		return snapshotMetrics{}, err
	}
	return snapshotMetrics{
		Memory: memory, Rootfs: rootfs,
		MemoryNeeds: sourceTotals(memorySources), RootfsNeeds: sourceTotals(rootfsSources),
		OwnNeeds: sourceTotals(both), NeededDisk: direct.Bytes, Kept: kept, All: total,
	}, nil
}

func ownSources(files map[string]snapshotFiles, headers ...cleanup.HeaderStats) map[string]uint64 {
	sources := map[string]uint64{}
	for _, header := range headers {
		for id, n := range header.BytesByBuild {
			if _, exists := files[id]; exists && n > 0 {
				sources[id] += n
			}
		}
	}
	return sources
}

func sourceTotals(sources map[string]uint64) sourceUsage {
	usage := sourceUsage{Builds: len(sources)}
	for _, n := range sources {
		usage.Bytes += n
	}
	return usage
}

func cleanupClosure(latest string, files map[string]snapshotFiles) map[string]int64 {
	kept := map[string]int64{}
	todo := []string{latest}
	for len(todo) > 0 {
		id := todo[0]
		todo = todo[1:]
		if _, seen := kept[id]; seen {
			continue
		}
		file, exists := files[id]
		if !exists {
			continue
		}
		kept[id] = file.Disk
		for _, header := range file.Headers {
			todo = append(todo, header.Build, header.BaseBuild)
			for source := range header.BytesByBuild {
				todo = append(todo, source)
			}
		}
	}
	return kept
}

func diskUsage(dirs map[string]int64) (directoryUsage, error) {
	usage := directoryUsage{Builds: len(dirs)}
	for _, n := range dirs {
		if n < 0 || usage.Bytes > math.MaxInt64-n {
			return directoryUsage{}, fmt.Errorf("snapshot disk usage is out of range")
		}
		usage.Bytes += n
	}
	return usage, nil
}

type pauseCycle struct {
	Cycle                int
	Pause, Write, Resume time.Duration
	Snapshot             snapshotMetrics
}

func formatBytes(n uint64) string {
	if n < 1<<20 {
		return fmt.Sprintf("%d B", n)
	}
	return hibapi.FormatMB(int64(n >> 20))
}

func formatPauseCycle(c pauseCycle) string {
	s := c.Snapshot
	return fmt.Sprintf("cycle %2d: pause %v. write %v. resume %v. "+
		"memfile allocated %s, logical %s, %d mappings. rootfs.ext4 allocated %s, logical %s, %d mappings. "+
		"own needed memory %d/%s, rootfs %d/%s, both %d/%s mapped, %s on disk. "+
		"cleanup keeps %d/%s. snapshots %d/%s.",
		c.Cycle, c.Pause.Round(time.Millisecond), c.Write.Round(time.Millisecond), c.Resume.Round(time.Millisecond),
		formatBytes(s.Memory.Allocated), formatBytes(s.Memory.Logical), s.Memory.Mappings,
		formatBytes(s.Rootfs.Allocated), formatBytes(s.Rootfs.Logical), s.Rootfs.Mappings,
		s.MemoryNeeds.Builds, formatBytes(s.MemoryNeeds.Bytes), s.RootfsNeeds.Builds, formatBytes(s.RootfsNeeds.Bytes),
		s.OwnNeeds.Builds, formatBytes(s.OwnNeeds.Bytes), formatBytes(uint64(s.NeededDisk)),
		s.Kept.Builds, formatBytes(uint64(s.Kept.Bytes)), s.All.Builds, formatBytes(uint64(s.All.Bytes)))
}

func tailSlope(values []float64) float64 {
	values = values[max(0, len(values)-5):]
	if len(values) < 2 {
		return 0
	}
	var mean float64
	for _, y := range values {
		mean += y / float64(len(values))
	}
	mid := float64(len(values)-1) / 2
	var numerator, denominator float64
	for i, y := range values {
		x := float64(i) - mid
		numerator += x * (y - mean)
		denominator += x * x
	}
	return numerator / denominator
}

func pauseCycleSummary(options pauseCycleOptions, cycles []pauseCycle) string {
	first, last := cycles[0], cycles[len(cycles)-1]
	var mapped, kept []float64
	var slowest time.Duration
	for _, c := range cycles {
		mapped = append(mapped, float64(c.Snapshot.OwnNeeds.Bytes)/(1<<20))
		kept = append(kept, float64(c.Snapshot.Kept.Bytes)/(1<<20))
		slowest = max(slowest, c.Resume)
	}
	return fmt.Sprintf("pause cycles summary: cycles=%d churn=%s. own needed first %d/%s, last %d/%s. "+
		"cleanup kept first %d/%s, last %d/%s. resume first %v, last %v. "+
		"last %d slopes own mapped %+.2f MiB/cycle, kept disk %+.2f MiB/cycle. slowest resume %v.",
		options.Cycles, options.Churn,
		first.Snapshot.OwnNeeds.Builds, formatBytes(first.Snapshot.OwnNeeds.Bytes),
		last.Snapshot.OwnNeeds.Builds, formatBytes(last.Snapshot.OwnNeeds.Bytes),
		first.Snapshot.Kept.Builds, formatBytes(uint64(first.Snapshot.Kept.Bytes)),
		last.Snapshot.Kept.Builds, formatBytes(uint64(last.Snapshot.Kept.Bytes)),
		first.Resume.Round(time.Millisecond), last.Resume.Round(time.Millisecond),
		min(5, len(cycles)), tailSlope(mapped), tailSlope(kept), slowest.Round(time.Millisecond))
}
