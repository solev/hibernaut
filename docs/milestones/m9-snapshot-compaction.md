# M9 Snapshot compaction

Status: **done** · Spec: §5 (Snapshot compaction) · Needs: M2 (snapshot cleanup) ·
Research: [e2b-snapshot-compaction](../research/e2b-snapshot-compaction.md)

**Goal:** a machine's old snapshots stop taking more disk with every pause. After each cleanup,
they hold only the data its current snapshot still reads.

It can come any time after M2, and should come before the host's disk fills: until then,
`hib rm` is the only way to free a machine's snapshots.

## Why

M2's `TestPauseCycles` paused and resumed an idle `dev` machine 20 times (2026-10-02). Resume
time stayed at 0.7–1.5 s, but every pause wrote about 280 MiB, and the latest snapshot still
needed 13 of the 20 (3.6 GiB), one more with every pause since the 12th.

M9's first measurement (S0, 2026-10-05) ran it twice, rewriting a 64 MiB file of random data in
the machine's home directory before each of 20 pauses: once without syncing it, once synced to
disk. Cleanup kept 19 or 20 builds (5.4 to 5.7 GiB), about 300 MiB more with every pause. Yet the
latest snapshot's own headers map only 447 MiB of that, in 12 builds (309 MiB in all 20 when the
file was synced). Resume stayed between 0.6 and 1.1 s.

A snapshot is a chain of diffs. A pause's headers map every range of the machine's memory and
disk to the build that holds it, and E2B reads only through the headers of the snapshot it starts
from. But each build's headers also name older builds, and cleanup keeps every build a kept
build's headers name, so it keeps the whole chain, all of every file. Deleting the builds the
latest headers don't name would free little: they name most of them, and a named build keeps its
whole file.

## Design

The M9 code is built against this section. Code that has to differ updates it in the same commit.

**Trim, don't delete.** Cleanup deletes what M2 deletes, and keeps every other build directory
with all its files (headers, snapfile, metadata) at their sizes. New in M9: in old builds, it
punches holes in the memory and disk files, `memfile` and `rootfs.ext4`, wherever no snapshot
that can still be used reads them, with `fallocate(PUNCH_HOLE|KEEP_SIZE)`. A hole reads as zeros
and takes no disk. Everything in hib that reads build directories keeps working: it checks that
files exist, that headers are valid, and that their ranges fit in the files. leakscan reads holes
as zeros.

**Roots** are the builds something may use whole: start a machine or a build from, or resume.
Trimming keeps all of a root, and every range a root's headers map to another build. M2's roots
stay:

- the latest ready build in each tag of every image, saved image and machine that isn't removed
- the image builds a machine may have started from (M2's startup history)
- the builds E2B's layer cache points to
- the directories changed in the last `MinAge` (an hour), but removed images' and machines'

New in M9, for each machine whose snapshots aren't removed, the builds it may be running from or
may yet resume from: `keepHistory` on the machine's slot, from a time `since`.

- E2B caches a machine's latest snapshot for up to 5 minutes, so a machine may resume from a
  snapshot that was just replaced. For a machine E2B lists as running, `since` is an hour before
  it started: any build put in its slot from then on, or the latest one before, may be what it
  runs from. If E2B doesn't say when it started, all of its builds are roots.
- E2B lists a machine it's still pausing as paused, and records the pause's build only after
  that. So a paused machine counts as running until its pause is done, when its slot has a ready
  build created after it started that's in no image's slot, and for an hour after that build
  finished. An image save puts its build in the machine's slot as well as the image's, and the
  machine keeps running.
- E2B's listing skips a machine in `snapshotting` and shows its last pause instead, with that
  pause's run start. Before E2B records the save's pending build, that stale start can hide the
  older snapshot its current run reads. Saves and forks first pause, confirm durability and
  resume the origin, so this is a stale pause, not an unlisted new machine. Cleanup reads under
  `hibd.lock`, which saves and forks hold through the whole save. Persistent save records cover
  a save interrupted after dispatch. A GET recheck cannot prove safety because E2B falls back
  to its database snapshot on any live-store error.
- Otherwise `since` is an hour ago: a replaced snapshot stays a root for an hour.

After an image save, E2B resumes the machine from the save's build. With E2B's
`in-place-checkpoint` flag on, it would keep the machine on the build it was running from, and
the machine's later snapshots would read through that one. The host sets no E2B flags, so the
flag is off, and the pause rule doesn't count an image save's build either way.

A machine with no ready snapshot of its own may have started from another machine's: E2B's
native fork does that, and copies the machine's metadata, so E2B's listing can't tell such a
machine from a new one. A native fork also checkpoints the machine it forks: a build in that
machine's slot alone, made while the machine keeps running, which looks like a finished pause.
hib never forks that way (`hib fork` saves an image first), and M2's deletion already relies on
that for removed machines. M9 makes sure of it: hib's E2B client is generated without E2B's fork
endpoint, by an overlay in `internal/engine/e2b`. Still, while E2B lists a machine with no
snapshot of its own, every machine slot that isn't removed keeps its builds from an hour before
that machine started, or all of them if E2B doesn't say when it started. M2's startup history
does the same for images.

The hour is `SelectionGrace`, a constant of its own beside `MinAge`. Deletion keeps these builds
too. Roots never depend on the trim scope below.

**Read under the save lock.** Cleanup takes `cleanup.lock`, then `hibd.lock`. It holds the
second across E2B's listing, both SQL queries, the layer cache, build directories and save-record
evaluation. It releases it before deletion and trimming, on every exit path. No machine operation
takes `cleanup.lock`. A sweep never waits for `hibd.lock`. It returns `machines.ErrBusy` and
retries in a minute, without a `not trimming` message. The command waits cancellably and says
`waiting for an image save or fork to finish` once. This excludes saves, not the whole machine
lifecycle. SSH wake stays unlocked. A resume during or after the read still uses a paused root,
including the replaced roots kept by `SelectionGrace`.

**Save records.** After preparing the origin, and a final cancellation check, hibd publishes a
private, state-owner-owned JSON file in its state directory's `saves` directory before dispatch.
Each save has a unique file. It records the engine ID, name, wall-clock start, boot ID, zero end
and `succeeded: false`. Files are opened without following symlinks. Publication syncs the file
and directories. The save POST and readback use `context.WithoutCancel`, with a ten-minute
`saveCallTimeout` backstop. Ctrl-C after dispatch no longer cancels that save in hibd. Losing SSH
can still end hibd, so this does not promise an image appears. Check `hib image ls` and `hib ls`
before retrying. Fork templates left without copies are swept as before.

Immediately after the call, before restoring the origin or cleaning an alias, hibd deletes the
record for a 404 or 409 that created nothing. Otherwise it writes the end time and whether
`SaveTemplate` succeeded: a 201 and a good template readback. An HTTP error, a failed readback,
an invalid response body and a transport failure are all unsuccessful. Legacy records with
`answered: true` but no `succeeded` remain readable and count as unsuccessful.

An HTTP answer alone is not proof. E2B marks the build failed when its 70-second deadline ends,
but the node may already run the machine from that build and keep uploading for up to 20 minutes.
The failed build stays whole while a record inhibits cleanup. A later ready pause keeps that
input through its headers, and trimming never punches a failed build.

A record write or removal failure reports an error and leaves cleanup inhibited. It does not
choose the save's outcome. Failed saves still restore the origin and clean their alias.
Successful saves still re-pause a paused origin and keep their image if only recording failed.
Their normal re-pause failure handling stays unchanged. The error gives the recovery threshold
and a next step. Callers keep a returned image with an error. A fork stops, leaving its hidden
template to the existing sweep.

Cleanup settles a record only when one of these is proved by its locked read:

- The save succeeded, and the listing shows the machine running.
- The machine's slot has a ready build in no image's slot, created after the recorded end.
  With no end, it must be created after start plus `saveCallTimeout`. Removed images count
  as images when checking the build's assignments.
- The machine has a slot, and every slot of it is deleted.
- Both boot IDs are known and differ. Nothing the old host boot ran survives.

An unsuccessful record never settles just because the listing says running. Cleanup checks every
record before deleting settled ones. A dry run only says it would clear them. An unknown current
boot ID is not proof of a restart.

Only `saveOrigin` publishes records. Its save and fork callers hold `hibd.lock`, and cleanup
evaluates records under that lock. No publication is in flight during the read. Record names are
`<engine ID>-<32 lowercase hex>`. Publication debris names are
`.<record name>.tmp-<32 lowercase hex>`. Cleanup classifies each entry by its exact name and
`Lstat`, never following symlinks. Debris must be a regular file owned by the state owner or hibd's
effective UID, since a crash can happen before chown. Its contents are not read.

A real run removes all debris and syncs the directory before reading records. A record then has
one link. A dry run removes nothing and reports the debris. It allows one link plus the debris
entries that `os.SameFile` identifies as the same inode. Unexplained links fail closed.

An unexpected entry, including a debris-named symlink or directory, stops deletion and trimming.
The error gives its full path and says to move it out of `saves/`. Hibd never writes that entry,
so no restart is needed. A record-named file that cannot be read also stops cleanup. No mtime or
guessed contents can settle it. Once the host has restarted, nothing the old boot ran survives.
Then move exactly the file the error names out of `saves/`, preserving it for inspection:

```
sudo mkdir -p <state>/saves-damaged && sudo mv <path> <state>/saves-damaged/
```

The random token identifies one record from before the restart. The error supplies shell-quoted
paths for this procedure and names the machine ID from the file name, without a guessed start.

Readable unsettled records name the machine and give UTC RFC3339 times. The threshold is the end,
or start plus `saveCallTimeout` if the end is zero. A running machine needs to pause after that
threshold. A paused or unlisted machine needs to run and pause again. The next step is
`hib pause NAME`, or `hib exec NAME -- true` then `hib pause NAME`. Before the threshold, the
message says to wait until that time. Removal or a host restart can also settle the record.
The sweep retries in a minute and logs only when the record, listing state or threshold branch
changes, not as the current time advances within a branch.

**Save-safety residue.** Hibd's E2B client must reach the API directly, as the default
`http://127.0.0.1:3000` does. A dropped connection must cancel E2B's handler, not leave it running
behind a proxy. This design relies on the handler finishing its save transition before answering,
pauses being unable to start during a save, kill soft-deleting the machine's slot, and E2B's
70-second request deadline. It also relies on the node publishing the checkpoint's resumed run
before upload, with the old lifecycle removed atomically. A later pause then captures that run,
including the failed build it may read. The write deadline is 75 seconds. The ten-minute client
bound is a backstop. SIGPIPE on closed stdout or stderr can still end hibd. The persisted record,
not process survival, protects cleanup. Host clock jumps between a recorded end and a later pause
build can defeat that time comparison. Save records do not serialize external direct API saves.

**Candidates** are the build directories cleanup keeps that aren't roots and hold only a
machine's pauses: ready, unchanged for `MinAge`, put in at least one slot and only in machines'
slots, and no root's base build (its header's `BaseBuildId`). M9 never trims an image's build.

