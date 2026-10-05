//go:build !e2e

package e2e

import (
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/cleanup"
)

const (
	oldA    = "a0a0a0a0-a0a0-4a0a-8a0a-a0a0a0a0a0a0"
	goneA   = "a1a1a1a1-a1a1-4a1a-8a1a-a1a1a1a1a1a1"
	latestA = "a2a2a2a2-a2a2-4a2a-8a2a-a2a2a2a2a2a2"
	oldB    = "b0b0b0b0-b0b0-4b0b-8b0b-b0b0b0b0b0b0"
	latestB = "b1b1b1b1-b1b1-4b1b-8b1b-b1b1b1b1b1b1"
	saved   = "c0c0c0c0-c0c0-4c0c-8c0c-c0c0c0c0c0c0"
	devBase = "d0d0d0d0-d0d0-4d0d-8d0d-d0d0d0d0d0d0"

	sumOne = "0101010101010101010101010101010101010101010101010101010101010101"
	sumTwo = "0202020202020202020202020202020202020202020202020202020202020202"
)

func TestTemplateQuery(t *testing.T) {
	query, err := templateQuery("hibimg-e2e-image-0a1b2c3d")
	if err != nil || !strings.Contains(query, "al.alias = 'hibimg-e2e-image-0a1b2c3d'") ||
		!strings.Contains(query, "SELECT b.id, b.status_group FROM env_aliases al") ||
		!strings.HasSuffix(query, "ORDER BY max(a.created_at)") {
		t.Errorf("template query = %q, %v", query, err)
	}
	for _, alias := range []string{"", "-flag", "hibimg-x'; DROP TABLE env_builds; --", "hibimg-$(echo bad)", "a\nb", strings.Repeat("a", 129)} {
		if _, err := templateQuery(alias); err == nil {
			t.Errorf("template query accepted the unsafe alias %q", alias)
		}
	}
}

func TestStorageRecordCommand(t *testing.T) {
	command, err := storageRecordCommand([]string{chainA, chainB})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"cd /var/lib/e2b/local-template-storage",
		`printf "block\t%s\n" "$(stat -f -c %S .)"`,
		"for b in " + chainA + " " + chainB + "; do",
		`du -sB1 -- "$b"`,
		"for f in memfile rootfs.ext4; do",
		"for f in memfile.header rootfs.ext4.header snapfile metadata.json; do",
		`stat -c "%b %B %s" -- "$b/$f"`,
		`sha256sum < "$b/$f" | cut -c1-64`,
		`base64 -w0 -- "$b/$f"`,
	} {
		if !strings.Contains(command, want) {
			t.Errorf("storage command doesn't contain %q", want)
		}
	}
	for _, builds := range [][]string{nil, {"../build"}, {chainA, chainA}, {chainA + ";echo bad"}, {"'"}} {
		if got, err := storageRecordCommand(builds); err == nil || got != "" {
			t.Errorf("storage command accepted builds %q", builds)
		}
	}
}

