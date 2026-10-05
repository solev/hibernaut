package e2e

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/solev/hibernaut/internal/cleanup"
)

// ext4 spends a block on each directory, and a 4 KiB extent block on every 340 ranges a punched
// file keeps. 8 MiB covers both for over 680,000 kept ranges.
const trimSlack = 8 << 20

var (
	snapshotBodies = [...]string{"memfile", "rootfs.ext4"}
	storedFiles    = slices.Concat(snapshotBodies[:], snapshotSidecars[:])

	templateAlias = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)
	sha256Hex     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	wouldTrimLine = regexp.MustCompile(`^would trim ([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}) (?:memfile|rootfs\.ext4): `)
)

func templateQuery(alias string) (string, error) {
	if !templateAlias.MatchString(alias) {
		return "", fmt.Errorf("template alias isn't safe to use in a query")
	}
	return fmt.Sprintf("SELECT b.id, b.status_group FROM env_aliases al "+
		"JOIN env_build_assignments a ON a.env_id = al.env_id JOIN env_builds b ON b.id = a.build_id "+
		"WHERE al.alias = '%s' GROUP BY b.id, b.status_group ORDER BY max(a.created_at)", alias), nil
}

type storedFile struct {
	Size, Allocated uint64
	Sum             string
}

type storedBuild struct {
	Disk    int64
	Files   map[string]storedFile
	Headers map[string]cleanup.HeaderStats
}

type storageRecord struct {
	Block  uint64
	Builds map[string]storedBuild
}

func storageRecordCommand(builds []string) (string, error) {
	if _, err := checkedBuilds(builds); err != nil {
		return "", err
	}
	if len(builds) == 0 {
		return "", fmt.Errorf("no snapshot builds to record")
	}
	return `sudo sh -c 'set -eu; cd /var/lib/e2b/local-template-storage
printf "block\t%s\n" "$(stat -f -c %S .)"
for b in ` + strings.Join(builds, " ") + `; do
	[ -d "$b" ] || continue
	printf "dir\t"
	du -sB1 -- "$b"
	for f in ` + strings.Join(snapshotBodies[:], " ") + `; do
		[ -f "$b/$f" ] || continue
		printf "file\t%s\t%s\t" "$b" "$f"
		stat -c "%b %B %s" -- "$b/$f"
	done
	for f in ` + strings.Join(snapshotSidecars[:], " ") + `; do
		[ -f "$b/$f" ] || continue
		printf "file\t%s\t%s\t" "$b" "$f"
		stat -c "%b %B %s" -- "$b/$f"
		printf "sum\t%s\t%s\t" "$b" "$f"
		sha256sum < "$b/$f" | cut -c1-64
		case "$f" in
		*.header)
			printf "header\t%s\t%s\t" "$b" "$f"
			base64 -w0 -- "$b/$f"
			printf "\n"
			;;
		esac
	done
done'`, nil
}

func parseStorageRecord(out string, builds []string) (storageRecord, error) {
	own, err := checkedBuilds(builds)
	if err != nil {
		return storageRecord{}, err
	}
	record := storageRecord{Builds: map[string]storedBuild{}}
	for line := range strings.Lines(out) {
		fields := strings.Split(strings.TrimSuffix(line, "\n"), "\t")
		switch {
		case len(fields) == 2 && fields[0] == "block":
			err = record.readBlock(fields[1])
		case len(fields) == 3 && fields[0] == "dir":
			err = record.readDir(own, fields[2], fields[1])
		case len(fields) == 4:
			err = record.readFile(fields)
		default:
			err = fmt.Errorf("invalid storage record")
		}
		if err != nil {
			return storageRecord{}, err
		}
	}
	if record.Block == 0 {
		return storageRecord{}, fmt.Errorf("the storage record has no block size")
	}
	for _, build := range record.Builds {
		if !build.recorded() {
			return storageRecord{}, fmt.Errorf("snapshot file records are incomplete")
		}
	}
	return record, nil
}

func (r *storageRecord) readBlock(s string) error {
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || !cycleDigits.MatchString(s) || n == 0 || n&(n-1) != 0 || r.Block != 0 {
		return fmt.Errorf("invalid or repeated storage block size")
	}
	r.Block = n
	return nil
}

