# Milestones

How we build Hibernaut v1, one milestone at a time. The design and the reasons behind it are in
the [spec](../spec.md): §7 is the plan, §8 is how we test.

v1's milestones are done: [M0 Foundations](m0-foundations.md), [M1 Machines](m1-machines.md),
[M2 Idle and wake](m2-idle-and-wake.md), [M3 Vault, egress proxy, leak scan](m3-vault-and-proxy.md),
[M4 GitHub](m4-github.md), [M5 AI tools](m5-ai-tools.md),
[M6 Golden images and fork](m6-images-and-fork.md),
[M7 GitHub device flow](m7-github-device-flow.md), [M8 Machine sizes](m8-machine-sizes.md),
[M9 Snapshot compaction](m9-snapshot-compaction.md) and
[M10 CLI friendliness](m10-cli-friendliness.md). What's left is under **Later**. What comes after
v1 is in [the goals](../goals.md), and its milestones start at
[M11 Bot faces](m11-bot-faces.md), which is in progress.

| # | Milestone | Status | End-to-end tests |
| --- | --- | --- | --- |
| M0 | [Foundations](m0-foundations.md) | done | `TestEngineContract`, `TestPauseAll`, `TestSurvivesHostRestart` |
| M1 | [Machines](m1-machines.md) | done | `TestMachine`, `TestMachineSecurity`, `TestMachineNetwork`, `TestMachineTerminal`, `TestQuiescing`, `TestWakeOnSSH` |
| M2 | [Idle and wake](m2-idle-and-wake.md) | done | `TestIdlePauseWake`, `TestBusyStaysAwake`, `TestBusyPastAnHour`, `TestMachine/exec_wakes_a_paused_machine`, `TestHostFull`, `TestSnapshotCleanup`, `TestPauseCycles` |
| M3 | [Vault, egress proxy, leak scan](m3-vault-and-proxy.md) | done | `TestSecretSwap`, `TestNoHostAccess`, `TestAuditLog`, `TestLeakScan` |
| M4 | [GitHub](m4-github.md) | done | `TestGitHubFullFlow`, `TestWorkflowPush` |
| M5 | [AI tools](m5-ai-tools.md) | done | `TestCodexRefresh`, `TestCopilot` |
| M6 | [Golden images and fork](m6-images-and-fork.md) | done | `TestImageAndFork` |
| M7 | [GitHub device flow](m7-github-device-flow.md) | done | `TestDeviceFlow` |
| M8 | [Machine sizes](m8-machine-sizes.md) | done | `TestSizes` |
| M9 | [Snapshot compaction](m9-snapshot-compaction.md) | done | `TestTrim`, `TestPauseCycles` (sentinels) |
| M10 | [CLI friendliness](m10-cli-friendliness.md) | done | none new (unit tests in `cmd/hib`) |
| M11 | [Bot faces](m11-bot-faces.md) | in progress | none new (unit tests in `internal/bot` and `cmd/hib`) |

M8, M9 and M10 came last. M8 and M9 needed only M2, and M10 nothing.

## How we track

- One file per milestone: goal, tasks, tests, and when it's done.
- Tick a box in the same commit as the code that does it.
- Status goes `not started` → `in progress` → `done`. Update it here and in the milestone file.
- **Done** means all of:
  - `make lint test` and CI are green (while CI is parked, `make lint test` alone),
  - `make e2e` is green on the real host, slow tests included,
  - the README and spec match what was built.
- Learned something that changes the design? Update the spec in the same commit, and add a line
  under **Changes** in the milestone file.
- Fixes and leftovers that don't belong to a milestone go under **Later** below. New features go
  in [ideas](../ideas.md).

## Later (not v1)

- The nightly shutdown pauses busy machines too, so a long agent run stops at 20:00 UTC and its
  connections drop. Put the shutdown off while a machine is busy, up to a limit.
- Images and fork (M6): `hib image save --replace`, and a new `/etc/machine-id` for each copy.
  An image whose save `hibd` didn't live to finish can be left behind with no listing showing it.
  So can a failed fork's template that E2B hides for longer than cleanup looks. A failed save
  holds the state lock for about 90 s while it looks, so `hib new`, `hib pause` and `hib rm`
  wait. E2B may pause a save that takes over 10 minutes on a machine with a shorter idle time.
  Nightly `pause-all` doesn't wait for a save, fork or `hib new` in progress, which then fails.
- Snapshot compaction (M9) trims old snapshots but keeps their directories, one a pause. Merge a
  machine's chain into one build so those stop adding up too. E2B's own options
  (`memfile-diff-dedup`, `guest-pause-reclaim` and `collapse-envd-heap`) are untried.
- The unit tests assume a umask of 022. Under the host's 002, `t.TempDir()` is group-writable and
  hibd refuses it as a state directory, so tests in `internal/hibd` and `internal/machines` fail
  on Linux. Set the umask in their `TestMain`, or tighten the directories they make.
- Encode the constraints the comment cleanup deleted, which no test or type holds yet: the
  functions that need a lock held (the fake engine's `resume` and `lookup`, hibd's Codex login
  lock, a test's `setGit`), a checked type for hib's remote arguments, a test that retiring a
  Codex login creates no keys, an egress test that a slow body outlives the header timeout, and
  e2e checks that the real GitHub token never enters an SSH command and model output never
  enters diagnostics.
- Remove the e2b engine's `chunkedConn` after a large-write test. websocat 1.14.1 splits
  oversized messages itself unless run with `-S`.
- Inject clocks where tests read the wall clock (the CA's `testNow`, idle-deallocate's test), and
  a barrier for the fixed sleep in the snapshot cleanup e2e test.
- idle-deallocate counts an inspection error as activity. Type the outcome as active, idle or
  unknown.
- E2B's sandbox proxy sometimes ends a command's stream early, and its orchestrator logs "use of
  closed network connection". It happened several times in two days of e2e runs, each time just
  after a machine started or resumed. Nothing in E2B's source we read closes that connection
  then: it resets them only when a machine stops. `hibd` now runs setup again (M6), so find the
  cause or report it upstream with the logs. `hibd` doesn't log a rerun yet, so look for cuts in
  the orchestrator's log. A cut may also show there as "ReverseProxy read error during body copy".