func storageWire(t *testing.T) string {
	t.Helper()
	lines := []string{"block\t4096"}
	for _, build := range []string{chainA, chainC} {
		lines = append(lines, "dir\t1048576\t"+build, "file\t"+build+"\tmemfile\t16 512 8192")
		for _, name := range snapshotSidecars {
			lines = append(lines, fmt.Sprintf("file\t%s\t%s\t8 512 144", build, name), fmt.Sprintf("sum\t%s\t%s\t%s", build, name, sumOne))
			switch name {
			case "memfile.header":
				lines = append(lines, fmt.Sprintf("header\t%s\t%s\t%s", build, name, wireHeader(t, build, image, map[string]uint64{build: 4096})))
			case "rootfs.ext4.header":
				lines = append(lines, fmt.Sprintf("header\t%s\t%s\t%s", build, name, wireHeader(t, build, image, map[string]uint64{image: 4096})))
			}
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func TestParseStorageRecord(t *testing.T) {
	got, err := parseStorageRecord(storageWire(t), []string{chainA, chainB, chainC})
	if err != nil {
		t.Fatal(err)
	}
	if got.Block != 4096 || len(got.Builds) != 2 {
		t.Fatalf("record = %+v, want 4096-byte blocks and the two surviving builds", got)
	}
	build := got.Builds[chainC]
	if build.Disk != 1048576 || len(build.Files) != 5 {
		t.Errorf("build = %+v, want 1 MiB on disk and five files", build)
	}
	if build.Files["memfile"] != (storedFile{Size: 8192, Allocated: 8192}) {
		t.Errorf("memfile = %+v, want 8192 bytes and 8192 allocated, no SHA-256", build.Files["memfile"])
	}
	if build.Files["snapfile"] != (storedFile{Size: 144, Allocated: 4096, Sum: sumOne}) {
		t.Errorf("snapfile = %+v, want 144 bytes, 4096 allocated and its SHA-256", build.Files["snapfile"])
	}
	if header := build.Headers["memfile.header"]; header.Build != chainC || !maps.Equal(header.BytesByBuild, map[string]uint64{chainC: 4096}) {
		t.Errorf("memfile header = %+v, want the build's own 4096 bytes", header)
	}
	if header := build.Headers["rootfs.ext4.header"]; header.Build != chainC || !maps.Equal(header.BytesByBuild, map[string]uint64{image: 4096}) {
		t.Errorf("rootfs header = %+v, want 4096 bytes of the image", header)
	}
}

func TestParseStorageRecordRefusesMalformedOutput(t *testing.T) {
	wire := storageWire(t)
	header := wireHeader(t, chainA, image, map[string]uint64{chainA: 4096})
	for name, out := range map[string]string{
		"empty":                "",
		"no block size":        strings.Replace(wire, "block\t4096\n", "", 1),
		"two block sizes":      wire + "block\t4096\n",
		"zero block size":      strings.Replace(wire, "block\t4096", "block\t0", 1),
		"odd block size":       strings.Replace(wire, "block\t4096", "block\t4095", 1),
		"signed block size":    strings.Replace(wire, "block\t4096", "block\t+4096", 1),
		"unexpected build":     wire + "dir\t0\t" + image + "\n",
		"duplicate directory":  wire + "dir\t0\t" + chainA + "\n",
		"unsafe directory ID":  strings.ReplaceAll(wire, chainA, "../bad"),
		"file before listing":  "file\t" + chainB + "\tmemfile\t8 512 4096\n" + wire,
		"unknown file":         strings.Replace(wire, "\tmemfile\t", "\tother\t", 1),
		"duplicate file":       wire + "file\t" + chainC + "\tmemfile\t8 512 4096\n",
		"negative blocks":      strings.Replace(wire, "16 512 8192", "-16 512 8192", 1),
		"allocation overflow":  strings.Replace(wire, "16 512 8192", "18446744073709551615 512 8192", 1),
		"extra stat field":     strings.Replace(wire, "16 512 8192", "16 512 8192 1", 1),
		"sum of a body":        wire + "sum\t" + chainC + "\tmemfile\t" + sumOne + "\n",
		"sum without file":     strings.Replace(wire, "file\t"+chainC+"\tsnapfile\t8 512 144\n", "", 1),
		"duplicate sum":        wire + "sum\t" + chainC + "\tsnapfile\t" + sumTwo + "\n",
		"short sum":            strings.Replace(wire, sumOne, sumOne[1:], 1),
		"upper case sum":       strings.Replace(wire, sumOne, strings.Repeat("A", 64), 1),
		"missing sum":          strings.Replace(wire, "sum\t"+chainC+"\tmetadata.json\t"+sumOne+"\n", "", 1),
		"header of a sidecar":  wire + "header\t" + chainC + "\tsnapfile\t" + header + "\n",
		"header without file":  strings.Replace(wire, "file\t"+chainA+"\tmemfile.header\t8 512 144\n", "", 1),
		"missing header":       strings.Replace(wire, "header\t"+chainA+"\tmemfile.header\t"+header+"\n", "", 1),
		"duplicate header":     wire + "header\t" + chainA + "\tmemfile.header\t" + header + "\n",
		"invalid base64":       strings.Replace(wire, header, "!", 1),
		"wrong header build":   strings.Replace(wire, header, wireHeader(t, image, image, nil), 1),
		"unknown record":       wire + "unsafe\t\x1b[31m\n",
		"short record":         wire + "file\t" + chainA + "\n",
		"directory extra":      strings.Replace(wire, "dir\t1048576\t"+chainA, "dir\t1048576\t"+chainA+"\textra", 1),
		"block extra":          strings.Replace(wire, "block\t4096", "block\t4096\textra", 1),
		"negative disk usage":  strings.Replace(wire, "dir\t1048576", "dir\t-1048576", 1),
		"oversized disk usage": strings.Replace(wire, "dir\t1048576", "dir\t9223372036854775808", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseStorageRecord(out, []string{chainA, chainB, chainC}); err == nil {
				t.Fatal("accepted a malformed storage record")
			} else if strings.ContainsAny(err.Error(), "\n\x1b") {
				t.Errorf("error prints raw host output: %q", err)
			}
		})
	}
}

func TestMissingStorageFiles(t *testing.T) {
	record, err := parseStorageRecord(storageWire(t), []string{chainA, chainC})
	if err != nil {
		t.Fatal(err)
	}
	if got := record.missingFiles([]string{chainA, chainC}); len(got) != 0 {
		t.Errorf("complete builds miss %q", got)
	}
	got := record.missingFiles([]string{chainA, chainB, chainC})
	if len(got) != 1 || got[0] != chainB+" has no directory" {
		t.Errorf("missing = %q, want only the directory of %s", got, chainB)
	}

	wire := strings.Replace(storageWire(t), "file\t"+chainC+"\tmemfile\t16 512 8192\n", "", 1)
	wire = strings.Replace(wire, "file\t"+chainC+"\tsnapfile\t8 512 144\nsum\t"+chainC+"\tsnapfile\t"+sumOne+"\n", "", 1)
	header := wireHeader(t, chainA, image, map[string]uint64{chainA: 4096})
	wire = strings.Replace(wire, "file\t"+chainA+"\tmemfile.header\t8 512 144\nsum\t"+chainA+"\tmemfile.header\t"+sumOne+"\n"+
		"header\t"+chainA+"\tmemfile.header\t"+header+"\n", "", 1)
	record, err = parseStorageRecord(wire, []string{chainA, chainC})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		chainA + " has no memfile.header",
		chainC + " has no memfile, though its memfile.header maps 4096 bytes to it",
		chainC + " has no snapfile",
	}
	if got := record.missingFiles([]string{chainA, chainC}); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("missing = %q, want %q", got, want)
	}
}

