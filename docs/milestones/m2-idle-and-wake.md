# M2 Idle and wake

Status: **in progress** · Spec: §5 (Idle and wake, Room on the host)

**Goal:** idle machines pause themselves. Touching one wakes it with its processes still running.
Busy machines never pause. A full host says so plainly, and snapshots don't pile up on its disk.

## Tasks

**Idle and wake**

- [ ] Create machines with `autoPause: true`, memory snapshots and `autoResume: {policy: any}`.
- [ ] Idle timeout 15 minutes by default; `hib new --idle 30m`, where `0` means never pause.
- [x] Run commands in machines through envd (`Engine.Exec`). M3 uses it too. (Done in M0.)
- [x] Lift E2B's 1-hour cap. (Done in M1: `install-e2b.sh` raises the seeded tier's
      `max_length_hours` from 1 to 720, and a resume restarts that clock, because E2B gives a
      resumed machine a new start time: `packages/api/internal/orchestrator/create_instance.go`.)
- [ ] `hibd` runs as a daemon under systemd, after E2B (M0 has only the pause-all guard).
- [ ] Busy detector, any of:
  - [ ] an SSH session through the bridge (`hibd` counts its own bridges),
  - [ ] CPU above a small threshold (E2B metrics),
  - [ ] an agent process alive (`pgrep` through envd; configurable list: `codex`, `copilot`, …).
- [ ] Poll loop: every 60 s, only machines E2B reports as running. Busy → push the timeout back.
- [ ] `hib` resumes explicitly before its own commands.
- [x] `hib ls` shows running / paused, time left until pause, and the room left on the host.

**Room on the host** (see Changes)

- [x] Before `hibd` starts or resumes a machine (`hib new`, `ssh name.hib`, `hib exec`), it checks
      there's room:
  - [x] memory: free huge pages (`HugePages_Free` minus `HugePages_Rsvd` in `/proc/meminfo`)
        cover the machine's RAM;
  - [x] disk: free space on `/var/lib/e2b` covers pausing every running machine and this one
        (their RAM, plus a margin for what they wrote to disk), because a pause that can't be
        written still stops the machine.
- [x] No room → a clear error that names the running machines and says what to do:
      `hib pause NAME` frees memory, `hib rm` frees disk. It replaces E2B's
      `500 Failed to place sandbox … please retry`. Two starts can race the check, so when E2B
      refuses anyway, `hibd` checks again and gives the same message.
- [ ] Snapshot cleanup. E2B never deletes snapshot files, not even a removed machine's. `hibd`
      deletes the build directories in `/var/lib/e2b/local-template-storage` that nothing can
      resume from any more. It keeps every machine's and image's current build, the older builds
      their headers point into (a snapshot is a chain of diffs), and anything under an hour old,
      which may still be being written. It runs after `hib rm` and once a day.
- [ ] Find out whether E2B bounds its cache in `/var/lib/e2b/build` (5.7 GB now). If it doesn't,
      clean it up too.

## Tests

Unit (fake clock, no sleeping):

- [ ] Busy → timeout pushed back. Idle → left alone.
- [ ] Each busy signal alone keeps a machine awake.
- [x] Room: from a `/proc/meminfo` reading and the free disk, a machine fits or doesn't, and the
      refusal names the running machines.
- [ ] Cleanup, from a listing of builds and their headers: keeps every current build, everything
      its headers point into, and anything under an hour old; deletes the rest.

Integration:

- [ ] Poll loop against the fake engine never touches a paused machine (the fake fails the test if
      it's called).
- [x] With the fake host full, `new` and the bridge's resume are refused with the room message and
      create nothing.

End-to-end:

- [ ] `TestIdlePauseWake`: `--idle 1m`; start a tmux session running a counter; disconnect; wait
      until E2B reports paused; `ssh` in → same tmux session and PID, counter moving again. Logs the
      resume time.
- [ ] `TestBusyStaysAwake`: `--idle 1m`; three machines, each kept busy one way (open SSH session,
      CPU loop, a process named `codex`); all still running after 3 minutes.
- [ ] `TestWakeOnExec`: `hib exec` on a paused machine works on the first try.
- [ ] A busy machine stays running past E2B's default 1-hour cap (slow).
- [ ] `TestHostFull`: pause one machine, then start machines until no more fit. One more
      `hib new`, and `ssh` to the paused machine, are both refused within a second with the room
      message, and the running machines are untouched. Once one of them is paused, the `ssh`
      works.
- [ ] `TestSnapshotCleanup`: `hib new` → pause → resume → pause → `hib rm`. Its build directories
      are gone from the host, and other machines and the image still start and resume.
- [ ] `TestPauseCycles` (slow): one machine paused and resumed 20 times; logs the disk it uses and
      its resume time per cycle. E2B never compacts a machine's chain of snapshot diffs, so if
      either keeps growing, plan compaction.

## Done when

- [ ] `make lint test`, CI and `make e2e` are green.
- [ ] README explains idle and wake, how many machines fit at once, and what happens when the
      host is full.

## Changes

- From M0: `Engine.Exec` is already done, and this milestone also turns `hibd` into a daemon.
- From M1: the 1-hour cap is already lifted (see Tasks).
- **Room on the host.** Added after probing the real host with throwaway machines (2026-10-02).
  Memory is a hard limit that fails safely but with a confusing error, and CPU is shared. Disk
  fills up: E2B never deletes snapshot files, and a pause that runs out of disk still stops the
  machine. Details are in spec §5 (Room on the host).
