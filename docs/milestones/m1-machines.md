# M1 Machines

Status: **done** · Spec: §2, §3, §5 (Names and state, SSH path)

> Everything is built. `make lint test` and `make e2e` are green on the real host: `hib new app`
> then `ssh app.hib` works from the laptop. `hib new` → first ssh takes about 0.5 s, resume on
> ssh about 0.6 s, a cold `dev` image build about 1m40s.

**Goal:** `hib new app && ssh app.hib` works from the laptop.

## Tasks

**`dev` image**

- [x] The `dev` image: Ubuntu 24.04, user `user` with passwordless sudo, sshd, websocat on port
      8081 → sshd, git, gh, tmux, Node LTS, Python 3, build tools. Defined in `images/images.go`
      from the embedded `images/dev/` files, not a Dockerfile (see Changes).
- [x] Each machine gets its own SSH host key, made at create time and pinned by `hibd`, not one
      baked into the image (see Changes).
- [x] The laptop's SSH public key is written into the machine at create time (single user).
- [x] The start command runs sshd and websocat; E2B's ready check waits for sshd on port 8081.
- [x] `hib image build dev` builds it as an E2B template.

**`hibd`**

- [x] Machine state lives in E2B metadata (`hib.name`, image, created time); there is no separate
      store to lose (see Changes).
- [x] Names are `[a-z0-9-]`, unique, and written to E2B metadata `hib.name`.
- [x] Commands `new`, `ls`, `rm`, `pause`, `bridge`, speaking versioned JSON to `hib`.
- [x] New machines get a long timeout for now. M2 replaces it with the idle policy.
- [x] `bridge`: resumes the machine if it's paused, then relays bytes to its port 8081 through
      E2B's proxy on localhost (WebSocket).

**`hib`**

- [x] `hib init`: writes `config.json`, an ssh config with a `Host *.hib` block
      (`ProxyCommand hib bridge`, user, `ServerAliveInterval 60`, `StrictHostKeyChecking yes`,
      a per-host `UserKnownHostsFile`), and includes it from `~/.ssh/config`. Safe to run again.
- [x] `hib new / ls / rm / pause / exec / bridge`. `exec` is `ssh name.hib -- cmd`.
- [x] If the host doesn't answer, `hib` starts the Azure VM (through `az`, like `azure/up.sh`)
      and waits.

## Tests

Unit:

- [x] Name rules, metadata round-trip, rebuilding the listing from metadata.
- [x] The ssh-config writer: idempotent, leaves the rest of the file alone, and an `Include` at
      the top so hib's settings win.
- [x] JSON round-trip; a version mismatch gives a clear error.

Integration:

- [x] `hibd` commands against the fake engine.
- [x] `bridge` relays bytes both ways through a local WebSocket server, and resumes a paused
      (fake) machine first.

End-to-end:

- [x] `TestMachine`: `hib new` → `ssh name.hib true` (strict host key checking, no prompt) →
      `hib exec name -- uname -s` → `hib ls` shows it → `hib rm` → gone from `hib ls` and the
      engine. Logs how long `new` → first ssh took, and resume after a pause.
- [x] `TestMachineSecurity`: only the laptop's key is accepted (no password, no `root`/other
      users), agent and X11 forwarding stay off, a remote port-forward is refused, the laptop pin
      equals the host pin, a second machine gets a different key, and a swapped host pin is caught.
- [x] `TestMachineNetwork`: GitHub is reachable; Azure's WireServer (168.63.129.16, :80 and
      :32526) is not (see Changes, the firewall).
- [x] `TestQuiescing`: while the host is draining, `hib new` is refused with an explanation and
      leaves nothing behind, and `hib ls` still works.
- [x] `TestWakeOnSSH` (slow): after the VM deallocates itself, `ssh name.hib` wakes it through
      hib's bridge and connects, with the machine's files intact.

## Done when

- [x] `make lint test` and `make e2e` are green (CI is parked; `make lint test` alone for now).
- [x] README quick start: `hib init`, `hib new`, `ssh name.hib`.

