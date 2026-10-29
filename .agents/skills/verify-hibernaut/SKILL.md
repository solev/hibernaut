---
name: verify-hibernaut
description: "Drive Hibernaut's hib CLI against the real Azure host to prove machine, secret, port, image and status behavior the way a user sees it, with recorded evidence. Use after changing cmd/hib, internal/hibd, the proxy, vm/ or images/, or when asked to verify, demo or reproduce Hibernaut behavior end to end."
---

# Verify Hibernaut

Hibernaut's user surface is the `hib` CLI on the laptop, plus `ssh NAME.hib` into machines. Both talk over SSH to `hibd` and E2B on one Azure VM (`rg-e2b-test`/`e2b-test`). Nothing runs locally. Every proof goes through that one shared host, which also holds the user's own machines, secrets and GitHub and Codex logins.

All driving goes through `scripts/control-hib`. It builds `hib` from this checkout into a scratch run directory. It uses its own `--config` and ssh config, so `~/.config/hibernaut` and `~/.ssh/config` are never touched. It names everything it makes `vfy<6 hex>-ROLE`, and it refuses to change anything else. Run it from the repo root as `.agents/skills/verify-hibernaut/scripts/control-hib`, called `control-hib` below.

## Isolation: one run at a time

The laptop side is isolated per run. The host is not: one hibd, one E2B, about 6 GiB of machine memory (6 small, 3 medium or 1 large running at once), and host-wide secrets, logins and ports. So `control-hib` allows one active run per laptop (`~/.cache/hibernaut-verify/current`). `launch` refuses while another run is active. Another person or agent may still use the same host. Never pause, remove, fork or exec into a machine you didn't make. The guard rejects names without this run's prefix, and `doctor` lists the others.

## Launch

```bash
control-hib launch               # about 3 min if E2B is current; up to an hour if it must build the dev image
control-hib launch --no-deploy   # verify what's already on the host, without deploying this checkout
```

`launch` does this:

1. Records the VM's power state.
2. Runs `azure/up.sh`, which starts the VM, deploys this checkout's hibd and host scripts, installs E2B if its pin changed, and waits for `E2B is healthy`. With `--no-deploy`, starts the VM and E2B without deploying this checkout.
3. Builds `hib` with `git describe --tags --always --dirty` as its version.
4. Runs `hib init` into the scratch config. Records `EXPECTED_HIBD_VERSION` once in `run.env`. A deploy run expects the checkout's build. A `--no-deploy` run expects the version the host's hibd reports at this point.
5. Builds the `dev` image if `hib image ls` says it isn't built.
6. Runs `doctor`.

The run is ready when `doctor` exits 0. Deploying replaces the hibd that everyone on the host uses. That is the point when verifying a hibd change, but say so to the user if they didn't ask for it.

## Doctor

```bash
control-hib doctor
```

Read-only, and it never wakes the host. It exits 1 on any `FAIL` line:

- The scratch hib exists.
- The VM reports `VM running`.
- E2B answers (`hib status --json` `.status.e2b.ok`).
- hibd reports a readable version that equals the run's `EXPECTED_HIBD_VERSION`. Older runs without that field expect the checkout's build.
- The `dev` image is built.
- There is room for at least one small machine.

For a `--no-deploy` run whose expected version differs from the checkout's, an `info` line says that hib commands reach the host's hibd, not this checkout.

It also prints `info` lines for the GitHub, Codex and secret states, this run's machines, and how many machines are someone else's. Run it first, and again whenever anything looks off. A version mismatch means another deploy happened mid-run. An unreadable version also fails. Report either failure. To verify this checkout's hibd instead, clean up and start a deploy run with the user's agreement.

## Drive

