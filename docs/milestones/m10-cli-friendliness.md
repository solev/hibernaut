# M10 CLI friendliness

Status: **not started** · Spec: §2 (The experience) · Needs: nothing

**Goal:** `hib` is easy to learn and hard to misuse. Every command explains itself, a mistake
gets a suggestion and a next step, slow commands say what they're doing, and scripts get stable
output.

It needs no other milestone, so it can be pulled forward any time. It changes `cmd/hib` mostly.

## Why

A local check of `hib` at `af11404` (2026-10-04) found:

- `hib rm -h`, `hib ls -h` and `hib status -h` print only `Usage of rm:` (or `ls`, `status`) and
  exit 2. Other `-h` output is Go's flag listing, in a different shape from the `usage:` line
  that mistakes print.
- Help needs settings: before `hib init`, `hib new -h` says "run hib init first".
- `hib help new` prints the general usage. `hib --version` fails with "flag provided but not
  defined".
- A typo (`hib nwe`) prints the whole command list, with no suggestion.
- Help shows some flags with one dash (`-env`, `-bind`, `-image`) and some with two
  (`--from-gh`). The spec's examples use two. Both work.
- `hib rm NAME` deletes the machine and its disk at once, with no question asked.
- `hib image build` streams every build command's output, so the step that matters is hard to
  find.

## Tasks

**Help**

- [ ] Every command has help: a synopsis, what it does, its flags, and one to three examples.
      `hib help CMD`, `hib CMD -h` and `hib CMD --help` print it to stdout and exit 0.
- [ ] Help and `hib version` work before `hib init`. `hib --version` works too.
- [ ] One usage shape: a mistake prints the command's synopsis and "run hib help CMD", never the
      whole command list or Go's `Usage of …:`.
- [ ] Help and messages show flags with two dashes everywhere. One dash keeps working.
- [ ] `hib help` lists exit codes: 0 done, 1 failed, 2 used wrongly.

**Mistakes**

- [ ] Unknown command or subcommand: "unknown command nwe. Did you mean new?", for `hib`,
      `hib secret`, `hib image` and `hib connect`.
- [ ] Unknown machine: "no machine ap. Did you mean app?", from the names `hibd` already has.
- [ ] Every error ends with the next step to take. Check the ones that don't yet, such as ssh
      failures and failed wakes.

**Safety**

- [ ] `hib rm NAME` asks "Delete app and its disk for good? [y/N]" on a terminal. `--yes` skips
      the question. Without a terminal, `--yes` is required, as `gh repo delete` does. The e2e
      tests and docs pass `--yes`.
- [ ] Ctrl-C says what was left behind, e.g. "stopped waiting. The host may still be making app:
      check hib ls".

**Feedback**

- [ ] `hib new` says what it's waiting for when it takes more than a few seconds (waking the
      host, making the machine).
- [ ] `hib image build` prints one line per step (name, reused or built, time). The full build
      log moves behind `--verbose`, and a failed step prints the tail of its log.

**Discovery**

- [ ] `hib completion bash|zsh|fish`: commands, flags, and machine and image names. Names come
      from a cache that `hib ls` refreshes, so completing never wakes the host.
- [ ] `hib ssh NAME [-- CMD]`, the same as `ssh NAME.hib`, for people who don't know the
      `.hib` trick yet.
- [ ] Next-step hints: after `hib init`, `hib status` says which of `hib connect github`,
      `hib connect codex` and `hib new` are still to do.

**Output for scripts**

- [ ] `--json` on `ls`, `status`, `secret ls`, `log` and `new`, using the `hibapi` types. The
      README says the fields are stable.
- [ ] Colour for states (running, paused, ok, failing) only on a terminal, and never when
      `NO_COLOR` is set.

## Tests

Unit (all in `cmd/hib`, no host):

- [ ] A table over every command: its help has a synopsis, flags and an example, prints to
      stdout and exits 0, with no settings directory.
- [ ] Mistakes: typos get the nearest command or machine, a bad flag prints one synopsis line
      and exits 2.
- [ ] `hib rm`: asks on a fake terminal, deletes only on y, needs `--yes` without a terminal.
- [ ] `--json` output decodes into the `hibapi` types. No colour when not a terminal or with
      `NO_COLOR`.
- [ ] Completion scripts pass `bash -n` and `zsh -n`, and complete names from the cache without
      running ssh.

End-to-end:

- [ ] The existing tests stay green with `hib rm --yes`.

## Done when

- [ ] `make lint test`, CI and `make e2e` are green.
- [ ] The README's command list matches `hib help`.

## Changes

(none yet)
