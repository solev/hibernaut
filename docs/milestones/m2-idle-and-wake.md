# M2 Idle and wake

Status: **done** · Spec: §5 (Idle and wake, Room on the host)

**Goal:** idle machines pause themselves. Touching one wakes it with its processes still running.
Busy machines never pause. A full host says so plainly, and snapshots don't pile up on its disk.

## Tasks

**Idle and wake**

- [x] Create machines with `autoPause: true`, memory snapshots and auto-resume off (see Changes).
- [x] Idle timeout 15 minutes by default; `hib new --idle 30m`, where `0` means never pause.
- [x] Run commands in machines through envd (`Engine.Exec`). M3 uses it too. (Done in M0.)
- [x] Lift E2B's 1-hour cap. (Done in M1: `install-e2b.sh` raises the seeded tier's
      `max_length_hours` from 1 to 720, and a resume restarts that clock, because E2B gives a
      resumed machine a new start time: `packages/api/internal/orchestrator/create_instance.go`.)
- [x] `hibd` runs as a daemon under systemd, after E2B (M0 has only the pause-all guard).
- [x] Busy detector, any of:
  - [x] an SSH session through the bridge (the bridge renews the timeout while it's open),
  - [x] CPU above a small threshold (10% of a CPU, from the machine's cgroup; see Changes),
  - [x] an agent process alive (`pgrep` through envd; `codex`, `copilot`, `claude`;
        `HIBD_AGENTS`).
- [x] Poll loop: every 15 s, only machines E2B reports as running. Busy → push the timeout back.
- [x] `hib` resumes explicitly before its own commands (the bridge resumes for `ssh` and
      `hib exec`).
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
- [x] Snapshot cleanup. E2B never deletes snapshot files, not even a removed machine's. `hibd`
      deletes the build directories in `/var/lib/e2b/local-template-storage` that nothing can
      resume from any more. It keeps every machine's and image's current build, the older builds
      their headers point into (a snapshot is a chain of diffs), and anything under an hour old,
      which may still be being written. It runs after `hib rm` and hourly.
- [x] Find out whether E2B bounds its cache in `/var/lib/e2b/build` (5.7 GB now). If it doesn't,
      clean it up too. (It does: it deletes files after 25 hours, evicts at 85% disk, and empties
      the cache when the orchestrator starts.)

## Tests

Unit (fake clock, no sleeping):

- [x] Busy → timeout pushed back. Idle → left alone.
- [x] Each busy signal alone keeps a machine awake.
- [x] Room: from a `/proc/meminfo` reading and the free disk, a machine fits or doesn't, and the
      refusal names the running machines.
- [x] Cleanup, from a listing of builds and their headers: keeps every current build, everything
      its headers point into, and anything under an hour old; deletes the rest.

Integration:

- [x] Poll loop against the fake engine never touches a paused machine (the fake fails the test if
      it's called).
- [x] With the fake host full, `new` and the bridge's resume are refused with the room message and
      create nothing.
- [x] The daemon cleans up when it starts and when a machine is removed, and logs a cleanup that
      failed.
- [x] The daemon never calls E2B without a deadline.
- [x] Cleanup on a host directory skips the cache files E2B hasn't finished writing.

End-to-end:

- [x] `TestIdlePauseWake`: `--idle 1m`; start a tmux session running a counter; disconnect; wait
      until E2B reports paused; `ssh` in → same tmux session and PID, counter moving again. Logs the
      resume time.
- [x] `TestBusyStaysAwake`: `--idle 1m`; three machines, each kept busy one way (open SSH session,
      CPU loop, a process named `codex`); all still running after 3 minutes.
- [x] `TestWakeOnExec`: `hib exec` on a paused machine works on the first try.
- [x] A busy machine stays running past E2B's default 1-hour cap (slow).
- [x] `TestHostFull`: pause one machine, then start machines until no more fit. One more
      `hib new`, and `ssh` to the paused machine, are both refused within a second with the room
      message, and the running machines are untouched. Once one of them is paused, the `ssh`
      works.
- [x] `TestSnapshotCleanup`: `hib new` → pause → resume → pause → `hib rm`. Its build directories
      are gone from the host, and other machines and the image still start and resume.
- [x] `TestPauseCycles` (slow): one machine paused and resumed 20 times; logs the disk it uses and
      its resume time per cycle. E2B never compacts a machine's chain of snapshot diffs, so if
      either keeps growing, plan compaction.

## Done when

- [x] `make lint test`, `make e2e` and `make e2e-slow` are green (CI is parked; `make lint test`
      alone for now).
- [x] README explains idle and wake, how many machines fit at once, and what happens when the
      host is full.

## Changes

- From M0: `Engine.Exec` is already done, and this milestone also turns `hibd` into a daemon.
- From M1: the 1-hour cap is already lifted (see Tasks).
- **Room on the host.** Added after probing the real host with throwaway machines (2026-10-02).
  Memory is a hard limit that fails safely but with a confusing error, and CPU is shared. Disk
  fills up: E2B never deletes snapshot files, and a pause that runs out of disk still stops the
  machine. Details are in spec §5 (Room on the host). When a start or resume fails for any
  reason, `hibd` checks the room again rather than match E2B's error; a machine that resumed but
  couldn't be reached isn't reported as a full host.
- **No auto-resume.** The task's `autoResume: {policy: any}` isn't E2B's API (it's
  `{enabled}`), and auto-resume stays off: traffic to a paused machine doesn't wake it, so only
  `hibd`'s users can, and only after the room check. The bridge resumes for `ssh` and `hib exec`.
- **Poll every 15 s, not 60 s**, so a 1-minute idle time is kept to within about 15 s.
- **CPU from the machine's cgroup, not E2B's metrics.** The metrics API returns every sample
  since the machine started (one per 5 s), so each look would read more.
- **The SSH busy signal is the bridge's keepalive.** While a bridge is open it renews the
  machine's timeout, and closing it starts the idle clock, so `hibd daemon` needn't count
  bridges. The shortest idle time is 1 minute.
- **idle-deallocate** already counts a running machine as activity, so a busy machine keeps the
  host up as well.
- **Snapshot cleanup** reads E2B's database, the machines and the build headers, as spec §5
  says. Beyond the task:
  - a removed machine's builds go at once, not after an hour;
  - a build can be in more than one image or machine, under tags: E2B's
    `env_build_assignments` says which, and when it put the build there. For each image tag,
    cleanup keeps the build E2B put there last, which is what E2B starts machines from, and
    the one finished last;
  - it deletes nothing while E2B is writing a build (one in progress for under 6 hours), or if
    it can't read a header it needs. Image builds time out after an hour, and E2B never
    finishes a build it stopped writing (say it crashed mid-pause), so waiting for every one
    would stop cleanup for good;
  - a removed image's builds go once no running machine may have started from them: the image
    was removed over an hour before the first of those machines started (E2B checks an image
    before it starts a machine, and stamps the start once the machine runs);
  - it keeps the image builds a running machine may have started from, and the builds E2B's
    layer cache points to. It skips the cache files E2B hasn't finished writing, which a crash
    can leave half-written;
  - it reads headers of versions 1 to 3 only. Later ones are compressed, which E2B's local
    setup doesn't turn on; if a build it keeps has one, it deletes nothing rather than guess.
  - "After `hib rm`" is the daemon noticing a machine gone, so a machine removed some other way
    counts too. It also runs when the daemon starts, and `hibd cleanup [-n]` runs it by hand.
  - It runs hourly, not daily: the daemon misses a machine created and removed between two of
    its looks, and `hib rm` can't clean up itself (`hib` runs `hibd` as `azureuser`, who can't
    read E2B's storage).
- **Every call to E2B has a deadline**: 10 s for each of the busy watcher's, and 30 s for a
  cleanup's look. A stuck call would stop the daemon, and busy machines would pause.
- **`TestPauseCycles` found a machine's disk keeps growing** (20 pauses of an idle machine, on
  2026-10-02). Resume time stayed at 0.7–1.5 s, but every pause wrote about 280 MiB, and the
  latest snapshot still needed 13 of the 20 (3.6 GiB), one more with every pause since the
  12th. Cleanup can't free those, so compaction is planned as M9.
- **Known limit of cleanup:** it reads E2B's database, then deletes. If E2B put an old build
  back into an image or machine in between, cleanup could delete it. E2B's pauses, image builds
  and snapshots only ever put in new builds. Only its tags API puts in an existing one (the
  current build under another tag), and `hib` doesn't call it. M6 notes this.
