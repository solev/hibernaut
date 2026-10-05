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

## Design

The M10 code is built against this section. Code that has to differ updates it in the same commit.

**Commands.** A table in `cmd/hib` describes every command: its name, arguments, a one-line
summary, what it does, one to three examples, its subcommands, and a function that declares its
flags on a `flag.FlagSet`. Parsing, help, mistakes, suggestions, completion and the README check
all read the table, so they can't drift apart. hib keeps Go's `flag` package: `--image` and
`-image` both work, and `-image` is never read as a cluster of one-letter flags.

**Help.**

- `hib help CMD`, `hib CMD -h`, `hib CMD -help` and `hib CMD --help` print the command's help to
  stdout and exit 0: the synopsis, what it does, its flags with two dashes and their defaults, and
  the examples. Subcommands work the same way: `hib help image build`, `hib image build -h`.
- Each command parses its flags before it loads settings, so help works before `hib init` and
  without `$HOME`. `hib version` and `hib --version` work there too.
- `hib help`, `hib -h` and `hib --help` list the commands and the exit codes on stdout and exit 0.
  `hib` alone prints the same on stderr and exits 2. So does a group alone, such as `hib image`,
  with its own help.

**Exit codes.** 0 done, 1 failed, 2 used wrongly, and 128 plus the signal when a signal stopped
hib: 130 for Ctrl-C. hib records which signal arrived, since a cancelled context can't tell.

**Mistakes** have one shape, on stderr, with exit 2 and nothing on stdout:

```
hib new: unknown flag --imag
usage: hib new NAME [--image IMAGE] [--size SIZE] [--idle 15m]
run hib help new
```

- Go's flag package never prints: its output goes to `io.Discard`, and hib rewrites its errors
  with two-dash flags.
- An unknown command or subcommand of `hib`, `hib secret`, `hib image`, `hib connect`,
  `hib disconnect` or `hib completion`: "unknown command nwe. Did you mean new?" and
  "run hib help".
- An unknown machine: hibd looks names up by exact match, so on a miss it lists the names (at most
  1,000) and adds "Did you mean app?". The error stays `ErrNoMachine`. If listing fails, the
  original error stands without a suggestion.
- `internal/suggest` picks the suggestion for hib and hibd: the one name within an edit distance
  of 1 (names up to 4 letters) or 2 (longer), counting a swap of two letters as one edit, or the
  one name the word is a prefix of. Two equally close names give no suggestion.

**Errors end with the next step.** Every message hib prints through `fail`, and hibd's common
ones, end with what to do, such as the ssh failures in `exitError` and the failed wakes in
`wakeError`. Messages that hold a credential keep the guard in `cmd/hib/connect.go` that never
echoes it.

**`hib rm NAME`** asks "Delete app and its disk for good? [y/N] " on stderr when stdin and stderr
are both terminals. Only `y` or `yes`, in any case, deletes. Anything else, end of input or Ctrl-C
keeps the machine ("kept app"), exit 1 (130 for Ctrl-C), and nothing reaches the host. `--yes`
skips the question. Without a terminal, `--yes` is required: "hib rm: deleting app can't be undone,
so pass --yes when there's no terminal to ask on", exit 2. The e2e tests and the docs pass `--yes`.

**Ctrl-C** stops hib waiting, not the host. hib says what may be left behind, then exits 130:

- `new`: "stopped waiting. The host may still be making app: check hib ls". `fork` says "making
  copies of app".
- `image build`: "stopped waiting. The host may still finish building dev: check hib image ls in a
  few minutes". It doesn't suggest building again, which would start a second build.
- `image save`, `image rm`, `rm`, `pause`, `secret add` and `secret rm` say the same for their own
  work. A wake says "stopped waiting. The host keeps starting: check hib status in a few minutes".
  Commands that only read, such as `ls`, say "stopped waiting".
- hib goes by the signal it recorded, not by the error it got: the same Ctrl-C may make ssh exit
  255.
- `connect` keeps the messages M5 and M7 gave it.

**Text from the host** is untrusted, as before.

- `termsafe` also escapes bidi controls (U+061C, U+200E, U+200F, U+202A to U+202E, U+2066 to
  U+2069), so a name can't reorder what's shown around it.
