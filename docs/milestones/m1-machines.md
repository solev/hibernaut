# M1 Machines

Status: **not started** · Spec: §2, §3, §5 (Names and state, SSH path)

**Goal:** `hib new app && ssh app.hib` works from the laptop.

## Tasks

**`dev` image**

- [ ] `images/dev/Dockerfile`: Ubuntu, user `user` with passwordless sudo, sshd, websocat on port
      8081 → sshd, git, gh, tmux, Node LTS, Python 3, build tools.
- [ ] SSH host key generated once on the host and baked in (kept out of git).
- [ ] Your SSH public key baked in (single user).
- [ ] Start command runs sshd and websocat; E2B's ready check waits for port 8081.
- [ ] `hib image build dev` builds it as an E2B template.

**`hibd`**

- [ ] SQLite store: name ↔ sandbox ID, image, created time.
- [ ] Names are `[a-z0-9-]` and unique, and are also written to E2B metadata `hib.name`. `hibd`
      rebuilds its store from metadata if the store is lost.
- [ ] Commands `new`, `ls`, `rm`, `pause`, `bridge`, speaking versioned JSON to `hib`.
- [ ] New machines get a long timeout for now. M2 replaces it with the idle policy.
- [ ] `bridge`: resumes the machine if it's paused, then relays bytes to its port 8081 through
      E2B's proxy on localhost (websocket).

**`hib`**

- [ ] `hib init`: host address, SSH key, and a `Host *.hib` block in `~/.ssh/config`:
      `ProxyCommand hib bridge`, user, `ServerAliveInterval 60`, and the pinned host key. Safe to
      run again.
- [ ] `hib new / ls / rm / pause / exec / bridge`. `exec` is `ssh name.hib -- cmd`.
- [ ] If the host doesn't answer, `hib` starts the Azure VM (through `az`, like `azure/up.sh`)
      and waits.

## Tests

Unit:

- [ ] Name rules, the store, rebuilding from metadata.
- [ ] The `~/.ssh/config` writer: idempotent, leaves the rest of the file alone.
- [ ] JSON round-trip; a version mismatch gives a clear error.

Integration:

- [ ] `hibd` commands against the fake engine.
- [ ] `bridge` relays bytes both ways through a local websocket server, and resumes a paused
      (fake) machine first.

End-to-end:

- [ ] `TestMachineBasics`: `hib new` → `ssh name.hib true` (strict host key checking, no prompt)
      → `hib exec name -- uname -a` → `hib ls` shows it → `hib rm` → gone from `hib ls` and E2B.
      Logs how long `new` → first SSH took.
- [ ] `TestSSHKeepalive` (slow): an idle interactive session is still alive after 11 minutes.

## Done when

- [ ] `make lint test`, CI and `make e2e-slow` are green.
- [ ] README quick start: `hib init`, `hib new`, `ssh name.hib`.

## Changes

(none yet)