```bash
control-hib name app                              # vfy3fa2c1-app: use for every machine, image and secret
control-hib hib LABEL -- ARGS...                  # hib ARGS, recorded; stdin passes through
control-hib ssh LABEL NAME [-- 'CMD']             # ssh NAME.hib CMD as user, recorded
control-hib tty LABEL [--answer y] -- ARGS...     # hib ARGS in a terminal: live line, faces, colour, prompts
control-hib note LABEL "what this proved"         # record an assertion next to the evidence
eval "$(control-hib env)"                         # RUN, PREFIX, EVIDENCE, HIB, HIB_CONFIG for ad hoc use
```

- **Make machines small.** Use `hib new NAME --size small`, unless the size is what you verify. Small machines leave room for the user's.
- **Assert on JSON.** Use `--json` on `ls`, `new`, `fork`, `status`, `secret ls`, `log`, `image ls` and `port ls` for assertions. The fields in the repo README's JSON table are stable. Text output is for what a person sees.
- **Expect no live line in a pipe.** `hib` drops the live line, faces and colour when stderr isn't a terminal. `hib rm` without `--yes` fails there too. Use `control-hib tty` to prove those. It runs hib under `expect` with `TERM=xterm-256color` and `HIB_FACES=text`.
- **Treat Ctrl-C as host-side.** Stopping `hib` doesn't stop work on the host. After an interrupted step, check `hib ls` before retrying.
- **Ask first for global commands.** `connect`, `disconnect`, `init` and `image build` change state for every machine. The guard refuses them unless `HIB_VERIFY_ALLOW_GLOBAL=1`. Set that only after the user agrees.

The recipes, with exact commands and expected results, are in [`features/`](features/README.md). Read the index, then the feature file you need.

## Evidence

Every `control-hib hib|ssh|tty|note` call writes the next numbered file to the run's `evidence/` directory, for example `~/.cache/hibernaut-verify/runs/20261028T155900Z-3fa2c1/evidence/007-pause.txt`. Each file holds the command as a user types it, the UTC time, the exit code, stdout and stderr. A `tty` step also keeps the raw terminal stream (`.tty`). `control-hib env` prints the path.

Proof standards:

- Drive the real user path: `hib` and `ssh NAME.hib`. Don't use `azure/ssh.sh hibd ...`, the E2B API or the e2e tunnel as a substitute, except for host-side side effects that no user command shows. Record those with a `note`.
- Record the action and the resulting state: run the command, then a second read-only view (`hib ls --json`, `ssh NAME.hib cat FILE`, `hib log --json`).
- Check side effects, not just exit codes. A machine is gone only when `hib ls` stops listing it and `ssh NAME.hib` fails. A swap happened only when `hib log` shows it.
- Real secrets never appear in evidence. Use a throwaway random value for any secret you add, and check that no evidence file contains it.
- A feature you couldn't reach is reported as not verified, with the command and the unmet precondition. It is never reported as verified through another path.

## Cleanup

```bash
control-hib cleanup            # this run's machines, saved images and secrets; then the VM's power state
control-hib cleanup --sweep    # also every vfy*- leftover from crashed earlier runs
```

Cleanup does this, in order:

1. Removes machines, saved images and secrets whose names carry the run's prefix (`hib rm --yes`, `hib image rm`, `hib secret rm`), each recorded.
2. Checks that none remain.
3. If the VM was off when `launch` started, runs `azure/down.sh`. It skips that step and says so when someone else's machine is running.
4. Deletes the scratch hib and settings, and frees the run slot.

It never kills processes by name and never touches what the run didn't make. It keeps `evidence/` and prints its path. Run it after every run, failed ones included. It is idempotent, and it handles a launch that died halfway. If it reports leftovers, fix the cause and rerun it. Don't free the slot by deleting `current`.

## Helpers

- `scripts/control-hib`: every command above. `control-hib help` prints its usage.
- `scripts/tty.exp`: the terminal runner behind `control-hib tty`. You don't call it directly.

Both need bash 3.2 or later, `jq`, `expect`, `az` (logged in), `go` and OpenSSH 9.6 or later.
