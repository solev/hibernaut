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
	for _, b := range l.Builds {
		switch b.Status {
		case "ready", "failed":
		case "pending", "in_progress":
			if now.Sub(b.Created) < StuckAfter {
				return Plan{}, fmt.Errorf("%w: %s isn't done", ErrBusy, b.ID)
			}
		default:
			return Plan{}, fmt.Errorf("build %s has status %q, which hibd doesn't know, so deleting nothing", b.ID, b.Status)
		}
	}
	envs := map[string]Env{}
	for _, e := range l.Envs {
		envs[e.ID] = e
	}
	roots := map[string]string{} // build → why it stays
	keep := func(id, why string) {
		if _, ok := roots[id]; !ok && id != "" {
			roots[id] = why
		}
	}
	// E2B starts a machine from the ready build it put in an env last, under the tag asked for.
	slots := map[slot][]placed{}
	snapshotted := map[string]bool{} // machines with a ready snapshot
	for _, b := range l.Builds {
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
	order := slices.SortedFunc(maps.Keys(slots), func(a, b slot) int {
		return cmp.Or(cmp.Compare(a.env, b.env), cmp.Compare(a.tag, b.tag))
	})
	// latest keeps the build put in a slot last, and the one finished last, in case a later E2B
	// goes by that.
	latest := func(ps []placed, why string) {
		var at, fin time.Time
		for _, p := range ps {
			at, fin = later(at, p.at), later(fin, p.finished)
		}
		for _, p := range ps {
			if p.at.Equal(at) || p.finished.Equal(fin) {
				keep(p.build, why)
			}
		}
	}
	for _, s := range order {
		e := envs[s.env] // an env E2B didn't list counts as not removed
		if !e.Deleted.IsZero() {
			continue
		}
		why := "the current build of image " + s.env
		if s.tag != "default" {
			why += ":" + s.tag
		}
		if e.Machine != "" {
			why = "the current snapshot of machine " + e.Machine
		}
		latest(slots[s], why)
	}
	// A machine needs the image build it started from until its first pause is done: only then
	// do its snapshot's headers name it. So keep every image build that was current since the
	// first of those machines started, or in the last MinAge, in case hibd just started one.
	since := now.Add(-MinAge)
	for _, m := range l.Machines {
		if (!m.Paused || !snapshotted[m.ID]) && m.Started.Before(since) {
			since = m.Started
		}
	}
	// E2B starts machines only from images that aren't removed, so none of those machines started
	// from an image removed well before since. It checks the image before it starts a machine,
	// and stamps the start once the machine runs, so allow MinAge in between.
	for _, s := range order {
		e := envs[s.env]
		if e.Machine != "" || !e.Deleted.IsZero() && e.Deleted.Before(since.Add(-MinAge)) {
			continue
		}
		var before []placed // what was in the slot before since
		for _, p := range slots[s] {
			if p.at.Before(since) && p.finished.Before(since) {
				before = append(before, p)
			} else {
				keep(p.build, "a machine may have started from it")
			}
		}
		latest(before, "a machine may have started from it")
	}
	for _, id := range l.Cached {
		keep(id, "E2B's layer cache points to it")
	}
	// A build is a removed machine's, or a removed image's, once every env it's in is removed.
	removed := map[string]bool{}
	for _, b := range l.Builds {
		removed[b.ID] = len(b.In) > 0
		for _, a := range b.In {
			removed[b.ID] = removed[b.ID] && !envs[a.Env].Deleted.IsZero()
		}
	}
	for _, d := range l.Dirs {
		if now.Sub(d.Modified) < MinAge && !removed[d.Build] {
			keep(d.Build, "changed in the last hour")
		}
	}
	return plan(l.Dirs, roots)
}

// A slot is a tag of an env: E2B starts machines from the build it put in the slot last.
type slot struct{ env, tag string }

// placed is a ready build in a slot.
type placed struct {
	build string
	// at is when E2B put it in the slot.
	at, finished time.Time
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// finished is when b finished, or else when it started.
func finished(b Build) time.Time {
	if b.Finished.IsZero() {
		return b.Created
	}
	return b.Finished
}

// plan keeps the directories of roots and of every build their headers name, transitively, and
// deletes the rest. It fails if a kept directory's headers couldn't be read.
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
	for len(todo) > 0 {
		d := byID[todo[0]]
		todo = todo[1:]
		if d.Err != nil {
			return Plan{}, fmt.Errorf("can't tell which builds %s needs, so deleting none: %w", d.Build, d.Err)
		}
		for _, ref := range d.Refs {
			if _, ok := byID[ref]; !ok {
				continue
			}
			if _, ok := p.Keep[ref]; !ok {
				p.Keep[ref] = d.Build + " needs it"
				todo = append(todo, ref)
			}
		}
	}
	for _, d := range dirs {
		if _, ok := p.Keep[d.Build]; !ok {
			p.Delete = append(p.Delete, d.Build)
		}
	}
	slices.Sort(p.Delete)
	return p, nil
}

// Headers are the files in a build directory that name the builds its data is in.
var Headers = []string{"memfile.header", "rootfs.ext4.header"}

// MinAge is how long a directory must stay unchanged before it's deleted, unless only removed
// images and machines use its build: E2B may still be writing it.
const MinAge = time.Hour

// StuckAfter is how long after a build started E2B must have stopped writing it, if it's still
// not done: image builds time out after an hour, and pauses take seconds. E2B never finishes a
// build it stopped writing, say when it crashed mid-pause, so cleanup can't wait for those.
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
	if len(b) < metadataSize {
		return nil, fmt.Errorf("header is %d bytes, too short", len(b))
	}
	// The low 16 bits are the format; the rest are flags.
	if v := binary.LittleEndian.Uint64(b) & 0xFFFF; v < 1 || v > 3 {
		return nil, fmt.Errorf("header version %d isn't supported", v)
	}
	if (len(b)-metadataSize)%mappingSize != 0 {
		return nil, fmt.Errorf("header is %d bytes: its mappings are cut short", len(b))
	}
	ids := []string{uuid(b[32:48]), uuid(b[48:64])}
	for m := b[metadataSize:]; len(m) > 0; m = m[mappingSize:] {
		ids = append(ids, uuid(m[16:32]))
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	return slices.DeleteFunc(ids, func(id string) bool { return id == nilUUID }), nil
}

const nilUUID = "00000000-0000-0000-0000-000000000000"

func uuid(b []byte) string {
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