- `termsafe.Line` escapes tab, newline and carriage return too. Table cells, suggestions, step
  lines and progress use it.
- Styling comes after escaping, so colour sequences are always hib's own.

**`--json`** on `ls`, `status`, `secret ls`, `log`, `new`, `fork` and `image ls` prints one
`hibapi.Response` on stdout, with `v` and only that command's fields.

- It changes only the output, never what the command does. `hib status --json` doesn't wake the
  host either. When the host is an Azure VM, hib adds `host` from Azure's answer: asleep is
  `{"v":1,"host":{"state":"asleep","detail":"VM deallocated"}}`, with no `status` and exit 0, and
  awake is `"host":{"state":"running"}` next to hibd's `status`. hibd never sends `host`.
- A failure is the same object with `error`, exit 1, and hib adds nothing of its own on stderr. A
  fork that fails partway includes the copies it made. `hib status --json` exits 1 when something
  is failing, as it does without `--json`. A mistake on the command line is still a mistake: help
  on stderr, exit 2.
- The bytes are ASCII: every control character, DEL and non-ASCII character is written as
  `\uXXXX`. Decoded values are exact, and printing the JSON can't affect the terminal.
- No colour, hints or progress, ever.
- The README lists the fields, and says they're stable.

**Colour** with Lip Gloss v2, for states only: running and paused in `hib ls`, ok and failing in
`hib status`.

- Only when stdout is a terminal, `TERM` isn't `dumb`, and `NO_COLOR` is unset or empty. Any
  other value turns colour off, as no-color.org says. hib checks this itself: Lip Gloss reads
  `NO_COLOR` as a boolean, so `NO_COLOR=yes` would still colour.
- Columns line up the same with colour on and off.
- Colour lives in `cmd/hib`. The printers in `hibapi` take an optional function that styles a
  state, and hibd passes none, so hibd never links Lip Gloss.

**`hib ssh NAME [-- CMD]`** runs `ssh NAME.hib [-- CMD]`, as `hib exec` does but with the command
optional.

**Completion.** `hib completion bash|zsh|fish` prints a script that calls a hidden
`hib __complete WORD...`.

- It completes commands, subcommands and flags from the table, sizes from the M8 table, and
  machine and image names from a cache.
- The cache is `names.json` in the settings directory, mode 0600, written atomically. `hib ls`
  replaces its machine names and `hib image ls` its image names. Each name passes
  `hibapi.CheckName` when written and when read, with at most 1,000 of each.
- Completing never runs ssh or az and prints nothing on stderr. It honours `-config` on the line
  being completed. The scripts never evaluate what they complete.
- A failure to write the cache never fails the command.
- The cache doesn't expire. A stale name fails like a typo, with a suggestion.

**Next steps in `hib status`.** After the health lines, a "Next:" line lists what's still to do:

- `hib connect github` when GitHub isn't connected;
- `hib connect codex` (optional) when Codex isn't;
- `hib new NAME` when there are no machines. `hibapi.Status` gets `machines`, a count hibd sends
  only when E2B answered. With an older hibd there's no count and no `hib new` hint.

`hib init` ends with "run hib status to see what's next".

**Progress in `hib new`.** After 2 s, on a terminal, one live line on stderr shows what hib waits
for and for how long: "waking the host (40s)", then "making app (12s)".

- It uses Bubble Tea v2.0.10, inline, with `WithInput(nil)`, `WithOutput(stderr)`,
  `WithoutSignalHandler()` and `WithContext`. Without input, Bubble Tea sends no terminal queries,
  and leaves the terminal and Ctrl-C alone.
- The line fits the terminal's width and follows resizes. hib reads stderr's size itself and
  passes it with `WithWindowSize`. While the line runs, hib watches SIGWINCH and sends Bubble Tea
  each new size. Bubble Tea never gets the terminal's file, so it can't read the size itself.
  Without a size, hib assumes 80 columns and 24 rows.
- What hib and hibd write on stderr while hib waits prints above the live line. The line stops,
  and is cleared, before hib prints anything else, so lines never mix.