**Scope.** `HIBD_TRIM` in `/etc/hibernaut/hibd.env` limits candidates, never roots:

- `e2e`, the default until M9 has run on real machines: only builds put only in the slots of
  machines E2B lists as e2e test machines, named `e2e-*` or labeled `hib-test=e2e`, that no root
  outside the scope maps data in or has as its base. A root is in the scope if it's put only in
  those slots. So when an image is saved from an e2e machine, the builds it reads stay whole,
  and trimming changes no file that anything but e2e machines reads.
- `all`: every machine's. It trims Martin's `test` and `demo`, so it waits for his OK and a
  backup of `test` (below).
- `off`: no trimming. Any other value counts as `off`, and cleanup says so.

**Headers** are read by a new strict parser, `internal/snapheader`, because trimming relies on
them being exact. A header is valid only if:

- its version word is 1, 2 or 3, with no flag bits set. E2B's compressed formats, 4 and up, lay
  files out differently.
- its block size is a power of two of at least 4096, and its size at most `math.MaxInt64`
- its build is its directory's
- its mappings, sorted by offset, cover `[0, size)` exactly: no gap, no overlap, no overflow.
  Zero-length mappings are skipped. An empty list maps the whole size to its own build, as in
  E2B. A gap never falls back to the base build.
- every offset, length and storage offset is a multiple of 4096

