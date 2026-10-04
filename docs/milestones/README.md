# Milestones

How we build Hibernaut v1, one milestone at a time. The design and the reasons behind it are in
the [spec](../spec.md): §7 is the plan, §8 is how we test.

**In progress: [M6 Golden images and fork](m6-images-and-fork.md)**, and the end of
[M5 AI tools](m5-ai-tools.md). M5 is built, and every check is green. Only the live Codex tests
are left: they wait for `hib connect codex`, which needs someone to approve the code in a browser.
[M0 Foundations](m0-foundations.md), [M1 Machines](m1-machines.md),
[M2 Idle and wake](m2-idle-and-wake.md),
[M3 Vault, egress proxy, leak scan](m3-vault-and-proxy.md) and [M4 GitHub](m4-github.md) are
done.

| # | Milestone | Status | End-to-end tests |
| --- | --- | --- | --- |
| M0 | [Foundations](m0-foundations.md) | done | `TestEngineContract`, `TestPauseAll`, `TestSurvivesHostRestart` |
| M1 | [Machines](m1-machines.md) | done | `TestMachine`, `TestMachineSecurity`, `TestMachineNetwork`, `TestMachineTerminal`, `TestQuiescing`, `TestWakeOnSSH` |
| M2 | [Idle and wake](m2-idle-and-wake.md) | done | `TestIdlePauseWake`, `TestBusyStaysAwake`, `TestBusyPastAnHour`, `TestMachine/exec_wakes_a_paused_machine`, `TestHostFull`, `TestSnapshotCleanup`, `TestPauseCycles` |
| M3 | [Vault, egress proxy, leak scan](m3-vault-and-proxy.md) | done | `TestSecretSwap`, `TestNoHostAccess`, `TestAuditLog`, `TestLeakScan` |
| M4 | [GitHub](m4-github.md) | done | `TestGitHubFullFlow`, `TestWorkflowPush` |
| M5 | [AI tools](m5-ai-tools.md) | in progress | `TestCodexRefresh`, `TestCopilot` |
| M6 | [Golden images and fork](m6-images-and-fork.md) | in progress | `TestImageAndFork` |
| M7 | [GitHub device flow](m7-github-device-flow.md) | not started | `TestDeviceFlow` |
| M8 | [Machine sizes](m8-machine-sizes.md) | not started | `TestSizes` |
| M9 | [Snapshot compaction](m9-snapshot-compaction.md) | not started | `TestPauseCycles` (bounded) |
| M10 | [CLI friendliness](m10-cli-friendliness.md) | not started | none new (unit tests in `cmd/hib`) |

M8 and M9 came last but need only M2, so either can be pulled forward any time after M2. M9
should come before the host's disk fills: until then, every pause adds to a machine's disk.
M10 needs nothing, so it can be pulled forward any time.

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
- Anything that doesn't belong to a milestone goes under **Later** below.

## Later (not v1)

- Claude Code (API key, or `claude setup-token`) and OpenCode.
- Codex access tokens (`hib connect codex --access-token`), for ChatGPT Business/Enterprise.
- GitHub App: access to selected repos only, short-lived tokens.
- Vault key in Azure Key Vault, through the VM's managed identity.
- Multi-user: per-machine identity, SSH keys written at create time, SSH host certificates.
- A domain (hibernaut.dev), public HTTPS URLs per machine, a web console.
- Per-machine egress allowlists.
- The nightly shutdown pauses busy machines too, so a long agent run stops at 20:00 UTC and its
  connections drop. Put the shutdown off while a machine is busy, up to a limit.
- Images and fork (M6): `hib image save --replace`, and a new `/etc/machine-id` for each copy.
  An image whose save `hibd` didn't live to finish can be left behind with no listing showing it.
  Nightly `pause-all` doesn't wait for a save, fork or `hib new` in progress, which then fails.
