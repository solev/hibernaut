# Hibernaut

**Self-hosted, hardware-isolated sandboxes that sleep between missions.**

Hibernaut is an experiment: run our own [boxd.sh](https://boxd.sh)-style "computers on demand" — tiny
virtual machines that start in milliseconds, can be frozen mid-work and resumed later with
everything intact.

It runs [E2B's open-source infrastructure](https://github.com/e2b-dev/infra) (Firecracker microVMs)
on a single Azure VM, plus scripts that switch that VM off whenever nobody is using it.

---

## TL;DR

- One Azure VM (`e2b-test`, 4 vCPU / 16 GB) runs E2B.
- E2B starts **Firecracker microVMs** ("sandboxes") inside it.
- Each sandbox is a real VM with its own kernel. It is **not** a container.
- Sandboxes can be **paused** (memory + disk snapshot) and **resumed** later.
- The Azure VM **deallocates itself** after 30 idle minutes, and every night at 20:00 UTC.
- Running sandboxes are **paused before the VM stops**, so they survive reboots and deallocations
  with their files and processes (tested end to end).
- Setup is scripted: `make up` starts everything, `make down` stops paying.
- **Secrets stay on the host.** Machines hold look-alike placeholders. The host's proxy swaps in
  the real value only on HTTPS calls to the sites a secret is bound to.
- **GitHub is connected once.** `hib connect github` gives every machine your GitHub
  login: `git clone`, `git push` and `gh` work, with only a placeholder inside.
- **So are AI tools.** `hib connect codex` logs Codex in with your ChatGPT plan, and Copilot uses
  the GitHub connection. Both work in every machine, with only placeholders inside.
- **Set-up machines can be saved and copied.** `hib image save` keeps a machine as an image, and
  `hib fork` copies it. Files, packages and running processes come along, and each copy gets its
  own name and host key.
- **Where it's going:** the [v1 spec](docs/spec.md) plans named machines you `ssh` into, with
  GitHub and AI tools connected once and no real credentials inside machines. M0 (foundations),
  M1 (machines), M2 (idle and wake), M3 (vault and egress proxy), M4 (GitHub), M5 (AI tools),
  M6 (golden images and fork) and M8 (machine sizes) are done. Progress:
  [docs/milestones](docs/milestones/README.md).

## Measured on this setup

`examples/try.py` running against the VM (warm; the first run after boot is slower):

| Operation                        | Time         |
| -------------------------------- | ------------ |
| Create sandbox                   | ~260–280 ms  |
| Run a command inside it          | ~50 ms       |
| Pause (snapshot memory + disk)   | ~0.2–1.1 s   |
| Resume (file written before pause is still there) | ~340–380 ms |
| Kill                             | ~35 ms       |

Each sandbox runs Debian 12 on guest kernel 6.1 with 2 vCPUs and ~512 MB RAM (the `base` template).
The first create after a cold VM boot takes ~1.5 s.

Across host restarts (`make e2e-slow`), a running sandbox was paused on the way down and resumed
in ~1 s after a reboot, and ~3 s after the nightly deallocate and a cold start.

---

## Why this exists

1. **boxd.sh** sells "composable computers": microVMs that you can create, fork, snapshot and
   hibernate in milliseconds, for developers and AI agents.
2. We wanted to learn **how that works under the hood**, rather than just use it.
3. E2B builds the same kind of product and open-sourced the whole stack (Apache-2.0), so the
   quickest way to learn was to **run it ourselves** and poke at it.
4. Hibernaut is that experiment, packaged so it can be rebuilt from scratch.

## How it works (in layers)

```
Your Mac
  │  ssh (key-only)
  ▼
Azure VM  e2b-test  (Ubuntu 24.04, D4s_v5, nested virtualization → /dev/kvm)
  │
  ├── E2B control plane        (systemd units e2b-*, started on boot)
  │     api            :3000   create / list / kill sandboxes, API keys, templates
  │     orchestrator   :5008   actually boots Firecracker VMs, handles snapshots
  │     client-proxy   :3002   routes traffic into sandboxes
  │     infra (docker)         Postgres, ClickHouse, Redis, Grafana/observability
  │
  ├── Hibernaut host pieces
  │     hibd                   pause-all, ls (through E2B's API)
  │     hibd daemon            unit hibd: keeps busy machines running; idle ones pause
  │     hibd proxy             unit hibd-proxy: swaps bound secrets into machines' HTTPS calls
  │     hibd-pause-all         guard unit: pauses every sandbox whenever E2B stops
  │     idle / nightly timers  quiesce (pause all, stop E2B), then deallocate
  │
  └── Firecracker microVMs  ("sandboxes")
        own Linux kernel · own filesystem · envd agent inside (runs commands, files)
```

**The "save game" trick.** Pausing a sandbox writes its RAM and disk to files. Resuming loads the
snapshot back and maps memory **lazily**: pages are fetched only when the VM touches them
(`userfaultfd`). This is why resume takes milliseconds rather than seconds.

**The disk trick.** Sandboxes don't get a full disk copy. They share a read-only template image
and write changes to a copy-on-write layer, exposed through NBD block devices.

## Project layout

```
cmd/hib/                   Laptop CLI: machines, images, secrets, connections, status, the audit log
cmd/hibd/                  Host tool and daemon, which hib runs over SSH (code in internal/hibd/)
internal/engine/           The Engine interface; e2b/ (real), fake/ (in memory), enginetest/ (the
                           contract both must pass)
internal/machines/         Makes machines from images, finds them by name, reaches their SSH
internal/busy/, room/      Keeps busy machines running. Says whether one more machine fits.
internal/pauseall/         Pauses every running sandbox, within a time limit
internal/cleanup/          Finds the E2B snapshot builds that nothing needs any more
internal/vault/, envblock/ Sealed secrets and placeholders. The placeholder block in machines.
internal/egress/           The HTTPS proxy; hook/ (iptables, real destination), swap/ (swap, scrub)
internal/ca/, audit/       The Hibernaut CA. The audit log of every swap.
internal/github/           Checks a GitHub token with GET /user. The connected account, github.json.
internal/codex/            The Codex login: its profile, refreshing, machines' login file, and the
                           token endpoint the proxy answers Codex's refreshes with
internal/slots/            Finds the machine an intercepted connection comes from
internal/leakscan/         Searches machine and image snapshots for real secret values
images/                    Image recipes. dev/ is the dev image's steps, and dev/versions pins
                           Codex and the Copilot CLI.
e2e/                       End-to-end tests against the real VM (build tag e2e)
third_party/e2b/           E2B's API spec and envd proto at the pinned commit; clients are
                           generated from them
Makefile                   build, lint, test, generate, deploy, up, down, e2e, e2e-slow
.github/workflows/ci.yml   CI, parked: lint, test, and a check that generated code is current

azure/                     Runs on your Mac (needs az CLI + ssh)
  common.sh                Shared settings: RG, VM name, size, region, user. Env vars override.
  create-vm.sh             One-time: create RG, VM, identity, custom role, auto-shutdown, host setup
  up.sh                    Start VM → deploy → install E2B if needed → start E2B → wait until healthy
  deploy.sh                Build hibd, sync vm/, examples/ and Codex's pins, install the host pieces
  start.sh                 Start the VM and wait for SSH; starts nothing else
  down.sh                  Pause every sandbox, stop E2B, deallocate (billing for compute stops)
  ssh.sh                   SSH to the VM; extra args are passed through
  target.sh, state.sh      Print the VM's user@ip, or its power state

vm/                        Runs on the VM (synced to ~/hibernaut/vm)
  setup-host.sh            Root host setup: sshd, hibd, the CA, proxy, Codex, quiesce, guard, timers
  install-codex.sh         Installs the pinned Codex package, which hibd logs in with. CI uses it.
  install-e2b.sh           Installs E2B and its dependencies; --if-needed skips an installed version
  wait-e2b.sh              Waits until E2B is healthy; --start starts it first
  lib/quiesce              Pause every sandbox, stop E2B, flush to disk. Runs before any deallocate.
  lib/deallocate-self      Deallocates this VM through its managed identity
  lib/e2b.sh               E2B's units and health checks, shared by the scripts above
  lib/e2b-infra-ready      Waits until E2B's databases accept connections
  systemd/                 E2B's units, hibd, the proxy, the pause-all guard, the nightly timer
  idle-deallocate/         Script + systemd service/timer + config for auto-deallocation
  e2b/, hibd/              Settings for E2B's orchestrator and for hibd
  sshd/00-hardening.conf   No root login, no passwords, keys only

examples/
  try.py                   Create → run → pause → resume → kill, with timings

docs/
  spec.md                  v1 plan: machines, SSH, credential swap, testing, open spikes
  milestones/              M0–M10: tasks, tests and status for each milestone
  research/                Cited research behind the spec (E2B internals, agent/GitHub auth)
```

## Quick start

Prerequisites: Azure CLI logged in (`az login`), an SSH key at `~/.ssh/id_ed25519`, rsync, and Go.

```bash
./azure/create-vm.sh     # first time only
make up                  # start VM + E2B (first run installs everything and takes a while; later runs ~2–3 min)
./azure/ssh.sh hibernaut/.venv/bin/python hibernaut/examples/try.py
make down                # pause every sandbox and stop paying for compute
```

Everyday use is `make up` → play → `make down` (or just walk away and let it deallocate itself).

### Using machines (`hib`)

`hib` is the laptop CLI for named machines. Build it with `make build`, set it up once, then work
over plain `ssh`:

```bash
hib init --host azureuser@20.91.197.185 --azure-group rg-e2b-test --azure-vm e2b-test
hib image build dev        # build the dev image in every size, as E2B templates
hib new app                # create a machine named app
hib new tiny --size small  # a small one (or medium, the default, or large)
hib new big --idle 2h      # one that pauses after 2 idle hours (default 15m; 0 = never)
ssh app.hib                # shell in; if the VM is asleep, hib wakes it first
hib exec app -- uname -a   # run one command, no interactive shell
hib ls                     # machines, running and paused, their size, when they pause, room left
hib pause app              # pause it now; ssh app.hib resumes it
hib rm app --yes           # delete it and its disk for good
```

`hib rm` asks "Delete app and its disk for good? [y/N]" on a terminal, and only `y` or `yes`
deletes. `--yes` skips the question. Scripts and anything else without a terminal must pass it.

`hib image build dev` bakes the Hibernaut CA into the image, so machines can trust the host proxy.
Machines made before that rebuild need to be recreated to trust it.

`hib init` writes `~/.config/hibernaut` and adds an `Include` for hib's `Host *.hib` block at the
top of `~/.ssh/config`, so `ssh app.hib` just works. Behind that name, `hib bridge` resumes the
machine if it's paused, starts the Azure VM if it's deallocated, and checks the machine's pinned
host key. The machine user is `user`, with passwordless sudo.

**Idle machines pause themselves. Busy ones don't.** A machine is busy while you're connected
(`ssh` or `hib exec`), while it uses at least 10% of a CPU, or while a coding agent (`codex`,
`copilot`, `claude`) runs in it, even one waiting on an LLM. Once it has been idle for its idle
time, 15 minutes unless `--idle` says otherwise, it pauses with its memory and files. `hib ls`
shows when each machine pauses. `ssh app.hib` or `hib exec` wakes it in under a second, where it
left off. Nothing else wakes it. After 30 minutes with no machine running and no one connected,
the VM deallocates itself, and every night at 20:00 UTC it pauses every machine and deallocates
regardless (see [Costs and auto-off](#costs-and-auto-off)). `make down` does that now.

**About three machines run at once.** Running machines share the VM's memory pool of 6.25 GiB.
Paused ones take only disk. `hib ls` ends with how many more of each size fit, for example
`Room for more machines: 4 small, 2 medium or 1 large.` Each count is on its own, not a mix. When
the VM is full, `hib new` and waking a paused machine fail at once with a message naming the
running machines: `hib pause NAME` frees memory, `hib rm NAME --yes` frees disk. `hib new` with a
built-in image also says which sizes still fit. CPU is shared, so busy machines slow each other
down but nothing fails.

**Machines come in three sizes.** `hib new app --size small` picks one. The default is medium,
the size every machine had before sizes.

| Size   | vCPU | RAM   | At once on today's VM         |
| ------ | ---- | ----- | ----------------------------- |
| small  | 1    | 1 GiB | 6                             |
| medium | 2    | 2 GiB | 3                             |
| large  | 4    | 4 GiB | 1, plus a small or medium one |

Sizes mix in the one pool: two medium and two small machines fit too. Disk is 10 GiB for every
size.

**A machine keeps its size for life.** E2B can't change a snapshot's CPUs or RAM. To move to
another size, make a new machine and copy your files over, for example
`scp -3 -r app.hib:project tiny.hib:` or rsync. Saved images and forks keep the size of the
machine they came from, so `hib new --image myimg` takes no `--size`. A large machine can't be
saved or forked on today's VM: that needs 8 GiB at once, and `hib` says so.

**Each size is its own E2B template**, so `hib image build dev` builds every size: medium first,
then small and large. They share the build steps, so each extra size adds only its final boot
and snapshot. After updating hibd, run `hib image build dev` once before making small or large
machines.

E2B keeps every snapshot it writes, even a removed machine's. `hibd` deletes the ones nothing
needs any more when a machine is removed, and hourly. `./azure/ssh.sh sudo hibd cleanup -n`
says what it would delete, and why it keeps the rest. A machine's own snapshots still grow as it
pauses (3.6 GiB after 20 pauses of an idle one): each is a diff on the one before, and E2B never
merges them. Compaction is planned ([M9](docs/milestones/m9-snapshot-compaction.md)).

### Images and fork

Save a set-up machine as an image, or copy it straight away. Files, installed packages and running
processes come with it, so a dev server answers in each copy without restarting:

```bash
hib image save app myimg      # save app as image myimg, memory included
hib image ls                 # built-in and saved images, their memory and creation time
hib new task --image myimg    # start with everything that was in app
hib fork app --count 2        # app-1 and app-2. The default is one copy, the most is ten.
hib ls                       # FROM shows which machine a copy came from
hib rm task --yes
hib image rm myimg
```

**An image carries everything in the machine.** `hib` puts only placeholders and your public key
there, not real secrets. But a secret you typed into the machine yourself goes into the image too.
Treat an image as you would that machine.

Saving and forking pause the origin for a few seconds while its snapshot reaches disk, then
resume it. SSH sessions into it may drop. A paused origin is put back to sleep afterwards.
They need extra memory and disk for the checkpoint and copies. If there isn't room, `hib` says
what to pause or remove. A large save can take minutes. If E2B's checkpoint fails, `hib` reports the
origin's state and keeps a safe snapshot from when the save began. It never retries automatically.
E2B can hide a failed save's template for a few minutes. If `hib` can't confirm it's gone, it
says so, and for an image it says how to remove it.

Copies get fresh SSH host keys, the current placeholder setup and your authorized public keys.
Forks keep the origin's image, size and idle policy. Their names use the lowest free suffix,
starting at `app-1`. If a later copy fails, the whole copies already made stay, and `hib` names
them before printing the error.

Saved names can't replace a built-in image or an existing saved one. If a later built-in image
takes a saved name, `image ls` marks the saved one as shadowed. `new --image` uses the built-in one.
`image rm` always means the saved one, and refuses while any running or paused machine uses it.
Remove those machines first. E2B keeps a deleted image's snapshot files until `hibd`'s cleanup can
free them.

### Secrets (`hib secret`, `hib log`)

Real credentials never enter a machine. You store a secret on the host and bind it to the sites
that should get it. Machines get a look-alike placeholder in their environment instead. When a
machine sends the placeholder over HTTPS to a bound site, the host's proxy swaps in the real
value on the way out, and turns it back into the placeholder in the response. Sent anywhere
else, the placeholder is a meaningless string.

```bash
hib secret add OPENAI --env OPENAI_API_KEY --bind api.openai.com  # asks for the value, hidden
hib secret ls                         # names, variables, sites and placeholders, never values
ssh app.hib printenv OPENAI_API_KEY   # the placeholder
hib log                               # each swap: time, machine, site, method, path and secret
hib secret rm OPENAI
```

- The value comes from stdin or a hidden prompt, never from an argument. The host seals it with
  AES-256-GCM, under a key only root can read.
- For GitHub, use `hib connect github` ([below](#github-hib-connect-github-hib-status)). It
  stores the token as secret `github` and sets up git too.
- `--bind` takes host names, comma-separated. `*.example.com` matches every host under
  example.com, but not example.com itself.
- Adding or removing a secret updates every running machine at once, and paused ones when they
  wake. New logins see the change. The proxy uses it from the next request.
- The proxy swaps placeholders only in the `Authorization` header, Basic auth included. That's
  where git, gh, the Copilot CLI and Codex send credentials. It decrypts only calls to bound
  sites. Everything else passes straight through.
- On the host, `sudo hibd leakscan NAME` pauses a machine and searches its memory and disk for
  every real value, raw, base64 and URL-encoded. It waits up to 15 minutes for the snapshot to be
  fully written. `-image NAME` scans an image. `-canary` plants a value in a throwaway machine and
  checks that the scan finds it.

Limits: only HTTPS on port 443 is swapped, and only in `Authorization`. A placeholder anywhere
else (another header, the URL, the body, a WebSocket message) goes out as it is. That's on
purpose: swapping inside content would let a machine store the real value at the site, in a gist
say, and read it back. So a tool that sends its key in another header, such as `x-api-key`, can't
use a bound secret yet. Responses from bound sites are scrubbed back to placeholders in headers,
trailers and bodies, including escaped and base64 forms. WebSocket messages and line-wrapped
base64 aren't. A response the proxy can't decompress fails with 502 instead of passing unscrubbed.
Bind a secret only to the API hosts that use it. If the proxy stops, machines' HTTPS goes straight
out with placeholders. Bound calls fail, and nothing leaks.

### GitHub (`hib connect github`, `hib status`)

Connect GitHub once. Every machine can then clone your private repos, push, and use `gh`, with no
login inside it:

```bash
hib connect github            # shows a code and opens a browser to approve the host's login
ssh app.hib
  git clone https://github.com/solev/rocket   # git@github.com:solev/rocket.git works too
  gh pr create
hib status                     # the host, E2B, GitHub and each secret, without waking the host
```

- `hib connect github` shows a code and opens `https://github.com/login/device`. Sign in and
  enter the code to approve Hibernaut. The host gets its own token, which never touches the
  laptop. It asks for `repo`, `read:org` and `workflow`.
- Ctrl-C cancels the login. If you cancelled, run `hib status` to see whether GitHub is connected.
  GitHub may have granted the token just before you cancelled.
- `hib connect github --from-gh` takes the token from `gh auth token` on your laptop instead, to
  reuse your existing login.
- `hib connect github --token` takes a token you made, such as a fine-grained one limited to some
  repos. It asks for it hidden, or reads stdin. Never pass it as an argument.
- Every source checks the token with GitHub (`GET /user`) and stores it as secret `github`:
  variable `GH_TOKEN`, bound to `github.com`, `api.github.com`, `uploads.github.com` and
  `*.githubcopilot.com`. If GitHub refuses it, nothing is stored.
- To revoke the host's device login, open `https://github.com/settings/applications`, choose
  **Authorized OAuth Apps**, then **Hibernaut**, then **Revoke**.
- If your org restricts OAuth Apps, an owner must approve Hibernaut before machines can see the
  org's repos.
- In the `dev` image, `/etc/gitconfig` makes gh git's credential helper for GitHub, and rewrites
  `git@github.com:` URLs to HTTPS. gh answers with the placeholder, and the proxy swaps it.
- Commits are yours. `hibd` sets git's `user.name` to your GitHub name, and `user.email` to your
  noreply address (`ID+LOGIN@users.noreply.github.com`), so no email scope is needed.
- **The `workflow` scope.** The device flow asks for it. gh's token has `repo`, `read:org` and
  `gist`, but not `workflow`.
  Without it, GitHub refuses pushes that change `.github/workflows/`, and `hib connect` warns.
  To fix it: `gh auth refresh -h github.com -s workflow`, then `hib connect github --from-gh`.
- With `--from-gh`, the laptop and the host share gh's token. Logging gh in on many devices can
  make GitHub revoke it. `hib status` then says `revoked: run hib connect github --from-gh again`.

`hib status` checks secrets bound to `api.github.com` with GitHub. It can't test other keys, so
they show as `unchecked`. It exits 1 when E2B is down or a secret is revoked or failing. If the
host is asleep, it says so and exits 0, without waking it.

Machines made before the `dev` image was rebuilt with M4 don't have gh as git's helper. Run
`hib image build dev` and recreate them. Their git identity is set either way.

### AI tools (`hib connect codex`, Copilot)

Machines made from the `dev` image have Codex and the GitHub Copilot CLI. Connect each once, and
both work in every machine with no login and no real token inside:

```bash
hib connect codex              # Codex, with your ChatGPT plan: prints a code and a link
hib connect github            # Copilot uses the GitHub connection
ssh app.hib
  codex                        # or: codex exec "…"
  copilot                      # or: copilot -p "…"
hib status                     # adds a Codex line: account, plan, token expiry, health
```

- `hib connect codex` runs Codex's device login on the host. Open the link, sign in to ChatGPT,
  and enter the code within 15 minutes. If ChatGPT refuses, turn on device code login in ChatGPT,
  under Settings, Security. Connecting again replaces the login, even with another account.
- The host keeps the real tokens as secrets `codex-access`, `codex-refresh` and `codex-id`.
  Machines get `~/.codex/auth.json` with placeholders: running ones at once, paused ones when
  they wake. The proxy swaps the access token on `chatgpt.com`, and answers Codex's refresh
  requests itself. Only the host refreshes with OpenAI, at half the token's lifetime, or when a
  site refuses the token. `hib secret` refuses names that start with `codex-`.
- Codex runs with `approval_policy = "never"` and `sandbox_mode = "danger-full-access"`. The
  machine is the sandbox.
- If OpenAI refuses the refresh token, the login is dead. If a refresh's outcome is unknown, it's
  stuck, and the host doesn't retry by itself, since OpenAI refuses a refresh token used twice.
  Either way, `hib status` exits 1 and says to run `hib connect codex`. It reads the login's
  health on the host and never calls OpenAI.
- Copilot sends the `github` secret's placeholder, which the proxy swaps on `*.githubcopilot.com`.
  hibd adds that site to GitHub connections made before M5. Your account needs Copilot. gh's
  token works, and so does a fine-grained token with the Copilot Requests permission. Classic
  tokens (`ghp_`) don't.
- The `dev` image pins Codex 0.160.0 and Copilot CLI 1.0.91 (`images/dev/versions`), with
  updates off. Rebuild it with `hib image build dev`, and recreate machines made before M5.
  Codex's model stream is a WebSocket, which the proxy doesn't scrub. In the live check, Codex
  0.160.0 sent no token in it ([research](docs/research/codex-chatgpt-auth.md), §11). Check
  again when a pin moves.

> `create-vm.sh` mirrors the steps that were done by hand on the current VM. It has been
> linted but not yet run from scratch. `up.sh` and `install-e2b.sh` have been run for real,
> including a full reinstall, and `make e2e-slow` reboots and deallocates the VM for real.

Useful extras:

```bash
./azure/ssh.sh                                   # shell on the VM
./azure/ssh.sh systemctl status "'e2b-*'"        # E2B's services
./azure/ssh.sh journalctl -fu e2b-orchestrator   # follow one service's logs
./azure/ssh.sh hibd ls                           # sandboxes, running and paused
./azure/ssh.sh journalctl -fu hibd-proxy         # follow the secrets proxy

# Use the E2B SDK from your Mac through an SSH tunnel:
./azure/ssh.sh -N -L 3000:localhost:3000 -L 3002:localhost:3002
```

With the tunnel open, point the SDK at it (the same variables `try.py` uses):

```bash
export E2B_API_KEY=e2b_53ae1fed82754c17ad8077fbc8bcdd90   # E2B's public local-dev key
export E2B_API_URL=http://localhost:3000
export E2B_SANDBOX_URL=http://localhost:3002
```

The API key is the fixed development key from E2B's `DEV-LOCAL.md`. It is not a secret. Only
SSH (port 22) is reachable from the internet.

### Command reference

This is what `hib help` prints. `hib help COMMAND` adds a command's flags and examples. Flags
take one dash or two: `--idle` and `-idle` are the same.

```text
usage: hib [--config DIR] COMMAND [ARG...]

hib makes machines on your Hibernaut host and runs them, over SSH. ssh NAME.hib logs in to
one, waking it if it's paused.

commands:
  init                       set this laptop up for a host
  new NAME                   make a machine
  ls                         list machines, and when each pauses unless it's busy
  rm NAME                    delete a machine and its disk for good
  pause NAME                 pause a machine; logging in wakes it
  fork MACHINE               copy a machine
  ssh NAME [-- CMD]          log in to a machine, or run CMD there
  exec NAME -- CMD [ARG...]  run CMD on a machine
  connect github             give machines your GitHub login
  connect codex              give machines your Codex login on a ChatGPT plan
  disconnect codex|github    remove a login from the host
  status                     show the health of the host, E2B, each secret and the Codex login
  secret add NAME            store a bound secret
  secret ls                  list bound secrets
  secret rm NAME             remove a bound secret
  log                        show secret swap audit entries
  image build NAME           build an image on the host, in every size
  image save MACHINE NAME    save a machine as an image, memory included
  image ls                   list built-in and saved images
  image rm NAME              remove a saved image no machine uses
  completion bash|zsh|fish   print a shell completion script
  bridge NAME.hib            connect to a machine's SSH server; ssh runs this for you
  version                    print the version
  help [COMMAND [SUB]]       show help for hib or a command

flags:
  --config DIR  the settings directory (default ~/.config/hibernaut)
  --version     print the version

examples:
  hib init --host azureuser@203.0.113.7
  hib new app
  ssh app.hib

exit codes:
  0    done
  1    failed
  2    used wrongly
  130  stopped by Ctrl-C (128 plus the signal's number)

run hib help COMMAND for a command's flags and examples.
```

## Development

```bash
make build lint test     # binaries in bin/; gofmt, go vet, staticcheck, shellcheck; unit tests
make generate            # regenerate E2B's clients from third_party/e2b
make deploy              # install this checkout's host pieces on the VM
make e2e                 # make up, then the engine contract and hibd against the real VM
make e2e-slow            # also reboots and deallocates the VM (~5 minutes more)
```

The e2e tests reach E2B through an SSH tunnel and label their sandboxes `hib-test=e2e`. A run
deletes labeled leftovers of earlier runs before it starts. e2e needs the VM, so it runs from your
Mac.

The GitHub tests connect your laptop's gh login and use the private fixture repo
`solev/hibernaut-e2e`. They remove the `e2e-` branches and PRs they make, and ones that killed
runs left. `TestWorkflowPush` skips until gh's token has the `workflow` scope.
`TestDeviceFlow` uses the host's own token instead. It skips unless `HIB_E2E_DEVICE_FLOW=1`.
Run it with `HIB_E2E_DEVICE_FLOW=1 go test -tags e2e -run '^TestDeviceFlow$' -v -timeout 40m ./e2e`
and approve the code by hand. The account you approve needs access to the fixture repo.

CI (`lint`, `test` and the `generate` check) is parked: GitHub Actions is off for this repo, so
nothing runs on push. Run `make lint test` before you push. To turn it back on:
`gh api -X PUT repos/solev/hibernaut/actions/permissions -F enabled=true -f allowed_actions=all`.

## Costs and auto-off

| Thing                           | Cost                                  |
| ------------------------------- | ------------------------------------- |
| D4s_v5 running (Sweden Central) | ~$0.20–0.23 / hour (~$150+/month if left on) |
| VM deallocated                  | $0 compute                            |
| 128 GB Standard SSD             | ~$10 / month, always                  |
| Static public IP (Standard SKU) | ~$3.65 / month, always                |

Four layers stop it running by accident:

1. **Idle deallocate** (`vm/idle-deallocate`). A systemd timer checks every minute. If for
   **30 minutes** there has been no authenticated SSH session, no running sandbox, and load average
   stayed under 1.0, the VM deallocates itself through the Azure API.
   - It authenticates with the VM's **managed identity** (no secrets stored on the box).
   - The identity only has a custom role, **"VM Self Deallocate"** (read + deallocate), scoped to
     this one VM.
   - Only real logged-in SSH sessions count. Internet scanners hitting port 22 don't keep it awake.
   - Logs: `journalctl -t idle-deallocate`. Settings: `/etc/default/idle-deallocate`.
2. **Nightly shutdown** at 20:00 UTC: a systemd timer on the VM (`hibernaut-nightly`) does the
   same, whether or not anyone is using it.
3. **Azure's auto-shutdown** at 21:00 UTC, a backstop in case the nightly shutdown failed.
4. **`make down`** when you're done.

**Sandboxes survive all of these.** Before deallocating, the VM runs `quiesce`: pause every
running sandbox, stop E2B so the snapshots finish writing, and flush to disk. If a sandbox can't
be paused, the VM stays up until Azure's backstop. Any clean shutdown or reboot, Azure's
included, also stops the `hibd-pause-all` guard, which pauses sandboxes before E2B stops.

## Decisions and lessons learned

- **No Spot VM.** Visual Studio Enterprise subscriptions cannot create Spot VMs. Every region
  returns `SkuNotAvailable`. So this uses a regular VM plus aggressive auto-off.
- **No B-series.** Burstable B-series VMs don't expose nested virtualization, so there is no
  `/dev/kvm` and Firecracker cannot run. D-series v5 does.
- **Security type "Standard"**, not Trusted Launch, to keep nested virtualization simple.
- **az CLI 2.90 on Python 3.14** crashes with "content for this response was already consumed"
  and hides the real error. Workaround: rerun with `--debug > log` and grep for the error code.
- **SSH is open to the internet, key-only.** An IP allowlist was rejected as impractical. We
  accepted this for an experiment and added hardening instead: no passwords, no root.
- **E2B runs under systemd**, started on boot, in place of the tmux session from the first
  experiment: `e2b-infra` (the containers), then `e2b-api`, `e2b-orchestrator` and
  `e2b-client-proxy`. Logs: `journalctl -u 'e2b-*'`.
- **E2B's pause returns before the snapshot is on disk.** The orchestrator writes it in the
  background, and waits for those writes only when it stops cleanly. So the VM never deallocates
  without stopping E2B first (`quiesce`).
- **Azure's deallocate shuts the VM down cleanly** (systemd reaches `poweroff.target`), so even
  Azure's backstop shutdown runs the pause-all guard.
- **E2B's data lives in `/var/lib/e2b`**, not in the repo checkout, so reinstalling E2B keeps
  templates and paused sandboxes. `install-e2b.sh --if-needed` reinstalls only when the pinned
  commit or the install files change, and pauses every sandbox before it rebuilds.
- **E2B is pinned** to commit `5cfcf3282ddb95e90a8214ce71329641bdfe6cb6`. Upstream moves fast.
  Change `E2B_INFRA_REF` in `vm/install-e2b.sh` to upgrade.
- Gaps in E2B's local-dev docs that the scripts work around:
  - **busybox** is not mentioned. It's downloaded from `gs://e2b-artifact-binaries/busybox/`.
  - The template build runs `npx tsx`, which waits on a y/n prompt → `npm_config_yes=true`.
  - The **API returns 503 until an orchestrator registers**, so the API's health check can only
    pass after the orchestrator starts.
  - The orchestrator needs **root** (network namespaces, NBD, KVM).
- **Azure resources are still named `e2b-test` / `rg-e2b-test`.** Azure VMs can't be renamed. Set
  `RESOURCE_GROUP` / `VM_NAME` env vars to create a fresh `hibernaut` VM instead.

## Limitations

- Single node, no high availability. This is a lab, not production.
- Uses E2B's local-dev configuration: a fixed API key, local storage, no TLS.
- No public sandbox URLs yet. You reach sandboxes through the SSH tunnel.

## Ideas for next missions

- **Custom templates** with our own toolchains (Node, Python, Rocket's stack) baked in.
- **Coding-agent workspaces**: give an agent a sandbox, pause it overnight, resume it tomorrow.
- **Public URLs** per sandbox through the client-proxy + a domain (hibernaut.sh / hibernaut.dev
  looked available).
