# Ideas

The detail behind [the goals](goals.md), and ideas no goal has taken yet. Nothing here is planned
until a goal or a milestone takes it. Fixes and leftovers from v1 are under **Later** in
[the milestones](milestones/README.md#later-not-v1).

## The pitch

"Give every coding agent its own computer, on your own server."

- The combination is what's rare: named machines you reach with plain `ssh`, that sleep with their
  memory, wake when you connect, fork while they run and use your ChatGPT or Copilot plan without
  ever seeing the login.
- The story: one person built a self-hosted cloud for coding agents, with coding agents.
- The field is crowded, and that's fine. The awesome-ai-coding-sandboxes list had 38 providers in
  August 2026. It ranks them by isolation, then by egress control, and its strongest group is
  microVMs with restricted egress and brokered secrets. Several self-hosted microVM sandboxes
  already broker secrets: Cleanroom, smolvm, microsandbox, BoxLite, OpenComputer and Mitos. The
  outbound allowlist (G2) puts Hibernaut in that group.

## Videos

Short clips of real scenarios, filmed on today's host:

- **The laptop shuts, the agent keeps going.** Start Codex on a task over `ssh`, close the laptop
  and come back to finished work.
- **One agent, three ways.** Mid-task, `hib fork agent --count 3` copies the running machine, and
  each copy tries a different approach. On today's host that takes small machines: it holds six
  small ones awake, or three medium.
- **A prompt injection steals nothing.** An agent in YOLO mode is tricked into sending its GitHub
  token away. All it has is a placeholder, which the host swaps for the real token only on the
  way to GitHub.
- **A week later, it's all still there.** The dev server is still running as you left it, back in
  under a second.
- **How it's built.** One person and coding agents, with E2B's Firecracker machines underneath.

## G2. Ready for strangers

### Lock the host down

On Azure, the network security group lets in only SSH. That hides what E2B's local setup opens,
which on a bare server faces the internet:

- Its containers publish on every interface: Postgres with the default password (5432), Redis
  with none (6379), ClickHouse (8123, 9000), Grafana with an anonymous admin (53000), Loki
  (3100), Memcached (11211), OpenTelemetry (4317, 4318), a SOCKS5 proxy with a known password
  (1080) and Vector (30006).
- E2B's API listens on `0.0.0.0`.
- Machines can reach the host's public IP and E2B's internal service address (spec §4.5).

So bind the containers to `127.0.0.1`, add a default-deny input firewall and drop traffic from
machines to the host's public IP, as `vm/e2b/sandbox-firewall.nft` already does for Azure's
WireServer. Check what E2B's internal service address (`192.0.2.1`) offers them, and which of
E2B's dev extras it needs at all (Grafana, Loki, Mimir, Tempo, the SOCKS5 proxy). Each one dropped
also frees memory.

`hibd`'s own two ports that machines reach, the egress interception port and
[M12](milestones/m12-machine-links.md)'s link port, bind every interface because a redirect
rewrites the destination per slot. They accept only connections that arrived on a machine's veth,
so the default-deny input firewall should drop them from anywhere else rather than rely on that
check alone.

### Outbound allowlist

Machines reach the whole internet today (spec §1 lists allowlists as not in v1). Add an allowlist
per machine, with deny by default as an option: GitHub, package registries and model APIs. The
sandbox lists rank on it, and the try server needs it.

### Install anywhere

- One command on any x86 Linux server with KVM, Ubuntu 24.04 first. Hetzner's dedicated servers
  have KVM, and its cloud servers don't. AWS (since 12 February 2026: C7i, M7i, R7i, C8i, M8i,
  R8i and more), Google Cloud and Azure offer nested virtualization on ordinary VMs.
- Size the host from its hardware. `install-e2b.sh` fixes 3200 huge pages (6.25 GiB) and 64 NBD
  devices, enough for today's 16 GB host. The huge pages should come from the RAM, leaving
  enough for E2B's services. Each awake machine uses one NBD device, and E2B suggests 4096.
- Make nothing depend on Azure: `hib` should work with any host it reaches over SSH, with the
  Azure parts (starting the VM on `ssh`, idle deallocate, the nightly shutdown) optional.
- The installer is amd64 only. ARM can wait.

### Claude Code

- The egress proxy swaps only the `Authorization` header (`internal/egress/swap`). Anthropic's
  API keys travel in `x-api-key`, so swap that too.
- `hib connect claude` through `claude setup-token`, whose subscription token Claude Code reads
  from `CLAUDE_CODE_OAUTH_TOKEN` and sends as `Authorization: Bearer`.
- OpenCode after it.

### Releases

Prebuilt `hib` and `hibd` with GoReleaser, and a Homebrew tap. Release downloads count installs
with no telemetry.

### Going public

- License: Apache-2.0, the same as E2B's infrastructure.
- Scrub the README, which shows the host's real IP and Azure resource names.
- Publish a fresh history, so nothing private in old commits goes public.
- Check that nothing else stands in the way, the author's employment terms included.