Deletion keeps M2's lenient `cleanup.ParseHeader`, so a header trimming rejects never stops
deletion. leakscan keeps its own parser in M9.

**Fail closed.** Cleanup checks every root before it punches anything, and trims nothing in this
run, saying `not trimming: <reason>`, if:

- a root has no directory, however old. E2B keeps a template it has started in its cache, and
  can run from it with the files gone, reading other builds through the cached headers.
- a root's `memfile.header` or `rootfs.ext4.header` is missing or invalid. E2B's headerless
  legacy builds and filesystem-only snapshots aren't supported: M2's deletion already stops on
  them.
- a root maps data in a build that has no directory, or whose file (`memfile` or `rootfs.ext4`)
  isn't a regular file, ends before the mapping does, or has an invalid header of its kind. A
  root's own file has to exist only if its header maps data to it: a pause that changed no
  memory can write a `memfile.header` but no `memfile`.
- the storage isn't on ext4 (`statfs`)

Deletion goes ahead either way, as in M2.

**The plan** is pure. For each candidate's `memfile`, the ranges to keep are the union, over
every root, of `[storage offset, storage offset + length)` for each mapping in the root's
`memfile.header` whose build is the candidate. Likewise `rootfs.ext4` from `rootfs.ext4.header`.
The rest of the file, rounded inward to the filesystem's block size after the union, is punched.
A candidate no root maps loses all of both files. A 4 MiB chunk can be part kept and part
punched: E2B fetches whole chunks but reads only the mapped parts.

**Punching** goes through file descriptors:

1. Open the storage directory, then the build's, with `O_DIRECTORY|O_NOFOLLOW`, and the file with
   `openat(O_RDWR|O_NOFOLLOW|O_CLOEXEC)`.
2. Leave the file, with its reason, unless `fstat` says it's still the regular file the plan
   read: one link, and the same inode, size and mtime.
3. Punch only where the file has data (`SEEK_DATA`, `SEEK_HOLE`), checking for cancellation
   between punches.
4. Set the mtime back with `futimens` on the same descriptor, leaving the access time, so
   "changed in the last hour" still means E2B changed it.
5. Measure what was freed from the file's allocated blocks before and after.

Stopping halfway is safe: what was punched was unread, and a file whose mtime wasn't set back is
a root for an hour, which only keeps more.

**One cleanup at a time.** `hibd cleanup` and the daemon's sweep hold `cleanup.lock` in hibd's
state directory (`tryLock`) from reading E2B's listing to the last punch. A sweep that finds it
held tries again in a minute. `hibd cleanup` waits for it, and says so once.

**Time.** The sweep's 30 s budget (`sweepTimeout`) covers listing and deletion, as in M2.
Trimming gets two minutes of its own (`trimTimeout`). A sweep that runs out of it says how much
it trimmed, and tries again in a minute. The next run picks up where this one stopped, since it
punches only where files still have data. `hibd cleanup` has no budget.

**Output**, after M2's lines, with sizes as `hibapi.FormatMB` writes them:

```
would trim <build> memfile: keeps 64 MiB of 410 MiB, frees about 346 MiB
would trim about 2.4 GiB in 18 files
nothing to trim
not trimming: <reason>
trimmed <build> rootfs.ext4: freed 120 MiB
left <build> memfile: <reason>
trimmed 2.4 GiB in 18 files
```

