# M0 Foundations

Status: **in progress** · Spec: §3, §5 (The host), §7, §8 · Spike: S4 (answered)

> Everything is built, and `make e2e-slow` is green on the real host. M0 closes when CI's first
> run on GitHub is green, which needs a push.

**Goal:** a Go repo with the whole test pipeline working, and a host that runs E2B as services
and never loses a machine when it shuts down.

## Tasks

**Repo and tooling**

- [x] Go module `github.com/solev/hibernaut` with `cmd/hib`, `cmd/hibd`, `internal/`, `e2e/`.
- [x] Makefile: `build`, `lint` (gofmt, go vet, staticcheck, shellcheck), `test`, `test-host`,
      `generate`, `deploy`, `up`, `down`, `e2e`, `e2e-slow`.
- [x] GitHub Actions: `lint`, `test` and a check that generated code is current, on every push.
      `test-host` moved to M3 (see Changes).
- [x] `make deploy`: build `hibd` for linux/amd64, copy it and the host scripts and units to the
      host, and install them. Never restarts E2B or touches machines.
- [x] `make e2e`: start the host if it's asleep, deploy, install E2B if needed (`azure/up.sh`), then
      run `go test -tags e2e ./e2e/...` from the laptop through an SSH tunnel.
- [x] E2B's OpenAPI spec and envd proto vendored in `third_party/e2b/` with the pin, license and
      update steps.

**Engine**

- [x] `Engine` interface with only what M0–M2 need: create, get, list, pause, resume, kill, set
      timeout, metrics, and exec (moved up from M2).
- [x] E2B client generated from E2B's OpenAPI spec at the pinned SHA, wrapped behind `Engine`.
      Exec uses envd's process API, generated from its proto.
- [x] In-memory fake `Engine`.
- [x] One contract test suite (`internal/engine/enginetest`) that runs against both the fake and
      real E2B, so the fake can't drift.

**Host**

- [x] systemd units for everything `vm/start-e2b.sh` started (replacing tmux), in dependency
      order, enabled on boot: `e2b-infra` (the containers), `e2b-api`, `e2b-orchestrator`,
      `e2b-client-proxy`. `start-e2b.sh` is gone.
- [x] E2B storage moved from `~/infra/packages/orchestrator/tmp/` to `/var/lib/e2b`. Existing
      templates moved with it.
- [x] The `hibd-pause-all` guard unit, in place of `hibd` under systemd: ordered after E2B, so it
      stops, and pauses machines, before E2B does (see Changes).
- [x] `hibd pause-all`: pauses every running machine and waits (bounded). Runs from the guard's
      stop hook and from `quiesce`.
- [x] `quiesce` (new): pause-all, stop E2B, `sync`. Runs before every deallocate (idle, nightly,
      `make down`) and before `install-e2b.sh` rebuilds E2B. If it can't pause every machine, the
      VM stays up.
- [x] idle-deallocate: a running machine counts as activity; quiesces before deallocating.
- [x] Nightly timer in the VM at 20:00 UTC: quiesce, then deallocate through the managed identity
      (the same call idle-deallocate makes).
- [x] Azure's auto-shutdown moved an hour later (`SHUTDOWN_UTC`, 21:00 UTC) and stays only as a
      backstop.

## Tests

Unit (fake engine, fake clock):

- [x] Engine contract suite on the fake.
- [x] `pause-all` pauses running machines, skips paused ones, and gives up after its time limit.
      Also: machines that start meanwhile, retries, and a concurrency limit.
- [x] The E2B engine against a fake E2B server: requests, paging, error mapping, exec, metrics.
- [x] `hibd`'s commands and config.
- [x] `quiesce` and idle-deallocate with fake `hibd`, `systemctl` and `sync`: steps run in order,
      and the VM stays up when pausing fails.

End-to-end:

- [x] `TestEngineContract`: the same contract suite against real E2B.
- [x] `TestPauseAll`: `hibd pause-all` on the host pauses a running machine, leaves a paused one
      alone, and the machine resumes with its file.
- [x] `TestSurvivesHostRestart` (slow, S4):
  - create a machine, write a file, start a counter process;
  - `reboot`: reboot the VM. `nightly-deallocate`: run the real nightly unit (quiesce →
    deallocate), then start the VM (`azure/start.sh`);
  - E2B comes up on its own; the journal shows the machine was paused on the way down; it resumes
    with the file and the same counter process, still counting.

## Done when

- [x] `make lint test` and `make e2e-slow` are green.
- [ ] CI is green on GitHub. (Every job passed in a dry run on the Ubuntu 24.04 VM; the first real
      run needs a push.)
- [x] README: tmux steps replaced by systemd; nightly shutdown described as the in-VM timer.
- [x] Spec: S4 marked answered.

## Changes

- **A guard unit instead of a `hibd` service.** M0's `hibd` is a command, not a daemon, so a
  `hibd` service would have nothing to run. `hibd-pause-all.service` (oneshot, stays active) runs
  `hibd pause-all` when it stops. It starts after E2B's units, so on shutdown it stops before
  them. The `hibd` daemon arrives with M2's poll loop.
- **E2B's pause returns before the snapshot is written.** The orchestrator writes snapshots in the
  background, and waits for them only when it stops cleanly. So every deallocate runs `quiesce`
  (pause all → stop E2B → `sync`), not just pause-all. The orchestrator gets
  `TimeoutStopSec=3min` to finish writing.
- **`Engine.Exec` moved from M2 to M0.** The e2e tests need it to write files and start processes
  in machines.
- **CI's `test-host` job moved to M3**, with the first host test (iptables). Until then it would
  only rerun `test`. The Makefile target stays.
- **CI checks generated code is current** (`make generate`, then no diff), and uses Go `1.26.x`:
  setup-go sets `GOTOOLCHAIN=local`, and the pinned buf needs Go 1.26.7 or later, while `go.mod`
  says 1.26.2.
- **`TestSurvivesHostDeallocate` became `TestSurvivesHostRestart`**, with a reboot case that tests
  the guard. `TestPauseAll` is new.
- **Installing E2B:** its debug builds with the race detector and debug flags off; database
  migrations and seed data at install time, not at every start. `install-e2b.sh --if-needed`
  reinstalls only when the pin or the install files change. `wait-e2b.sh --start` starts E2B;
  without `--start` it only waits, so tests can check that E2B came up on its own.

Answered along the way:

- **S4: yes.** Machines survive a reboot and a deallocate with their files and processes, and E2B
  starts on boot. The guard paused a machine in 0.9 s during a reboot, and it resumed in 1.0 s.
  After the nightly deallocate and a cold start, it resumed in 2.9 s.
- **Azure's deallocate is a clean guest shutdown** (the journal shows `poweroff.target`), so
  systemd stop hooks run.
- **E2B's metrics work in local mode.** The first sample takes a few seconds.
- A process started with `setsid nohup … &` keeps its PID across pause and resume.

Still open:

- E2B's seeded tier (`base_v1`) caps a machine at 1 hour from its start: `SetTimeout` can't push
  its end further. M2 has to lift that (see M2).
- Is `TimeoutStopSec=3min` enough to write a big machine's memory snapshot?
- How long does Azure wait for the guest to shut down? Only the 21:00 backstop depends on it,
  since it skips `quiesce`.
