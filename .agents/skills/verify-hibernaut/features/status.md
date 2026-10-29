# Host status and setup

A user checks the host with `hib status`, which reports the host, hibd's version, E2B health, the GitHub and Codex logins, and each secret's state. `hib version` prints the laptop CLI's version. `hib init` points the CLI at a host once. `hib connect` and `hib disconnect` change logins that every machine uses.

## Sub-features

- `version` prints `hib VERSION`.
- `status` prints the host, hibd's version, E2B health, GitHub and Codex logins, and a `SECRET SITES STATE DETAIL` table.
- `status-json` returns `.host.state`, `.status.version`, `.status.e2b.ok` and `.status.github.login`.
- `status-asleep` reports a deallocated host without waking it. Not yet driven here.
- `init` runs `hib init --host ... --ssh-config FILE`. It checks hibd's version and writes the ssh include. `control-hib launch` drives this.
- `connect-global`: `hib connect github|codex` and `hib disconnect` change logins for every machine. They're guarded and need the user's consent.

## How to get to it (user POV)

- `hib version`, `hib status [--json]`.
- `hib init --host user@ip [--ssh-config FILE] [--azure-group G --azure-vm V]`.
- `hib connect github`, `hib connect codex`, `hib disconnect ...`.

## Driving it with control-hib

Preconditions:

- Baseline passes.

- **Version.** Run `control-hib hib version -- version`. Exit `0`, and stdout is `hib <git describe of the checkout>`, for example `hib aefffe9-dirty`.
- **Status text.** Run `control-hib hib status -- status`. Exit `0`. Stdout starts with `host`, `hibd`, `E2B ok`, `GitHub` and `Codex` lines, then the `SECRET SITES STATE DETAIL` table.
- **Status JSON.** Run `control-hib hib status-json -- status --json`. `.host.state == "running"`, `.status.version` equals `VERSION` from `control-hib env`, and `.status.e2b.ok == true`.
- **Init.** `control-hib launch` already recorded this as `launch-init`. Its stdout says `... runs hibd VERSION`, `added hib's settings to .../config/included.conf`, then `run hib status to see what's next`.
- **Global logins are guarded.** Run `control-hib hib try-connect -- connect github`. It's refused before hib runs: `hib connect changes every machine's logins or this run's settings. Ask the user first, then rerun with HIB_VERIFY_ALLOW_GLOBAL=1`. Exit `1`. Record the result with `control-hib note status "..."`.

## Gotchas

- `hib status` shows the user's real GitHub and Codex accounts. That's expected, since the host is shared. Don't paste account details beyond the login name into reports.
- Codex secrets show `unchecked` until a machine uses them. That's not a failure.
- `doctor` never wakes the host: it reads `azure/state.sh` first. To prove `status-asleep`, run `status --json` after `cleanup` deallocates the VM. That needs a run that keeps its config, which `cleanup` deletes, so it's not covered yet.
- `hib init` against the user's real settings (no `--config`) rewrites `~/.config/hibernaut` and `~/.ssh/config`. Only `launch` runs init, and always with the run's scratch config.
