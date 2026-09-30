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
- Setup is scripted: `./azure/up.sh` starts everything, `./azure/down.sh` stops paying.
- **Next:** the [v1 spec](docs/spec.md) plans named machines you `ssh` into, with GitHub and AI
  tools connected once and no real credentials inside machines. Progress is tracked in
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
  ├── E2B control plane        (tmux session "e2b")
  │     api            :3000   create / list / kill sandboxes, API keys, templates
  │     orchestrator   :5008   actually boots Firecracker VMs, handles snapshots
  │     client-proxy   :3002   routes traffic into sandboxes
  │     infra (docker)         Postgres, ClickHouse, Redis, Grafana/observability
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
azure/                     Runs on your Mac (needs az CLI + ssh)
  common.sh                Shared settings: RG, VM name, size, region, user. Env vars override.
  create-vm.sh             One-time: create RG, VM, identity, custom role, auto-shutdown, host setup
  up.sh                    Start VM → sync files → install E2B (first time only) → start E2B
  down.sh                  Deallocate the VM (billing for compute stops)
  ssh.sh                   SSH to the VM; extra args are passed through

vm/                        Runs on the VM (synced to ~/hibernaut/vm)
  setup-host.sh            sshd hardening + idle-deallocate timer (root)
  install-e2b.sh           Idempotent installer for E2B and all its dependencies
  start-e2b.sh             Starts every E2B service in tmux, waits for health, builds base template
  idle-deallocate/         Script + systemd service/timer + config for auto-deallocation
  sshd/00-hardening.conf   No root login, no passwords, keys only

examples/
  try.py                   Create → run → pause → resume → kill, with timings

docs/
  spec.md                  v1 plan: machines, SSH, credential swap, testing, open spikes
  milestones/              M0–M7: tasks, tests and status for each milestone
  research/                Cited research behind the spec (E2B internals, agent/GitHub auth)
```

## Quick start

Prerequisites: Azure CLI logged in (`az login`), an SSH key at `~/.ssh/id_ed25519`, and rsync.

```bash
./azure/create-vm.sh     # first time only
./azure/up.sh            # start VM + E2B (first run installs everything and takes a while; later runs ~2–3 min)
./azure/ssh.sh hibernaut/.venv/bin/python hibernaut/examples/try.py
./azure/down.sh          # stop paying for compute
```

Everyday use is `up.sh` → play → `down.sh` (or just walk away and let it deallocate itself).

> `create-vm.sh` mirrors the steps that were done by hand on the current VM. It has been
> linted but not yet run from scratch. `up.sh`, `install-e2b.sh` and `start-e2b.sh` have been run
> for real, including a cold start from a deallocated VM.

Useful extras:

```bash
./azure/ssh.sh                             # shell on the VM
./azure/ssh.sh -t tmux attach -t e2b       # watch the E2B services (Ctrl-b d to detach)

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

## Costs and auto-off

| Thing                           | Cost                                  |
| ------------------------------- | ------------------------------------- |
| D4s_v5 running (Sweden Central) | ~$0.20–0.23 / hour (~$150+/month if left on) |
| VM deallocated                  | $0 compute                            |
| 128 GB Standard SSD             | ~$10 / month, always                  |
| Static public IP (Standard SKU) | ~$3.65 / month, always                |

Three layers stop it running by accident:

1. **Idle deallocate** (`vm/idle-deallocate`). A systemd timer checks every minute. If there has
   been no authenticated SSH session and load average stayed under 1.0 for **30 minutes**, the VM
   deallocates itself through the Azure API.
   - It authenticates with the VM's **managed identity** (no secrets stored on the box).
   - The identity only has a custom role, **"VM Self Deallocate"** (read + deallocate), scoped to
     this one VM.
   - Only real logged-in SSH sessions count. Internet scanners hitting port 22 don't keep it awake.
   - Logs: `journalctl -t idle-deallocate`. Settings: `/etc/default/idle-deallocate`.
2. **Nightly auto-shutdown** at 20:00 UTC (Azure's built-in schedule, which deallocates).
3. **`./azure/down.sh`** when you're done.

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
- **Services run in tmux, not systemd.** That keeps them easy to watch and restart while
  experimenting. `start-e2b.sh` skips services that are already healthy, so it's safe to rerun.
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
- Move services from tmux to systemd if this graduates beyond an experiment.