func (r *storageRecord) readDir(own map[string]bool, id, disk string) error {
	if !own[id] {
		return fmt.Errorf("snapshot directory isn't one of the recorded builds")
	}
	if _, exists := r.Builds[id]; exists {
		return fmt.Errorf("duplicate snapshot directory")
	}
	n, err := parseDiskBytes(disk)
	if err != nil {
		return err
	}
	r.Builds[id] = storedBuild{Disk: n, Files: map[string]storedFile{}, Headers: map[string]cleanup.HeaderStats{}}
	return nil
}

func (r *storageRecord) readFile(fields []string) error {
	kind, id, name, value := fields[0], fields[1], fields[2], fields[3]
	build, exists := r.Builds[id]
	if !exists {
		return fmt.Errorf("snapshot file has no directory listing")
	}
	file, recorded := build.Files[name]
	switch kind {
	case "file":
		if !slices.Contains(storedFiles, name) || recorded {
			return fmt.Errorf("unexpected or duplicate snapshot file")
		}
		stat, err := parseBodyStats(value)
		if err != nil {
			return err
		}
		build.Files[name] = storedFile{Size: stat.Logical, Allocated: stat.Allocated}
	case "sum":
		if !recorded || !slices.Contains(snapshotSidecars[:], name) || file.Sum != "" {
			return fmt.Errorf("snapshot SHA-256 isn't of a recorded sidecar, or is repeated")
		}
		if !sha256Hex.MatchString(value) {
			return fmt.Errorf("invalid snapshot file SHA-256")
		}
		file.Sum = value
		build.Files[name] = file
	case "header":
		if !recorded {
			return fmt.Errorf("snapshot header has no file record")
		}
		return readSnapshotHeader(build.Headers, id, name, value)
	default:
		return fmt.Errorf("unknown storage record")
	}
	return nil
}

func (b storedBuild) recorded() bool {
	for name, file := range b.Files {
		if slices.Contains(snapshotSidecars[:], name) && file.Sum == "" {
			return false
		}
	}
	for _, name := range cleanup.Headers {
		if _, exists := b.Files[name]; exists {
			if _, read := b.Headers[name]; !read {
				return false
			}
		}
	}
	return true
}

func (r storageRecord) missingFiles(builds []string) []string {
	var missing []string
	for _, id := range builds {
		build, exists := r.Builds[id]
		if !exists {
			missing = append(missing, id+" has no directory")
			continue
		}
		for _, name := range storedFiles {
			if _, exists := build.Files[name]; exists {
				continue
			}
			if !slices.Contains(snapshotBodies[:], name) {
				missing = append(missing, fmt.Sprintf("%s has no %s", id, name))
			} else if n := build.Headers[name+".header"].BytesByBuild[id]; n > 0 {
				missing = append(missing, fmt.Sprintf("%s has no %s, though its %s.header maps %d bytes to it", id, name, name, n))
			}
		}
	}
	return missing
}

func (r storageRecord) disk(builds []string) int64 {
	var total int64
	for _, id := range builds {
		total += r.Builds[id].Disk
	}
	return total
}

func (r storageRecord) snapshotFiles(builds []string) map[string]snapshotFiles {
	files := map[string]snapshotFiles{}
	for _, id := range builds {
		build, exists := r.Builds[id]
		if !exists {
			continue
		}
		file := snapshotFiles{Disk: build.Disk, Headers: build.Headers, Bodies: map[string]bodyStats{}, Overhead: map[string]uint64{}}
		for name, stored := range build.Files {
			if slices.Contains(snapshotBodies[:], name) {
				file.Bodies[name] = bodyStats{Allocated: stored.Allocated, Logical: stored.Size}
			} else {
				file.Overhead[name] = stored.Allocated
			}
		}
		files[id] = file
	}
	return files
}

func (r storageRecord) reads(latest string, builds []string) map[string]bool {
	read := map[string]bool{}
	for _, header := range r.Builds[latest].Headers {
		for _, id := range builds {
			if header.BaseBuild == id || header.BytesByBuild[id] > 0 {
				read[id] = true
			}
		}
	}
	return read
}

func wouldTrimAny(out string, builds []string) bool {
	for line := range strings.Lines(out) {
		match := wouldTrimLine.FindStringSubmatch(strings.TrimSuffix(line, "\n"))
		if match != nil && slices.Contains(builds, match[1]) {
			return true
		}
	}
	return false
}

func distinctBuilds(lists ...[]string) []string {
	var builds []string
	for _, id := range slices.Concat(lists...) {
		if !slices.Contains(builds, id) {
			builds = append(builds, id)
		}
	}
	return builds
}

type trimSubjects struct {
	A, B  []string
	Image string
}

type trimLimit struct {
	Mapped, Sidecars, Block, Slack uint64
	Files, Dirs                    int
}

