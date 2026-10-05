// Package cleanup finds the build directories in E2B's local template storage that nothing can
// start or resume from any more (spec §5, Snapshot cleanup). E2B never deletes them, not even a
// removed machine's.
//
// Each image build and each pause of a machine writes one directory, named after the build.
// A snapshot is a chain of diffs: the headers of a build's memory and disk say which build holds
// each range of their data, so a build needs every build its headers name.
package cleanup

import (
	"cmp"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"
)

// Listing is what cleanup decides from: the build directories, what E2B's database says about
// their builds, and the machines E2B lists.
type Listing struct {
	Dirs   []Dir
	Envs   []Env
	Builds []Build
	// Cached are the builds E2B's layer cache points to.
	Cached []string
	// Machines are every machine E2B lists, running or paused.
	Machines []Machine
}

// Dir is a build's directory.
type Dir struct {
	Build string
	// Modified is when the directory or a file in it last changed.
	Modified time.Time
	// Refs are the builds its headers name.
	Refs []string
	// Err says why its headers couldn't be read, so that Refs may be incomplete.
	Err error
}

// Env is what E2B calls a template: an image's, or a machine's snapshots.
type Env struct {
	ID string
	// Machine is the ID of the machine whose snapshots these are, or empty for an image.
	Machine string
	// Deleted is when the image or machine was removed, or zero.
	Deleted time.Time
}

// Build is a build in E2B's database.
type Build struct {
	ID string
	// Status is E2B's status group: pending, in_progress, ready or failed.
	Status  string
	Created time.Time
	// Finished is zero if E2B didn't record when it finished.
	Finished time.Time
	// In are the envs E2B put the build in. Most builds are in one, but an env made from a
	// machine's snapshot, say, shares that snapshot's build.
	In []Assignment
}

// Assignment puts a build in an env, under a tag, at a time.
type Assignment struct {
	Env, Tag string
	At       time.Time
}

// Machine is a machine E2B lists.
type Machine struct {
	ID      string
	Started time.Time
	// Paused is true for a paused machine, and for one E2B is still pausing.
	Paused bool
}

// Plan is what to delete and what to keep.
type Plan struct {
	// Delete are the directories nothing needs, sorted.
	Delete []string
	// Keep says why each other directory stays.
	Keep map[string]string
}

// Garbage plans which directories to delete. It keeps the current build of each image tag and
// machine, every image build a machine may have started from, the layer cache's builds, anything
// changed in the last MinAge but removed images' and machines', and every build their headers
// name.
func Garbage(l Listing, now time.Time) (Plan, error) {
	return garbagePlan(l, retentionPolicy{MinAge, StuckAfter}, now)
}

type retentionPolicy struct {
	minAge, stuckAfter time.Duration
}

func garbagePlan(l Listing, policy retentionPolicy, now time.Time) (Plan, error) {
	if err := checkBuilds(l.Builds, policy.stuckAfter, now); err != nil {
		return Plan{}, err
	}
	envs := indexEnvs(l.Envs)
	slots, snapshotted := readySlots(l.Builds, envs)
	order := slices.SortedFunc(maps.Keys(slots), func(a, b slot) int {
		return cmp.Or(cmp.Compare(a.env, b.env), cmp.Compare(a.tag, b.tag))
	})
	roots := protection{}
	roots.keepCurrent(slots, order, envs)
	since := startupSince(l.Machines, snapshotted, now).Add(-policy.minAge)
	roots.keepStartupBuilds(slots, order, envs, since)
	for _, id := range l.Cached {
		roots.keep(id, "E2B's layer cache points to it")
	}
	roots.keepRecent(l.Dirs, removedBuilds(l.Builds, envs), policy.minAge, now)
	return plan(l.Dirs, roots)
}

func checkBuilds(builds []Build, stuckAfter time.Duration, now time.Time) error {
	for _, b := range builds {
		switch b.Status {
		case "ready", "failed":
		case "pending", "in_progress":
			if now.Sub(b.Created) < stuckAfter {
				return fmt.Errorf("%w: %s isn't done", ErrBusy, b.ID)
			}
		default:
			return fmt.Errorf("build %s has status %q, which hibd doesn't know, so deleting nothing", b.ID, b.Status)
		}
	}
	return nil
}

