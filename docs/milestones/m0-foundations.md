# M0 Foundations

Status: **not started** · Spec: §3, §5 (The host), §7, §8 · Spike: S4

**Goal:** a Go repo with the whole test pipeline working, and a host that runs E2B and `hibd` as
services and never loses a machine when it shuts down.

## Tasks

**Repo and tooling**

- [ ] Go module `github.com/solev/hibernaut` with `cmd/hib`, `cmd/hibd`, `internal/`, `e2e/`.
- [ ] Makefile: `build`, `lint` (gofmt, go vet, staticcheck), `test`, `test-host`, `deploy`,
      `e2e`, `e2e-slow`.
- [ ] GitHub Actions: `lint` and `test` on every push; `test-host` on a Linux runner with sudo.
- [ ] `make deploy`: build `hibd` for linux/amd64, copy it and its unit files to the host, restart
      it.
- [ ] `make e2e`: start the host if it's asleep (`azure/up.sh`), deploy, then run
      `go test -tags e2e ./e2e/...` from the laptop.

**Engine**

- [ ] `Engine` interface with only what M0–M2 need: create, get, list, pause, resume, kill, set
      timeout, metrics.
- [ ] E2B client generated from E2B's OpenAPI spec at the pinned SHA, wrapped behind `Engine`.
- [ ] In-memory fake `Engine`.
- [ ] One contract test suite that runs against both the fake and real E2B, so the fake can't
      drift.

**Host**

- [ ] systemd units for everything `vm/start-e2b.sh` starts today (replacing tmux), in dependency
      order, enabled on boot.
- [ ] E2B storage moved from `~/infra/packages/orchestrator/tmp/` to `/var/lib/e2b`. Existing
      templates moved or rebuilt.
- [ ] `hibd` under systemd, ordered after E2B, so it stops first on shutdown.
- [ ] `hibd pause-all`: pauses every running machine and waits (bounded). Runs from `hibd`'s stop
      hook.
- [ ] idle-deallocate: a running machine counts as activity; runs `hibd pause-all` before
      deallocating.
- [ ] Nightly timer in the VM: `hibd pause-all`, then deallocate through the managed identity (the
      same call idle-deallocate makes).
- [ ] Azure's auto-shutdown moves an hour later (`SHUTDOWN_UTC`) and stays only as a backstop.

## Tests

Unit (fake engine, fake clock):

- [ ] Engine contract suite on the fake.
- [ ] `pause-all` pauses running machines, skips paused ones, and gives up after its time limit.

End-to-end:

- [ ] `TestEngineContract`: the same contract suite against real E2B.
- [ ] `TestSurvivesHostDeallocate` (slow, S4):
  - create a machine, write a file, start a counter process;
  - run the nightly path (pause-all → deallocate), then `azure/up.sh`;
  - all services come up on their own; the machine resumes with the file and the counter
    process.

## Done when

- [ ] `make lint test`, CI and `make e2e-slow` are green.
- [ ] README: tmux steps replaced by systemd; nightly shutdown described as the in-VM timer.
- [ ] Spec: S4 marked answered.

## Changes

(none yet)
