# M2 Idle and wake

Status: **not started** · Spec: §5 (Idle and wake)

**Goal:** idle machines pause themselves. Touching one wakes it with its processes still running.
Busy machines never pause.

## Tasks

- [ ] Create machines with `autoPause: true`, memory snapshots and `autoResume: {policy: any}`.
- [ ] Idle timeout 15 minutes by default; `hib new --idle 30m`, where `0` means never pause.
- [x] Run commands in machines through envd (`Engine.Exec`). M3 uses it too. (Done in M0.)
- [ ] Lift E2B's 1-hour cap. The seeded tier (`base_v1`) has `max_length_hours = 1`, and
      `SetTimeout` can't push a machine's end past its start plus that
      (`packages/api/internal/orchestrator/keep_alive.go`). Raise it at install time, and find out
      whether a resume restarts the clock.
- [ ] `hibd` runs as a daemon under systemd, after E2B (M0 has only the pause-all guard).
- [ ] Busy detector, any of:
  - [ ] an SSH session through the bridge (`hibd` counts its own bridges),
  - [ ] CPU above a small threshold (E2B metrics),
  - [ ] an agent process alive (`pgrep` through envd; configurable list: `codex`, `copilot`, …).
- [ ] Poll loop: every 60 s, only machines E2B reports as running. Busy → push the timeout back.
- [ ] `hib` resumes explicitly before its own commands.
- [ ] `hib ls` shows running / paused and time left until pause.

## Tests

Unit (fake clock, no sleeping):

- [ ] Busy → timeout pushed back. Idle → left alone.
- [ ] Each busy signal alone keeps a machine awake.

Integration:

- [ ] Poll loop against the fake engine never touches a paused machine (the fake fails the test if
      it's called).

End-to-end:

- [ ] `TestIdlePauseWake`: `--idle 1m`; start a tmux session running a counter; disconnect; wait
      until E2B reports paused; `ssh` in → same tmux session and PID, counter moving again. Logs the
      resume time.
- [ ] `TestBusyStaysAwake`: `--idle 1m`; three machines, each kept busy one way (open SSH session,
      CPU loop, a process named `codex`); all still running after 3 minutes.
- [ ] `TestWakeOnExec`: `hib exec` on a paused machine works on the first try.
- [ ] A busy machine stays running past E2B's default 1-hour cap (slow).

## Done when

- [ ] `make lint test`, CI and `make e2e` are green.
- [ ] README explains idle and wake.

## Changes

- From M0: `Engine.Exec` is already done, and this milestone also turns `hibd` into a daemon and
  lifts E2B's 1-hour cap.