`hibd cleanup -n` prints a `would trim` line per file and the total, `nothing to trim`, or `not
trimming`. A real run prints a line per file it trims or leaves, and the total if it trimmed any.
A dry run's sizes are estimates from the files' extents. A real run's are the blocks it freed.
Before those lines, the command may say `waiting for another cleanup` or
`waiting for an image save or fork to finish`. Settled save records produce
`cleared settled save record <file> for <name>. Check it with: hib ls.`, or the same line starting
with `would clear` in a dry run. Publication debris produces
`removed save publication debris <path> for <machine ID>. Check machines with: hib ls.`, or the
same line starting with `would remove` in a dry run. An unsettled, damaged or unexpected save
entry instead stops cleanup with its problem and one next step.

**Why it's safe.**

1. E2B uses a build whole only to start a machine or a build from it, to resume it, and while a
   machine runs from it, until its pause is done. Each such build is a root.
2. Every other read goes through a root's headers, and trimming keeps every range they map.
3. A pause after the plan maps a subset of what its parent mapped, which the plan kept, plus its
   own new build, which isn't a candidate.
4. E2B changes an old build's files only by replacing them. An upload, which retries for up to
   two hours, reads its own diff cache and renames the file into place, and the prefetch harvest
   rewrites only the new build's metadata. So "unchanged for an hour" doesn't mean done, but a
   replaced file is whole, and punching checks it's still the file the plan read.
5. Headers, snapfiles, metadata and file sizes stay. Every check fails closed, and a file that
   changed after the plan is left.

**What's bounded.** After each successful cleanup, a machine's old builds hold only what its
roots map: its live memory and disk data, plus what its pauses of the last hour wrote. While a
machine that hasn't paused yet runs, they also keep what they wrote since an hour before it
started. Not bounded: the number of build directories (one a pause, each with 30 to 130 KB of
headers and snapfile), how big headers grow, saved images, E2B's chunk cache in
`/var/lib/e2b/build`, and how long cleanup waits while E2B is busy. Merging a chain into one
build is for a later milestone.

**Before `all`.** Copy the builds `test`'s latest snapshot maps, sparsely, outside E2B's storage,
with a manifest: the machine, its latest build, and each file's size and SHA-256. Keep the copy
until `test` resumes after a real trim and a host restart.

## Tasks

- [x] Measure (S0): `TestPauseCycles` rewriting a 64 MiB file of random data before each pause,
      synced to disk or not. Its numbers are in Why.
- [x] `internal/snapheader`: the strict parser.
- [x] Cleanup: the machine roots, root accounting, candidates and the scope. Read under
      `hibd.lock` and settle persistent save records before deleting or trimming.
- [x] Saves: uncancellable dispatched calls with a ten-minute backstop, successful-save
      accounting, and private save records written before dispatch and ended before restoration.
      Recover publication debris and join record errors without changing the save's outcome.
- [x] hib's E2B client without E2B's fork endpoint: an overlay `make generate` applies.
- [x] The plan, and punching on Linux (`trim_linux.go`, with a `trim_stub.go` that says trimming
      needs Linux). `hibd cleanup -n` says what it would trim.
- [x] `cleanup.lock`, the trim budget, and `HIBD_TRIM` in hibd's usage.
- [x] Sentinels in `TestPauseCycles`, and `TestTrim`.
- [x] On the host: `make test-host-remote`, deploy, `sudo hibd cleanup -n` (read-only), then
      `make e2e-slow`.
- [x] README and spec §5: what a machine's snapshots take on disk, what's bounded and what isn't.
- [ ] Optional, not needed for Done: E2B's own options, each turned on by patching its default in
      the pinned E2B and measured with `TestPauseCycles`: `memfile-diff-dedup`,
      `guest-pause-reclaim` and `collapse-envd-heap`.

## Tests

Unit:

- [x] `snapheader`: versions and flag bits, block sizes, sizes, alignment, another build's header,
      a gap, an overlap, an overflow, a mapping past the size, an empty list, zero-length
      mappings, cut-short mappings.