func TestMissingStorageFilesAllowsABodyItsHeaderDoesntMap(t *testing.T) {
	wire := strings.Replace(storageWire(t), "file\t"+chainC+"\tmemfile\t16 512 8192\n", "", 1)
	wire = strings.Replace(wire, wireHeader(t, chainC, image, map[string]uint64{chainC: 4096}),
		wireHeader(t, chainC, image, map[string]uint64{image: 4096}), 1)
	record, err := parseStorageRecord(wire, []string{chainA, chainC})
	if err != nil {
		t.Fatal(err)
	}
	if got := record.missingFiles([]string{chainA, chainC}); len(got) != 0 {
		t.Errorf("a pause that changed no memory misses %q", got)
	}
}

func TestWouldTrimAny(t *testing.T) {
	out := strings.Join([]string{
		"would delete " + goneA + " (64 MiB)",
		"would trim " + oldA + " memfile: keeps 64 MiB of 410 MiB, frees about 346 MiB",
		"would trim about 346 MiB in 1 files",
		"would trim " + strings.ToUpper(oldB) + " memfile: keeps 0 MiB of 4 MiB, frees about 4 MiB",
		"not trimming: " + latestB + " has no directory",
		"left " + latestA + " memfile: changed",
	}, "\n")
	if !wouldTrimAny(out, []string{goneA, oldA}) {
		t.Errorf("a would trim line for %s isn't found", oldA)
	}
	for _, builds := range [][]string{{goneA, latestA}, {latestB}, {oldB}, nil} {
		if wouldTrimAny(out, builds) {
			t.Errorf("found a would trim line for %q", builds)
		}
	}
	if !wouldTrimAny("would trim "+oldA+" rootfs.ext4: keeps 1 MiB of 2 MiB, frees about 1 MiB\n", []string{oldA}) {
		t.Error("a rootfs.ext4 line isn't found")
	}
	if wouldTrimAny("would trim "+oldA+" snapfile: keeps 1 MiB of 2 MiB\n", []string{oldA}) {
		t.Error("a line for a file trimming never touches counts")
	}
}

func sidecars(sum string) map[string]storedFile {
	files := map[string]storedFile{}
	for _, name := range snapshotSidecars {
		files[name] = storedFile{Size: 1000, Allocated: 4096, Sum: sum}
	}
	return files
}