func (l trimLimit) rounding() uint64 {
	return 2 * l.Block * uint64(l.Files)
}

func (l trimLimit) total() uint64 {
	return l.Mapped + l.Sidecars + l.rounding() + l.Slack
}

func (l trimLimit) String() string {
	return fmt.Sprintf("%s (%d bytes): %d mapped by A's latest snapshot, %d of headers, snapfiles and metadata, "+
		"%d of rounding (two %d-byte blocks for each of %d files in %d directories) and %d of slack",
		formatBytes(l.total()), l.total(), l.Mapped, l.Sidecars, l.rounding(), l.Block, l.Files, l.Dirs, l.Slack)
}

func diskBytes(n int64) string {
	return fmt.Sprintf("%s (%d bytes)", formatBytes(uint64(max(n, 0))), n)
}

func trimLimitFor(before, after storageRecord, a []string) (trimLimit, error) {
	if len(a) == 0 {
		return trimLimit{}, fmt.Errorf("machine A has no snapshots")
	}
	metrics, err := measureSnapshot(a[len(a)-1], before.snapshotFiles(a))
	if err != nil {
		return trimLimit{}, err
	}
	limit := trimLimit{Mapped: metrics.OwnNeeds.Bytes, Block: after.Block, Slack: trimSlack}
	for _, id := range a {
		build, exists := after.Builds[id]
		if !exists {
			continue
		}
		limit.Dirs++
		limit.Files += len(build.Files)
		for _, name := range snapshotSidecars {
			limit.Sidecars += build.Files[name].Allocated
		}
	}
	return limit, nil
}

func trimProblems(before, after storageRecord, s trimSubjects) ([]string, error) {
	if len(s.B) == 0 {
		return nil, fmt.Errorf("machine B has no snapshots")
	}
	limit, err := trimLimitFor(before, after, s.A)
	if err != nil {
		return nil, err
	}
	var problems []string
	was, is := before.disk(s.A), after.disk(s.A)
	if 2*is >= was {
		problems = append(problems, fmt.Sprintf("A's build directories take %s after the trim, want less than half of the %s they took after the pauses",
			diskBytes(is), diskBytes(was)))
	}
	if is > 0 && uint64(is) > limit.total() {
		problems = append(problems, fmt.Sprintf("A's build directories take %s after the trim, want at most %s", diskBytes(is), limit))
	}
	files := before.snapshotFiles(slices.Concat(s.A, s.B, []string{s.Image}))
	read := map[string]bool{}
	for _, latest := range []string{s.A[len(s.A)-1], s.B[len(s.B)-1], s.Image} {
		for id := range cleanupClosure(latest, files) {
			read[id] = true
		}
	}
	for _, id := range slices.Concat(s.A, s.B) {
		was, recorded := before.Builds[id]
		is, exists := after.Builds[id]
		switch {
		case !recorded:
		case !exists && read[id]:
			problems = append(problems, fmt.Sprintf("%s's directory is gone, though a latest snapshot reads it", id))
		case exists:
			problems = append(problems, fileProblems(id, was, is)...)
		}
	}
	return append(problems, imageProblems(before, after, s)...), nil
}

func fileProblems(id string, was, is storedBuild) []string {
	var problems []string
	for _, name := range storedFiles {
		before, recorded := was.Files[name]
		after, exists := is.Files[name]
		switch {
		case !recorded:
		case !exists:
			problems = append(problems, fmt.Sprintf("%s %s is gone", id, name))
		case after.Size != before.Size:
			problems = append(problems, fmt.Sprintf("%s %s is %d bytes, want %d", id, name, after.Size, before.Size))
		}
		if exists && after.Sum != before.Sum {
			problems = append(problems, fmt.Sprintf("%s %s's SHA-256 is %s, want %s", id, name, after.Sum, before.Sum))
		}
	}
	return problems
}

func imageProblems(before, after storageRecord, s trimSubjects) []string {
	var problems []string
	read := before.reads(s.Image, s.B)
	for _, id := range s.B {
		is, exists := after.Builds[id]
		if !read[id] || !exists {
			continue
		}
		for _, name := range storedFiles {
			was, recorded := before.Builds[id].Files[name]
			if recorded && is.Files[name].Allocated < was.Allocated {
				problems = append(problems, fmt.Sprintf("%s %s, which the image reads, has %d bytes allocated, want at least %d",
					id, name, is.Files[name].Allocated, was.Allocated))
			}
		}
	}
	return problems
}
