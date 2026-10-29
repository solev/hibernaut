# Hibernaut verification map

This directory is the maintained source for verifying what a Hibernaut user sees. Read this index before driving the host, then use the matching feature file as the recipe. Commands below are relative to the repo root. `control-hib` means `.agents/skills/verify-hibernaut/scripts/control-hib`.

## Baseline preconditions

- `control-hib launch` finished and `control-hib doctor` exits 0, with hibd at the checkout's version and room for a small machine.
- Machine, image and secret names come from `control-hib name ROLE`, which stays fixed for the run. Recipes write them as `$M`, `$IMG` and so on, set at the top of their Preconditions. Recompute them in each new shell.
- The `dev` image is built (doctor checks this). Machines are `--size small` unless size is the subject.
- Other people's machines, secrets and logins exist on this host. They are not fixtures. Never use them as one.

## Driving conventions

- Every hib call is `control-hib hib LABEL -- ARGS...`, and every machine shell is `control-hib ssh LABEL "$M" -- 'CMD'`. Both are recorded. Treat command text as literal.
- Assert on `--json` output with `jq`. Text output is checked only when the text itself is the feature: messages, tables, prompts.
- Terminal-only behavior, such as the live line, faces, colour or the `rm` prompt, needs `control-hib tty`. Without a terminal, `hib` hides it on purpose.
- Mutating steps leave state behind. Each recipe's last step removes what it made, and `control-hib cleanup` catches anything a failed recipe left.

## Proof and skip reporting

- Proof is the action's evidence file plus a second, read-only view of the resulting state, such as `hib ls --json`, `ssh NAME.hib cat FILE` or `hib log --json`.
- Record each assertion with `control-hib note LABEL "..."`, naming the feature file and sub-feature ID.
- A step that can't run is reported as not verified, with the command, its output and the unmet precondition. Never report it as verified through another entry point.
- Evidence must never hold a real credential. Secrets added during a run are random throwaway values.

## Feature entry contract

Each feature file starts with an H1 and one paragraph of user-visible behavior, then exactly four H2s in this order:

1. `Sub-features`: short IDs, one line each.
2. `How to get to it (user POV)`: every user entry point.
3. `Driving it with control-hib`: starts with `Preconditions:`, then labeled bullets that pair an action with the exact command and the observable result.
4. `Gotchas`: traps that waste or invalidate a run.

Keep implementation details out. Name only user paths, stable handles, required state, commands and observable proof.

## Features

- [Machine lifecycle](./machines.md) covers making, listing, logging in to, running commands on, pausing, waking and deleting a machine, including the delete prompt.
- [Secrets and the swap proxy](./secrets.md) covers storing a bound secret, the placeholder in machines, the swap on bound sites only, response scrubbing and the audit log.
- [Ports between machines](./ports.md) covers publishing a port, reaching it as `NAME.hib:PORT` from another machine, refusals and the link log.
- [Images and fork](./images-and-fork.md) covers saving a machine as an image, making machines from it, forking, and the in-use refusal.
- [Host status and setup](./status.md) covers `hib status`, `hib version`, JSON output, host sleep and wake, and the global logins you must not change unasked.