- There's no live line when stderr isn't a terminal, with `--json`, or when `TERM` is `dumb`.
- ssh runs with `BatchMode=yes` here, so no prompt can hide under the line.

**`hib image build` prints one line per step.**

- The engine passes hibd each E2B log entry with its time, level, `step` and message, instead of
  bytes.
- E2B logs a header when each step starts, before running it: `[CACHED ][builder 3/12] RUN …
  [hash]`. So a step runs from its header to the next header or the end of the build. Its time is
  the difference between their timestamps, rounded to seconds.
- hibd prints a line when a step ends. The base and finalize phases get lines too:

  ```
  dev (small)   3/12  RUN apt-get install -y git curl…  reused
  dev (small)   4/12  RUN npm i -g @openai/codex…       built in 41s
  ```

- `-verbose` prints the full log as today, without shortening messages.
- A failed step prints its last 20 lines (at most 4 KiB), kept by hibd as the log arrives. E2B's
  failure reason holds only part of it.
- hibd keeps reading logs after the build ends, until there are none left.

**Build events.** On a terminal, hib shows the running step on a live line, with its time so far.

- hib checks `hibd version --json`, which now lists `features`, for `build-events`.
- If listed, it runs `image build --json --events`. hibd then writes one `hibapi.Response` per line
  on stdout: an `event` for each step that starts or ends, with image, size, step, text, state
  (`started`, `reused`, `built` or `failed`) and seconds, then the final reply. stderr carries
  everything else.
- hib reads lines of at most 64 KiB. A line it can't read is an error, and it never starts the
  build again.
- With an older hibd, hib passes stderr on as today.
- `--verbose` shows the full log, with no live line. hib passes `-verbose` only to a hibd that lists
  `build-events`. An older hibd prints the full log anyway.
- hib never guesses which step comes next or how long is left.

**`hib disconnect codex|github`** runs `hibd disconnect codex|github`.

- hibd takes the same login lock as `connect`. For Codex it also takes `codex.lock`, the lock the
  refresher holds, so a refresh can't put tokens back.
- Codex: one vault update removes `codex-access`, `codex-refresh` and `codex-id`, then hibd removes
  `codex.json` and `codex-retired.json`. GitHub: one vault update removes the `github` secret, then
  hibd removes `github.json`.
- Other secrets and the lock files stay. The refresher keeps running and finds nothing to do.
- If hibd stops between the vault and the files, running it again finishes the job. Disconnecting
  what isn't connected says so and exits 0.
- Machines keep `~/.codex/auth.json`, which holds placeholders only. Codex in them stops working
  until `hib connect codex` again.
- The reply says where to revoke the login itself: ChatGPT's settings, or GitHub's authorized
  OAuth Apps (or the token's page). It doesn't promise to undo requests already made.

**Compatibility.**

- A newer hib with an older hibd: everything works except machine-name suggestions (hibd sends
  none), build events, the `hib new` hint and `disconnect`. hib checks `features` before build
  events and `disconnect`, and for `disconnect` says the host's hibd is too old: "run make up to
  update it".
- An older hib with a newer hibd: unchanged, except that `hib image build` shows step lines
  instead of the full log. `-verbose`, `-events` and `disconnect` are only used when asked for.

**Dependencies.** M10 adds `charm.land/bubbletea/v2` v2.0.10 and `charm.land/lipgloss/v2` v2.0.6
to hib only. A test checks with `go list -deps` that hibd links neither.

**Out of scope:**

- suggestions for flags and image names;
- guessing the next step or the time left;
- updating the names cache from `new`, `fork` or `rm`;
- a question before `hib image rm`;
- a full-screen view such as `hib top`.

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
- [ ] `hib disconnect codex|github` removes a login from the host and stops delivering it to
      machines. Today a Codex login can only be replaced: `hib secret rm` refuses the `codex-`
      secrets. GitHub's is the `github` secret, which `hib secret rm github` removes.

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

- [ ] `--json` on `ls`, `status`, `secret ls`, `log`, `new`, `fork` and `image ls`, using the
      `hibapi` types. A fork that fails partway returns the copies it made and the error. The
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