func storedBody(files map[string]storedFile, memory, rootfs uint64) map[string]storedFile {
	files["memfile"] = storedFile{Size: memory, Allocated: memory}
	files["rootfs.ext4"] = storedFile{Size: rootfs, Allocated: rootfs}
	return files
}

func headers(build string, memory, rootfs map[string]uint64) map[string]cleanup.HeaderStats {
	return map[string]cleanup.HeaderStats{
		"memfile.header":     {Build: build, BaseBuild: devBase, BytesByBuild: memory},
		"rootfs.ext4.header": {Build: build, BaseBuild: devBase, BytesByBuild: rootfs},
	}
}

func trimFixture() (before, after storageRecord, subjects trimSubjects) {
	record := func(old, gone uint64) storageRecord {
		builds := map[string]storedBuild{
			oldA: {
				Disk:    int64(old + 16<<10),
				Files:   map[string]storedFile{},
				Headers: headers(oldA, map[string]uint64{oldA: 80 << 20}, map[string]uint64{oldA: 16 << 20}),
			},
			latestA: {
				Disk:  20<<20 + 16<<10,
				Files: storedBody(sidecars(sumOne), 12<<20, 4<<20),
				Headers: headers(latestA,
					map[string]uint64{oldA: 10 << 20, latestA: 12 << 20, devBase: 300 << 20},
					map[string]uint64{oldA: 2 << 20, latestA: 4 << 20, devBase: 900 << 20}),
			},
			oldB: {
				Disk:    8<<20 + 16<<10,
				Files:   storedBody(sidecars(sumOne), 6<<20, 2<<20),
				Headers: headers(oldB, map[string]uint64{oldB: 6 << 20}, map[string]uint64{oldB: 2 << 20}),
			},
			latestB: {
				Disk:    3<<20 + 16<<10,
				Files:   storedBody(sidecars(sumTwo), 2<<20, 1<<20),
				Headers: headers(latestB, map[string]uint64{oldB: 1 << 20, latestB: 2 << 20}, map[string]uint64{latestB: 1 << 20}),
			},
			saved: {
				Disk:    1<<20 + 16<<10,
				Files:   storedBody(sidecars(sumTwo), 1<<20, 0),
				Headers: headers(saved, map[string]uint64{oldB: 5 << 20, saved: 1 << 20}, map[string]uint64{oldB: 2 << 20}),
			},
		}
		files := sidecars(sumOne)
		files["memfile"] = storedFile{Size: 80 << 20, Allocated: old * 5 / 6}
		files["rootfs.ext4"] = storedFile{Size: 16 << 20, Allocated: old / 6}
		builds[oldA] = storedBuild{Disk: builds[oldA].Disk, Files: files, Headers: builds[oldA].Headers}
		if gone > 0 {
			builds[goneA] = storedBuild{
				Disk:    int64(gone + 16<<10),
				Files:   storedBody(sidecars(sumTwo), gone/2, gone/2),
				Headers: headers(goneA, map[string]uint64{goneA: gone / 2}, map[string]uint64{goneA: gone / 2}),
			}
		}
		return storageRecord{Block: 4096, Builds: builds}
	}
	return record(96<<20, 50<<20), record(12<<20, 0),
		trimSubjects{A: []string{oldA, goneA, latestA}, B: []string{oldB, latestB}, Image: saved}
}

func TestTrimLimit(t *testing.T) {
	before, after, subjects := trimFixture()
	limit, err := trimLimitFor(before, after, subjects.A)
	if err != nil {
		t.Fatal(err)
	}
	if limit != (trimLimit{Mapped: 28 << 20, Sidecars: 32 << 10, Block: 4096, Files: 12, Dirs: 2, Slack: trimSlack}) {
		t.Errorf("limit = %+v, want 28 MiB mapped, 32 KiB of sidecars and 12 files in 2 directories", limit)
	}
	if want := uint64(28<<20 + 32<<10 + 2*4096*12 + trimSlack); limit.total() != want {
		t.Errorf("limit total = %d, want %d", limit.total(), want)
	}
	want := "36 MiB (37879808 bytes): 29360128 mapped by A's latest snapshot, 32768 of headers, snapfiles and metadata, " +
		"98304 of rounding (two 4096-byte blocks for each of 12 files in 2 directories) and 8388608 of slack"
	if got := limit.String(); got != want {
		t.Errorf("limit = %q, want %q", got, want)
	}
	if _, err := trimLimitFor(storageRecord{Block: 4096}, after, subjects.A); err == nil {
		t.Error("a limit without A's latest snapshot")
	}
}

