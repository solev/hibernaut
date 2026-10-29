# Machine lifecycle

A user makes a named Linux machine with `hib new` and lists it with `hib ls`. They log in with `ssh NAME.hib` and run one-off commands with `hib exec`. They pause it and wake it again with files and processes intact, then delete it for good with `hib rm`. The command asks first on a terminal and needs `--yes` without one.

## Sub-features

- `machine-new` makes a running machine of the asked size from the `dev` image.
- `machine-ls` lists it with state, image, size, idle time and room left.
- `machine-ssh` logs in as `user` through `ssh NAME.hib`.
- `machine-exec` runs one command and passes its exit code back.
- `machine-pause` pauses it, and `hib ls` shows `paused`.
- `machine-wake` resumes it on the next `ssh` or `hib exec`, with files and processes intact.
- `machine-rm-prompt` asks `Delete NAME and its disk for good? [y/N]` on a terminal, and keeps the machine unless the answer is `y` or `yes`.
- `machine-rm-noterm` refuses `hib rm` without `--yes` when there's no terminal.
- `machine-rm` deletes it. `hib ls` stops listing it and `ssh NAME.hib` fails.
- `machine-live` shows a live line such as `making NAME (12s)` on a terminal after 2 seconds.

## How to get to it (user POV)

- `hib new NAME [--size small|medium|large] [--idle 2h]` in a laptop terminal.
- `hib ls [--json]`.
- `ssh NAME.hib [CMD]`, or `hib ssh NAME [-- CMD]`.
- `hib exec NAME -- CMD [ARG...]`.
- `hib pause NAME`.
- `hib rm NAME [--yes]`.

## Driving it with control-hib

Preconditions:

- `control-hib doctor` exits 0 and reports room for at least one small machine.
- `M=$(control-hib name app)`. No machine named `$M` exists (doctor's `this run's:` line).

- **Make it, on a terminal.** Run `control-hib tty new -- new "$M" --size small`. Exit `0` in about 3 seconds. The transcript shows the live line `▬▬ making $M (2s)`, then `▪▪ made $M; connect with: ssh $M.hib`. The leading glyphs are the text faces.
- **List it.** Run `control-hib hib ls -- ls --json`. The machine whose `.name` is `$M` has `"state":"running"`, `"image":"dev"`, `"size":"small"`, `"idleSeconds":900` and `"ports":[]`. `.room.sizes` lists how many more of each size fit, for example `[{"size":"small","fits":6},...]` on an empty host.
- **Log in.** Run `control-hib ssh whoami "$M" -- 'whoami; nproc'`. Exit `0`, and stdout is `user` then `1`.
- **Run one command.** Run `control-hib hib exec -- exec "$M" -- uname -s`. Exit `0`, and stdout is `Linux`.
- **Exit codes pass through.** Run `control-hib hib exec-exit -- exec "$M" -- 'exit 3'`. Exit `3`.
- **Leave state behind.** Run `control-hib ssh mark "$M" -- "echo kept > /home/user/marker; tmux new -d -s keep 'sleep 100000'"`. Exit `0`.
- **Pause.** Run `control-hib hib pause -- pause "$M"`. Exit `0`, and stdout is `paused $M`. Then `control-hib hib ls-paused -- ls --json` shows `$M` with `"state":"paused"`.
- **Wake on ssh.** Run `control-hib ssh wake "$M" -- 'cat /home/user/marker; tmux has-session -t keep && echo tmux-alive'`. Exit `0` in under a second, and stdout is `kept` then `tmux-alive`. `hib ls --json` shows `$M` running again.
- **Wake on exec.** Pause again with `control-hib hib pause-2 -- pause "$M"`. Then `control-hib hib exec-wake -- exec "$M" -- echo woke` exits `0` with stdout `woke`.
- **Refuse without a terminal.** Run `control-hib hib rm-noterm -- rm "$M"`. Exit `2`. stderr contains `deleting $M can't be undone, so pass --yes when there's no terminal to ask on`, followed by `hib rm`'s usage.
- **Keep on no.** Run `control-hib tty rm-no --answer n -- rm "$M"`. The transcript shows `Delete $M and its disk for good? [y/N]`, then `kept $M`. Exit `1`. `hib ls --json` still lists `$M`.
- **Delete on yes.** Run `control-hib tty rm-yes --answer y -- rm "$M"`. The transcript shows the question, then `removed $M`. Exit `0`.
- **Proof it's gone.** Run `control-hib hib ls-gone -- ls --json`: no machine is named `$M`. Then `control-hib ssh gone "$M" -- true` exits `255`, and stderr is `hibd bridge: no machine called "$M"`. Record the result with `control-hib note machines "..."`.

## Gotchas

- Retrying `hib new` with a name that exists fails. After an interrupted `new`, check `hib ls --json` before retrying.
- A plain `ssh NAME.hib` from your own shell uses `~/.ssh/config` and the user's real settings, not the run's. Always go through `control-hib ssh`.
- `--yes` and `--answer y` both really delete. Use only this run's names. The guard enforces it.
- `idleSeconds` is 900 only for the default 15-minute idle time. A machine you leave idle for that long pauses by itself, and the next step wakes it, which hides `machine-pause` behind `machine-wake`. Run the pause steps back to back.
- On a full host, `hib new` and waking fail at once, naming the running machines. Report it. Don't pause someone else's machine to make room.
- `hib ls` text lists every machine on the host, including other people's, with columns sized to the content. Assert on `--json`, filtered by `.name`.