## Changes

- **No Dockerfile, no SQLite.** The `dev` image is a directory of files (`images/dev/`) embedded
  in `hibd` and installed by ordered, cached build steps in `images/images.go`, built through
  E2B's template API — there is no Dockerfile. Machine state lives in E2B metadata (`hib.name`),
  so there is no SQLite store to keep in sync or lose: the listing is the engine's, filtered by
  the `hib.name` label.
- **Per-machine host keys, pinned by `hibd` and re-pinned by the bridge.** Instead of one host
  key baked into the image, `machine-init` makes a fresh host key in each new machine and prints
  it; `hibd` pins it to `/var/lib/hibernaut/host-keys/<id>`. On every connection the bridge reads
  that pin and writes it into the laptop's per-host `known_hosts/NAME.hib`, so ssh verifies the
  machine against hibd's record with `StrictHostKeyChecking yes`. A machine that presented the
  wrong key, or a tampered host pin, is refused (`TestMachineSecurity`).
- **E2B's seeded tier capped machines too tightly.** `base_v1` shipped `max_length_hours = 1`
  and `disk_mb = 512`. `install-e2b.sh` now sets `max_length_hours = 720` and `disk_mb = 10240`
  in one UPDATE and verifies both: the 1 h cap would pause a machine mid-use, and 512 MB of build
  rootfs couldn't even untar Node. The build's free-disk floor (`minFreeDiskMB`) is a no-op
  because its feature flag defaults off, so the tier's `disk_mb` is what matters.
- **The WireServer firewall.** A sandbox could reach Azure's WireServer (168.63.129.16) through
  the orchestrator's egress proxy and read host metadata. The orchestrator now runs with
  `IPAddressDeny` for that address, and an nft rule on the sandbox veth blocks it as defence in
  depth. `TestMachineNetwork` checks GitHub still works and WireServer does not.
- **No SSRF through the host's machine-facing calls.** A machine controls what answers on its
  port 8081 and its envd, so it could return an HTTP redirect instead of a WebSocket upgrade or a
  valid RPC response; the host client followed it, turning a dial or an `Exec` (used when a lost
  host pin is re-pinned) into a host-side request of the machine's choosing — past the
  orchestrator's `IPAddressDeny` — and leaking the call's headers. The engine now makes both
  calls with a client that refuses redirects (`TestDialWebSocketRejectsRedirect`,
  `TestExecRejectsRedirect`). The REST API client is unchanged: its far end is E2B's control
  plane, not a machine.
- **The drain marker.** While the host gets ready to sleep (`vm/lib/quiesce`), it touches
  `/run/hibernaut/quiescing`; `hibd` refuses to start or resume machines with a clear error
  ("the host is going to sleep…") so a deallocate can't race a new machine. `TestQuiescing`
  covers it.
- **`hib init` doesn't edit `~/.ssh/config` in place.** It writes its own ssh config and adds one
  `Include` line at the top of `~/.ssh/config`, so hib's `Host *.hib` settings come first and win,
  and uninstalling is one line to remove. The `ProxyCommand` path is quoted against
  CVE-2023-51385.
- **hugepages 3200 (~3 machines).** The host reserves 3200 huge pages (6.25 GiB), enough for
  about three 2 GiB `dev` machines at once; paused machines need none.
- **termsafe.** `hib` runs engine and machine output through `internal/termsafe`, which escapes
  control sequences, so a machine can't drive the laptop's terminal (clipboard writes, screen
  clears) through command output.
- **Makefile timeouts.** `make e2e` runs with `-timeout 40m` (the default 10 m is too short for a
  cold image build plus the machine tests) and `make e2e-slow` with `-timeout 90m`.

Still open:

- `TestSSHKeepalive` (an idle session kept alive past its timeout) isn't worth running: a
  machine's default timeout is 1 h, so proving the bridge's keepalive renews it would need an
  hour-plus idle test. M2 makes the timeout short and the idle policy explicit, so the keepalive
  test belongs there.