func TestTrimProblemsAcceptsATrimmedChain(t *testing.T) {
	before, after, subjects := trimFixture()
	if problems, err := trimProblems(before, after, subjects); err != nil || len(problems) != 0 {
		t.Errorf("problems = %q, %v, want none", problems, err)
	}
	shared := subjects
	shared.B = []string{oldB, saved, latestB}
	if problems, err := trimProblems(before, after, shared); err != nil || len(problems) != 0 {
		t.Errorf("with the image's build in B's slot, problems = %q, %v, want none", problems, err)
	}
	if got := before.reads(saved, subjects.B); !maps.Equal(got, map[string]bool{oldB: true}) {
		t.Errorf("the image reads %v, want only %s", got, oldB)
	}
	if got, want := before.disk(subjects.A), int64(96<<20+50<<20+20<<20+3*16<<10); got != want {
		t.Errorf("A's directories take %d bytes, want %d", got, want)
	}
}

func TestTrimProblemsReportTheNumbersTheyCompared(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(before, after storageRecord)
		want   []string
	}{
		{"not trimmed", func(before, after storageRecord) { after.Builds[oldA] = before.Builds[oldA] }, []string{
			"A's build directories take 116 MiB (121667584 bytes) after the trim, want less than half of the 166 MiB (174112768 bytes) they took after the pauses",
			"A's build directories take 116 MiB (121667584 bytes) after the trim, want at most 36 MiB (37879808 bytes): 29360128 mapped by A's latest snapshot",
		}},
		{"over the limit", func(_, after storageRecord) {
			build := after.Builds[oldA]
			build.Disk = 30 << 20
			after.Builds[oldA] = build
		}, []string{"A's build directories take 50 MiB (52445184 bytes) after the trim, want at most 36 MiB (37879808 bytes)"}},
		{"resized", func(_, after storageRecord) {
			after.Builds[oldA].Files["memfile"] = storedFile{Size: 64 << 20, Allocated: 10 << 20}
		}, []string{oldA + " memfile is 67108864 bytes, want 83886080"}},
		{"rewritten", func(_, after storageRecord) {
			after.Builds[latestB].Files["snapfile"] = storedFile{Size: 1000, Allocated: 4096, Sum: sumOne}
		}, []string{latestB + " snapfile's SHA-256 is " + sumOne + ", want " + sumTwo}},
		{"file gone", func(_, after storageRecord) { delete(after.Builds[oldA].Files, "metadata.json") }, []string{
			oldA + " metadata.json is gone",
		}},
		{"named directory gone", func(_, after storageRecord) { delete(after.Builds, oldB) }, []string{
			oldB + "'s directory is gone, though a latest snapshot reads it",
		}},
		{"image's build trimmed", func(_, after storageRecord) {
			after.Builds[oldB].Files["memfile"] = storedFile{Size: 6 << 20, Allocated: 4 << 20}
		}, []string{oldB + " memfile, which the image reads, has 4194304 bytes allocated, want at least 6291456"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, after, subjects := trimFixture()
			tc.change(before, after)
			problems, err := trimProblems(before, after, subjects)
			if err != nil {
				t.Fatal(err)
			}
			if len(problems) != len(tc.want) {
				t.Fatalf("problems = %q, want %d", problems, len(tc.want))
			}
			for i, want := range tc.want {
				if !strings.HasPrefix(problems[i], want) {
					t.Errorf("problem %d = %q, want it to start with %q", i, problems[i], want)
				}
			}
		})
	}
}

func TestTrimProblemsNeedALatestSnapshot(t *testing.T) {
	before, after, subjects := trimFixture()
	delete(before.Builds, latestA)
	if _, err := trimProblems(before, after, subjects); err == nil {
		t.Error("checked a trim without A's latest snapshot")
	}
}

func TestDistinctBuilds(t *testing.T) {
	got := distinctBuilds([]string{oldA, latestA}, []string{oldB, saved, latestB}, []string{saved})
	if want := []string{oldA, latestA, oldB, saved, latestB}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("builds = %q, want %q", got, want)
	}
}