- [x] Roots, on an injected clock: a running machine keeps its builds from an hour before it
      started, all of them if its start is unknown. A machine E2B lists as paused before its
      pause's build exists, whose run started from a snapshot replaced over an hour before, keeps
      that snapshot. So does one whose slot also holds an image save's build from that run, made
      over an hour before. A paused machine keeps its last run's input for an hour after its
      pause's build finished. A replaced snapshot stays a root for an hour. While a machine with no
      snapshot of its own runs, as a native fork would, every other machine's slot that isn't
      removed keeps its builds from an hour before it started. Startup history, saved images and
      the layer cache stay roots in every scope. Deletion keeps the roots.
- [x] Locked reads: listing, SQL, cache, directories and records happen under `hibd.lock`.
      Deletion and trimming happen after release. Both locks release on errors and cancelled
      waits. Sweep contention retries in a minute without `not trimming`. The command logs its
      wait once. An unlocked resume during the read keeps the latest and warm-cache roots whole.
- [x] Save outcomes: pre-dispatch cancellation writes nothing and sends nothing. Cancellation
      during the call leaves a live, bounded context. The record exists before dispatch and ends
      before restoration. Only a 201 with a good readback succeeds. An HTTP error, a 201 with
      failed readback and a transport failure do not. A 404 or 409 removes its record. Write
      failures inhibit cleanup but do not skip restoration or turn success into failed-save
      cleanup. The ten-minute backstop leaves an ended, unsuccessful record.
- [x] Record settlement: each proof above, no running-only proof for unsuccessful or legacy
      answered records, no proof from a paused listing, an image save, an older build or a
      pending build. Interrupted saves use start plus the call timeout. Dry runs keep records.
      New Managers and Apps still see records after a process restart. Unreadable and unsettled
      records stop all changes.
- [x] Failed input: a later ready pause keeps the failed save build through its headers.
      Trimming never punches that failed build and preserves ranges in its other sources.
      Without a later pause, the unsettled record stops deletion and trimming.
- [x] Publication debris, in real and dry runs: partial and complete staging files, replacement
      debris with an old record, and a record linked to its staging file. Real runs remove
      debris. Dry runs account for same-inode links. Unexplained links, unexpected names and
      staging-named symlinks or directories fail closed with the correct manual procedure.
      Debris owned by the state owner or hibd's effective UID is accepted.
- [x] Recovery messages: running, paused and unlisted machines, before and at or after the
      threshold. Times are UTC RFC3339 and stable within a branch. Damaged records give the exact
      path and post-restart procedure without a zero time. Every message names the machine and
      ends with one next step.
- [x] Cross-process, with real flock on macOS and Linux: one process saves against a gated E2B
      handler while another runs cleanup on the same state directory. Cleanup logs its wait
      once, makes no List call until the save ends, then clears the settled record and completes.
- [x] Root accounting: a root without a directory stops trimming, however old. So does a root
      header missing or invalid, a headerless legacy build, a filesystem-only snapshot, or a
      source missing, short or with an invalid header. A root whose header maps nothing to its
      own build, which has no file of that kind, doesn't.
- [x] Candidates and scope: `e2e` takes only e2e machines' builds, and none that a root outside
      the scope maps data in or has as its base, such as a saved image's ancestors. `all` takes
      those. `off` and unknown values take none. Never a root, a base, an image's build, or an
      unfinished or recently changed build.
- [x] The plan: the union over roots comes before the complement. Kept ranges apart within one
      4 MiB chunk. Zero (nil) mappings. Storage offsets that differ from logical ones. A source
      shared by several roots and generations. `memfile` and `rootfs.ext4` apart. Holes rounded
      inward. A candidate no root maps loses everything.
- [x] The sweep: trimming has its own budget, a sweep that runs out of it tries again in a
      minute, and runs cut short again and again still finish the trim.
- [x] hib's E2B client has no method that forks a machine natively.

Host (`-tags host`, on ext4 under `/var/tmp`, in `make test-host-remote`):

- [x] A storage directory of several generations of random data: every root's memory and disk,
      read through its headers, has the same SHA-256 before and after a real trim. Fewer blocks
      are allocated, sizes, mtimes and access times are as before, and a second run trims
      nothing.
- [x] A file swapped for another, resized, touched or hard-linked after the plan is left, with
      its reason.