func indexEnvs(list []Env) map[string]Env {
	envs := map[string]Env{}
	for _, e := range list {
		envs[e.ID] = e
	}
	return envs
}

type protection map[string]string

func (p protection) keep(id, why string) {
	if _, ok := p[id]; !ok && id != "" {
		p[id] = why
	}
}

func readySlots(builds []Build, envs map[string]Env) (map[slot][]placed, map[string]bool) {
	// E2B starts a machine from the ready build it put in an env last, under the tag asked for.
	slots := map[slot][]placed{}
	snapshotted := map[string]bool{}
	for _, b := range builds {
		if b.Status != "ready" {
			continue
		}
		for _, a := range b.In {
			s := slot{a.Env, a.Tag}
			slots[s] = append(slots[s], placed{b.ID, a.At, finished(b)})
			if m := envs[a.Env].Machine; m != "" {
				snapshotted[m] = true
			}
		}
	}
	return slots, snapshotted
}

func (p protection) keepLatest(ps []placed, why string) {
	var at, fin time.Time
	for _, placed := range ps {
		at, fin = later(at, placed.at), later(fin, placed.finished)
	}
	for _, placed := range ps {
		if placed.at.Equal(at) || placed.finished.Equal(fin) {
			p.keep(placed.build, why)
		}
	}
}

func (p protection) keepCurrent(slots map[slot][]placed, order []slot, envs map[string]Env) {
	for _, s := range order {
		e := envs[s.env]
		if !e.Deleted.IsZero() {
			continue
		}
		p.keepLatest(slots[s], currentReason(s, e))
	}
}

func currentReason(s slot, e Env) string {
	if e.Machine != "" {
		return "the current snapshot of machine " + e.Machine
	}
	why := "the current build of image " + s.env
	if s.tag != "default" {
		why += ":" + s.tag
	}
	return why
}

func startupSince(machines []Machine, snapshotted map[string]bool, now time.Time) time.Time {
	// A machine needs the image build it started from until its first pause is done: only then
	// do its snapshot's headers name it. E2B picks the build when it checks the image, and stamps
	// the start once the machine runs. E2B may not list one hibd is starting yet.
	since := now
	for _, m := range machines {
		if (!m.Paused || !snapshotted[m.ID]) && m.Started.Before(since) {
			since = m.Started
		}
	}
	return since
}

func (p protection) keepStartupBuilds(slots map[slot][]placed, order []slot, envs map[string]Env, since time.Time) {
	// E2B starts machines only from images that aren't removed.
	for _, s := range order {
		e := envs[s.env]
		if e.Machine != "" || !e.Deleted.IsZero() && e.Deleted.Before(since) {
			continue
		}
		p.keepHistory(slots[s], since)
	}
}

func (p protection) keepHistory(ps []placed, since time.Time) {
	var before []placed
	for _, placed := range ps {
		if placed.at.Before(since) && placed.finished.Before(since) {
			before = append(before, placed)
		} else {
			p.keep(placed.build, "a machine may have started from it")
		}
	}
	p.keepLatest(before, "a machine may have started from it")
}

func removedBuilds(builds []Build, envs map[string]Env) map[string]bool {
	removed := map[string]bool{}
	for _, b := range builds {
		removed[b.ID] = len(b.In) > 0
		for _, a := range b.In {
			removed[b.ID] = removed[b.ID] && !envs[a.Env].Deleted.IsZero()
		}
	}
	return removed
}

func (p protection) keepRecent(dirs []Dir, removed map[string]bool, minAge time.Duration, now time.Time) {
	for _, d := range dirs {
		if now.Sub(d.Modified) < minAge && !removed[d.Build] {
			p.keep(d.Build, "changed in the last hour")
		}
	}
}

// A slot is a tag of an env: E2B starts machines from the build it put in the slot last.
type slot struct{ env, tag string }

