# M9 Snapshot compaction

Status: **not started** · Spec: §5 (Snapshot compaction) · Needs: M2 (snapshot cleanup)

**Goal:** a machine's disk use stops growing with every pause, however often it pauses.

It can come any time after M2, and should come before the host's disk fills: until then,
`hib rm` is the only way to free a machine's snapshots.

## Why

M2's `TestPauseCycles` paused and resumed an idle `dev` machine 20 times (2026-10-02). Resume
time stayed at 0.7–1.5 s, but every pause wrote about 280 MiB, and the latest snapshot still
needed 13 of the 20 (3.6 GiB), one more with every pause since the 12th. A snapshot is a chain of
diffs that E2B never merges, so cleanup (M2) can't free them.

## Tasks

- [ ] Try E2B's own options first. Its local setup leaves them all off: they're LaunchDarkly
      flags, which serve their defaults without a key, and no env var sets them. Turn each on by
      patching its default in the pinned E2B (`install-e2b.sh`), and run `TestPauseCycles`: the
      diff per pause, the disk the latest snapshot needs, and resume time.
  - `memfile-diff-dedup`: dedups each pause's memory diff in 4 KiB pages. Its budget keys copy
    parent pages into the new diff, so older diffs can stop being needed.
  - `guest-pause-reclaim`: fstrim, sync, drop_caches and compact_memory in the machine before a
    pause.
  - `collapse-envd-heap`: envd packs its own memory into fewer huge pages before a pause.
- [ ] If the latest snapshot still needs more with every pause, merge the chain. Find out whether
      E2B can write a full snapshot (say once the chain passes a length). If not, design how
      `hibd` writes one build holding every block the latest headers name, and swaps it in.
      Cleanup (M2) then deletes the old diffs.
- [ ] README and spec: the disk a machine needs, and how it stays bounded.

## Tests

Unit and integration tests come with the design (second task).

End-to-end:

- [ ] `TestPauseCycles` (slow) fails if the disk the latest snapshot needs keeps growing: after
      20 pauses it's within a bound set from the first task's measurements, and resume time
      stays flat.

## Done when

- [ ] `make lint test`, CI and `make e2e-slow` are green.

## Changes

(none yet)