- [x] Cancelled halfway: every root still reads the same, and the next run finishes.
- [x] Not ext4 (`/dev/shm`): nothing is trimmed, and cleanup says why.

End-to-end:

- [x] `TestPauseCycles` writes two sentinels before the first pause (64 MiB of random data in
      `/dev/shm`, which is memory, and a synced 64 MiB file on disk), and checks their SHA-256
      after every resume, reading the disk one after dropping the guest's page cache. It stays a
      measurement.
- [x] `TestTrim` (slow). Machine A, with the sentinels, pauses 10 times with the random
      workload. Machine B pauses twice, and an image is saved from it. Once their build
      directories hold all their files, the test records each of A's and B's build files: size,
      allocated blocks, and the SHA-256 of headers, snapfile and metadata. Then it calls
      `t.Parallel()`, so the hour passes while the rest of `make e2e-slow` runs
      (`TestBusyPastAnHour` takes 65 minutes). Afterwards, on fresh contexts, holding an SSH
      session so the host doesn't deallocate:
      1. It runs `hibd cleanup -n` until A's old builds are candidates, or the daemon has
         trimmed them, with a deadline.
      2. It runs `hibd cleanup`, and checks the result, not the run. A's builds take less than
         half the disk they took after the pauses, and no more than the latest snapshot maps,
         plus headers, snapfiles and rounding. Every file keeps its size, and headers, snapfiles
         and metadata their SHA-256. The builds of B's that the image reads keep every block.
      3. With A still paused, the host restarts. E2B empties its chunk cache when it starts, so
         A's resume reads the trimmed snapshot cold. The sentinels must match, the disk one read
         after dropping the guest's page cache.
      4. They must match again after a pause, a restart and a resume, and then in a saved image
         of A, a fork of A, and a machine made from the image.

## Done when

- [x] `make lint test`, `make test-host-remote`, CI and `make e2e-slow` are green. CI is parked,
      so `make lint test` stands in for it.
- [x] `sudo hibd cleanup -n` on the host gives no reason not to trim, and would trim nothing of
      `test` or `demo`.

## Changes

- 2026-10-05: Added the Design section, after three advisor reviews. The first plan merged a
  chain into one new build for E2B to resume from, which meant writing E2B's database and cache.
  The second deleted the builds the latest headers don't name, but S0 showed they name most of
  them, and hib's save and fork checks expect every build in a chain. So M9 only punches holes,
  and protects what a running machine may read for as long as it runs. The third review and its
  follow-up added a machine E2B is still pausing, image saves and native forks, warm template
  caches, builds an e2e machine shares with a saved image, and a baseline taken before the hour.
- 2026-10-05: The v4 re-review found the GET recheck unsafe because E2B falls back to its stale
  database snapshot on any store error. Replaced it with a read under the save lock and persistent
  per-save records. Dispatched saves no longer inherit caller cancellation, and the engine
  distinguishes an HTTP answer from no answer. Records inhibit cleanup until a later lifecycle
  fact proves the save over. Documented direct API access, E2B's deadlines, process death and
  host clock jumps as the remaining assumptions.
- 2026-10-05: The v5 review found that an HTTP error answer can leave a machine running from a
  failed build while the node continues uploading. Running-only settlement now requires a
  successful save. Legacy answered records remain unsuccessful. Cleanup recovers publication
  debris, accounts for dry-run hard links and gives exact-file manual recovery for damaged or
  unexpected entries. Recovery messages follow the listing and time threshold. Record-end
  failures no longer skip restoration or choose the save's success path.
- 2026-10-05: Done. `make e2e-slow` passed on the host. In `TestTrim`, A's ten snapshot
  directories took 3.9 GiB after the pauses and 553 MiB after the trim, within the bound of
  561 MiB. The daemon's sweep had trimmed them before the test's own cleanup ran. The sentinels
  matched after two host restarts, and in an image, a fork and a machine made from the image. The
  unit tests of `internal/hibd`, `internal/cleanup` and `internal/machines` passed on the host
  too, with a umask of 022. `HIBD_TRIM` stays `e2e` until `test` is backed up. E2B's own options
  weren't tried.