## G3. A try server

### How it works

- `ssh NAME@try.DOMAIN`, with NAME a GitHub username. The server checks the key against
  `github.com/NAME.keys`, so there's no signup. Accounts younger than about 30 days are turned
  away.
- Commands over `ssh`: `new`, `ls`, `fork` and `rm`. Machines are small (1 vCPU, 1 GiB). Open:
  how many each person may have, so `fork` can be shown.
- Credits are awake hours a day. A sleeping machine costs only disk, so sleep is free.
- A machine nobody used for a week is deleted.
- No stored logins in the first version. People log in to Codex or Claude Code inside their
  machine, and nothing goes in the host's vault.
- A privacy note and terms.

### What Hibernaut needs for it

- An SSH front door that checks GitHub keys and routes each person to their own machines.
- Machines with owners, and per-owner quotas, awake-hour accounting and expiry in `hibd`. This is
  where multi-user starts: an identity per machine, SSH keys written at create time, SSH host
  certificates.
- CPU caps. E2B gives each machine its own cgroup (`/sys/fs/cgroup/e2b/sbx-ID-RUN`, new on every
  start and resume) but sets no limit in it. So `hibd` would write `cpu.max` there after each
  start and resume.
- Network and disk caps. Firecracker can rate-limit both, and E2B has flags for it
  (`tcpfirewall-egress-throttle-config`, `block-drive-throttle-config`), off by default. Without
  LaunchDarkly the flags keep their built-in values, so this takes a LaunchDarkly key, a patch to
  E2B or `tc` on each machine's network device.
- The outbound allowlist (G2), open to GitHub, package registries and model APIs only.
- No public URLs.
- A kill switch: one command stops new machines and pauses the running ones.

### Abuse

Crypto mining is the risk: Hetzner's terms ban it (§8.3), and a miner can get the whole server
cancelled. The allowlist keeps mining pools out of reach. CPU caps and awake-hour credits limit
what a miner gets, and account age and the kill switch slow abusers down.

### Cost

A Hetzner dedicated server of its own, at prices checked in October 2026:

- AX41, a limited offer: Ryzen 5 3600, 64 GB, 2 × 512 GB NVMe, €57.30 a month, no setup fee.
- AX42: 64 GB DDR5, €97.30 a month plus €49 setup.

Memory sets how many fit: an awake machine holds all its RAM, a sleeping one none. 64 GB holds
about 50 small or 25 medium machines awake at once. When it's full, people wait in a queue.

## G5. Hosted service

At Hetzner's prices since 15 June 2026:

- An AX102 (16 cores, 128 GB) is €257.30 a month plus €129 setup, or €157.30 plus €39 as a
  limited offer. It holds about 50 medium machines awake at once.
- So a machine-hour costs about €0.007 with the server full, and €0.024 at 30% use. At €0.075 a
  machine-hour, it pays for itself at about 3,400 paid hours a month.
- Others charge about $0.13 an hour for a medium machine (E2B, Daytona), and Codespaces $0.18 for
  its smallest.
- Free credits of $5 to $10 cost little at these prices. Ask for a card before more, so abuse
  costs the abuser.
- Hetzner's cloud servers have no KVM, so it's dedicated servers only.

## Other ideas

- Narrower machine links, after [M12](milestones/m12-machine-links.md) ships the first version:
  `hib port add api 8080 --from worker`, so a published port is reachable from named machines
  rather than all of them; `hib new --isolated`, a machine that can neither reach a link nor have
  one published; opt-in named networks, where machines in the same network reach each other on
  every port, which is boxd's default and ours only if someone asks; and UDP.
- A domain (hibernaut.dev and hibernaut.sh both looked free once), public HTTPS URLs per machine
  and a web console.
- Codex access tokens (`hib connect codex --access-token`), for ChatGPT Business and Enterprise.
- A GitHub App, for access to selected repos only with short-lived tokens.
- The vault key in Azure Key Vault, through the VM's managed identity.
- Images built from a Dockerfile, with a team's toolchains baked in. Today you set up a machine
  and `hib image save` it.

## Parked: fundraising

Not a goal now, kept in case that changes.

- **EIC Accelerator:** a grant of up to €2.5M, with no equity taken, for the next stage of the
  technology: our own snapshot and fork engine in place of E2B's. The short proposal is 12 pages,
  10 slides and a 3-minute video, reviewed on the first Tuesday of each month. It needs a team of
  2 or 3 with a systems and virtualization engineer. The grant covers 70% of the costs, so it
  also needs a plan for the rest.
- **The evidence it would need:** 10 to 15 interviews with EU CTOs or platform teams that run
  coding agents, a waitlist with sign-ups counted and 2 or 3 free design-partner pilots with
  signed letters of intent.
- **NLnet:** €5,000 to €50,000, next deadline 3 November 2026. Its policy on generative AI won't
  pay for purely AI-generated work.
