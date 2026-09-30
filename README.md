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
- **Where it's going:** the [v1 spec](docs/spec.md) plans named machines you `ssh` into, with
  GitHub and AI tools connected once and no real credentials inside machines. M0 (foundations) is
  built and tested; M1 (machines) is next. Progress: [docs/milestones](docs/milestones/README.md).

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
cmd/hib/                   Laptop CLI (only `version` so far)
cmd/hibd/                  Host tool: `pause-all`, `ls`
internal/engine/           The Engine interface; e2b/ (real), fake/ (in memory), enginetest/ (the
                           contract both must pass)
internal/pauseall/         Pauses every running sandbox, within a time limit
e2e/                       End-to-end tests against the real VM (build tag e2e)
third_party/e2b/           E2B's API spec and envd proto at the pinned commit; clients are
                           generated from them
Makefile                   build, lint, test, generate, deploy, up, down, e2e, e2e-slow
.github/workflows/ci.yml   lint, test, and a check that generated code is current

azure/                     Runs on your Mac (needs az CLI + ssh)
  common.sh                Shared settings: RG, VM name, size, region, user. Env vars override.
  create-vm.sh             One-time: create RG, VM, identity, custom role, auto-shutdown, host setup
  up.sh                    Start VM → deploy → install E2B if needed → start E2B → wait until healthy
  deploy.sh                Build hibd, sync vm/ and examples/, install the host pieces
  start.sh                 Start the VM and wait for SSH; starts nothing else
  down.sh                  Pause every sandbox, stop E2B, deallocate (billing for compute stops)
  ssh.sh                   SSH to the VM; extra args are passed through
  target.sh, state.sh      Print the VM's user@ip, or its power state

vm/                        Runs on the VM (synced to ~/hibernaut/vm)
  setup-host.sh            Host pieces (root): sshd hardening, hibd, quiesce, guard, timers
  install-e2b.sh           Installs E2B and its dependencies; --if-needed skips an installed version
  wait-e2b.sh              Waits until E2B is healthy; --start starts it first
  lib/quiesce              Pause every sandbox, stop E2B, flush to disk. Runs before any deallocate.
  lib/deallocate-self      Deallocates this VM through its managed identity
  lib/e2b.sh               E2B's units and health checks, shared by the scripts above
  lib/e2b-infra-ready      Waits until E2B's databases accept connections
  systemd/                 E2B's units, the pause-all guard, the nightly timer
  idle-deallocate/         Script + systemd service/timer + config for auto-deallocation
  e2b/, hibd/              Settings for E2B's orchestrator and for hibd
  sshd/00-hardening.conf   No root login, no passwords, keys only

examples/
  try.py                   Create → run → pause → resume → kill, with timings

docs/
  spec.md                  v1 plan: machines, SSH, credential swap, testing, open spikes
  milestones/              M0–M7: tasks, tests and status for each milestone
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

> `create-vm.sh` mirrors the steps that were done by hand on the current VM. It has been
> linted but not yet run from scratch. `up.sh` and `install-e2b.sh` have been run for real,
> including a full reinstall, and `make e2e-slow` reboots and deallocates the VM for real.

Useful extras:

```bash
./azure/ssh.sh                                   # shell on the VM
./azure/ssh.sh systemctl status "'e2b-*'"        # E2B's services
./azure/ssh.sh journalctl -fu e2b-orchestrator   # follow one service's logs
./azure/ssh.sh hibd ls                           # sandboxes, running and paused

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

## Development

```bash
make build lint test     # binaries in bin/; gofmt, go vet, staticcheck, shellcheck; unit tests
make generate            # regenerate E2B's clients from third_party/e2b
make deploy              # install this checkout's host pieces on the VM
make e2e                 # make up, then the engine contract and hibd against the real VM
make e2e-slow            # also reboots and deallocates the VM (~5 minutes more)
```

The e2e tests reach E2B through an SSH tunnel and label their sandboxes `hib-test=e2e`. A run
deletes labeled leftovers of earlier runs before it starts. CI runs `lint`, `test` and the
`generate` check on every push; e2e needs the VM, so it runs from your Mac.

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
- **Fork**: resume one snapshot into several sandboxes to try things in parallel.
- **Public URLs** per sandbox through the client-proxy + a domain (hibernaut.sh / hibernaut.dev
  looked available).