type placed struct {
	build        string
	at, finished time.Time
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func finished(b Build) time.Time {
	if b.Finished.IsZero() {
		return b.Created
	}
	return b.Finished
}

func plan(dirs []Dir, roots map[string]string) (Plan, error) {
	byID := map[string]Dir{}
	for _, d := range dirs {
		byID[d.Build] = d
	}
	p := Plan{Keep: map[string]string{}}
	var todo []string
	for id, why := range roots {
		if _, ok := byID[id]; ok {
			p.Keep[id] = why
			todo = append(todo, id)
		}
	}
	if err := keepReferences(byID, p.Keep, todo); err != nil {
		return Plan{}, err
	}
	for _, d := range dirs {
		if _, ok := p.Keep[d.Build]; !ok {
			p.Delete = append(p.Delete, d.Build)
		}
	}
	slices.Sort(p.Delete)
	return p, nil
}

func keepReferences(byID map[string]Dir, keep map[string]string, todo []string) error {
	for len(todo) > 0 {
		d := byID[todo[0]]
		todo = todo[1:]
		if d.Err != nil {
			return fmt.Errorf("can't tell which builds %s needs, so deleting none: %w", d.Build, d.Err)
		}
		for _, ref := range d.Refs {
			if _, ok := byID[ref]; !ok {
				continue
			}
			if _, ok := keep[ref]; !ok {
				keep[ref] = d.Build + " needs it"
				todo = append(todo, ref)
			}
		}
	}
	return nil
}

// Headers are the files in a build directory that name the builds its data is in.
var Headers = []string{"memfile.header", "rootfs.ext4.header"}

// MinAge is how long a directory must stay unchanged before it's deleted, unless only removed
// images and machines use its build: E2B may still be writing it.
const MinAge = time.Hour

// StuckAfter is how long after a build started E2B must have stopped writing it, if it's still
// not done: image builds time out after an hour, and pauses take seconds. E2B never finishes a
// build it stopped writing, say when it crashed mid-pause.
const StuckAfter = 6 * time.Hour

// ErrBusy is wrapped by Garbage's error while E2B is writing a build: until a build is done,
// nothing says which builds it needs. Try again later.
var ErrBusy = errors.New("E2B is writing a build")

const (
	metadataSize = 64 // version, block size, size, generation, build, base build
	mappingSize  = 40 // offset, length, build, offset in that build
)

// ParseHeader returns the builds a header names: its own, its base, and every build holding a
// range of its data. It reads only E2B's uncompressed headers, versions 1 to 3.
func ParseHeader(b []byte) ([]string, error) {
	stats, err := ParseHeaderStats(b)
	if err != nil {
		return nil, err
	}
	ids := append([]string{stats.Build, stats.BaseBuild}, slices.Collect(maps.Keys(stats.BytesByBuild))...)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	return slices.DeleteFunc(ids, func(id string) bool { return id == nilUUID }), nil
}

// HeaderStats describes a header's metadata and the bytes mapped to each nonzero source UUID.
type HeaderStats struct {
	Version, BlockSize, Size, Generation uint64
	Build, BaseBuild                     string
	BytesByBuild                         map[string]uint64
	Mappings                             int
}

// ParseHeaderStats reads metadata and mappings from E2B's uncompressed headers, versions 1 to 3.
func ParseHeaderStats(b []byte) (HeaderStats, error) {
	if len(b) < metadataSize {
		return HeaderStats{}, fmt.Errorf("header is %d bytes, too short", len(b))
	}
	// The low 16 bits are the format; the rest are flags.
	version := binary.LittleEndian.Uint64(b) & 0xFFFF
	if version < 1 || version > 3 {
		return HeaderStats{}, fmt.Errorf("header version %d isn't supported", version)
	}
	if (len(b)-metadataSize)%mappingSize != 0 {
		return HeaderStats{}, fmt.Errorf("header is %d bytes: its mappings are cut short", len(b))
	}
	stats := HeaderStats{
		Version:      version,
		BlockSize:    binary.LittleEndian.Uint64(b[8:]),
		Size:         binary.LittleEndian.Uint64(b[16:]),
		Generation:   binary.LittleEndian.Uint64(b[24:]),
		Build:        uuid(b[32:48]),
		BaseBuild:    uuid(b[48:64]),
		BytesByBuild: map[string]uint64{},
		Mappings:     (len(b) - metadataSize) / mappingSize,
	}
	for m := b[metadataSize:]; len(m) > 0; m = m[mappingSize:] {
		if id := uuid(m[16:32]); id != nilUUID {
			stats.BytesByBuild[id] += binary.LittleEndian.Uint64(m[8:])
		}
	}
	return stats, nil
}

const nilUUID = "00000000-0000-0000-0000-000000000000"

func uuid(b []byte) string {
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
