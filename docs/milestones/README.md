# Milestones

How we build Hibernaut v1, one milestone at a time. The design and the reasons behind it are in
the [spec](../spec.md): §7 is the plan, §8 is how we test.

**In progress: [M4 GitHub](m4-github.md).** Only `TestWorkflowPush` is left: it needs a token with
the `workflow` scope.
[M0 Foundations](m0-foundations.md), [M1 Machines](m1-machines.md),
[M2 Idle and wake](m2-idle-and-wake.md) and
[M3 Vault, egress proxy, leak scan](m3-vault-and-proxy.md) are done.

| # | Milestone | Status | End-to-end tests |
| --- | --- | --- | --- |
| M0 | [Foundations](m0-foundations.md) | done | `TestEngineContract`, `TestPauseAll`, `TestSurvivesHostRestart` |
| M1 | [Machines](m1-machines.md) | done | `TestMachine`, `TestMachineSecurity`, `TestMachineNetwork`, `TestMachineTerminal`, `TestQuiescing`, `TestWakeOnSSH` |
| M2 | [Idle and wake](m2-idle-and-wake.md) | done | `TestIdlePauseWake`, `TestBusyStaysAwake`, `TestWakeOnExec`, `TestHostFull`, `TestSnapshotCleanup`, `TestPauseCycles` |
| M3 | [Vault, egress proxy, leak scan](m3-vault-and-proxy.md) | done | `TestSecretSwap`, `TestNoHostAccess`, `TestAuditLog`, `TestLeakScan` |
| M4 | [GitHub](m4-github.md) | in progress | `TestGitHubFullFlow`, `TestWorkflowPush` |
| M5 | [AI tools](m5-ai-tools.md) | not started | `TestCodex`, `TestCodexRefresh`, `TestCopilot` |
| M6 | [Golden images and fork](m6-images-and-fork.md) | not started | `TestImageAndFork`, `TestImageLeakScan` |
| M7 | [GitHub device flow](m7-github-device-flow.md) | not started | `TestDeviceFlow` |
| M8 | [Machine sizes](m8-machine-sizes.md) | not started | `TestSizes` |
| M9 | [Snapshot compaction](m9-snapshot-compaction.md) | not started | `TestPauseCycles` (bounded) |

M8 and M9 came last but need only M2, so either can be pulled forward any time after M2. M9
should come before the host's disk fills: until then, every pause adds to a machine's disk.

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
