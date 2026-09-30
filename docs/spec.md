# Hibernaut v1 spec: machines that come ready

Status: **draft** · 2026-09-30 · Owner: Martin
Research behind it: [e2b-capabilities](research/e2b-capabilities.md) · [agent-auth](research/agent-auth.md)

## TL;DR

- A **machine** is a named, persistent Firecracker microVM on our host. It **sleeps when idle and
  wakes when you touch it**, with its files and running processes intact.
- You **connect GitHub and your AI tools once**. After that, every machine can `git clone` private
  repos and run `claude` / `codex` / `opencode` without any login.
- **Real credentials never enter a machine.** Machines hold look-alike **placeholders**. The host's
  egress proxy swaps in the real value only when a request goes to the right site
  (e.g. `github.com`).
- Everything runs over **SSH**: `hib new app` → `ssh app.hib`. No new ports are opened.
- It's glue around E2B (unmodified), written in Go: `hib` (laptop) and `hibd` (host). Machines run
  only stock tools (sshd, websocat, git, gh).

---

## 1. Goals and non-goals

**Goals (v1, single user):**

1. A ready machine in about a second, reachable with `ssh name.hib`.
2. Connect GitHub, Claude Code, Codex and OpenCode **once**. They work on every machine, including
   new ones.
3. No real credential stored inside any machine: not on disk, not in env vars, not in memory.
4. Idle machines pause automatically. Any access wakes them with processes intact.
5. Golden images and forks: save a machine as an image and start many machines from it, or clone
   a running machine N times.
6. The host costs ~nothing when unused and wakes on demand.

**Non-goals (later):** teams and multi-user, billing, a web console, public HTTPS URLs per machine,
egress allowlists, GPUs, non-Linux guests, integrations and automations, and the Grid.

## 2. The experience

One-time setup:

```bash
hib init                  # pick the host, register your SSH key, add "Host *.hib" to ~/.ssh/config
hib connect github        # shows a code → approve at github.com/login/device
hib connect claude        # paste a token from `claude setup-token` (or an Anthropic API key)
hib connect openai        # optional: OpenAI API key for Codex / OpenCode
```

Daily use:

```bash
hib new app                                     # ~1 s, from the "dev" image
ssh app.hib                                     # wakes it first if it was paused
  git clone https://github.com/solev/rocket && cd rocket
  claude                                        # already logged in
hib exec app -- claude -p "Add a /health endpoint" --dangerously-skip-permissions
hib ls · hib pause app · hib rm app · hib log   # hib log = which credentials were used where
```

Golden images:

```bash
hib image save app rocket        # capture app: repo, dependencies, dev server running
hib new task-1 --from rocket     # ready in ~1 s with all of that in place
hib fork app --count 3           # app-1..3 = copies of app as it is right now; app keeps running
```

## 3. Architecture

```
 Laptop                     Azure host (e2b-test)                              Machine (microVM)
 ──────                     ─────────────────────                              ─────────────────
 hib ──── ssh ───────────▶  hibd ──────▶ E2B API → orchestrator ─────────────▶ create/pause/resume
 ssh app.hib ── ssh ─────▶  hibd bridge ──▶ E2B proxy ─ websocket ───────────▶ websocat → sshd :22
                            vault  (real secrets, encrypted)
                            egress proxy ◀──── outgoing HTTPS ────────────────  git · gh · claude · codex
                               │  swap placeholder → real value, bound sites only
                               └──────────────▶ github.com · api.anthropic.com · api.openai.com
```

| Piece | Runs on | Job |
| --- | --- | --- |
| `hib` | laptop | The CLI. Talks to the host only over SSH. Starts the Azure VM if it's asleep. |
| `hibd` | host (systemd) | Machine names and state, vault, egress proxy, SSH bridge, idle policy. |
| E2B | host (systemd) | The engine: boots, pauses and resumes Firecracker VMs, and builds images. |
| `dev` image | E2B template | Linux, sshd + websocat (the SSH endpoint), git + gh (gh is git's credential helper), Node/Python, Claude Code/Codex/OpenCode, and our CA certificate. |

**Control channel = SSH.** Each `hib <cmd>` runs `ssh host hibd <cmd> --json`. SSH is already open
and key-based, so v1 needs no HTTPS API, domain, TLS certificates or API keys.

## 4. Credentials (the core idea)

### 4.1 One mechanism: secrets bound to sites

Every credential is a **bound secret**: a name, a real value (encrypted, host only), the sites it may
be sent to, and a **placeholder**.

- **Placeholder** = the same prefix and length as the real token plus randomness (e.g.
  `gho_hib…`), so tools accept it as a real token. It is stable, so rotating the real value changes
  nothing inside machines.
- **Delivery:** `hibd` writes a managed block of placeholders (`GH_TOKEN=…`, `CLAUDE_CODE_OAUTH_TOKEN=…`)
  into each machine's `/etc/environment`. It does this on create, on resume, and whenever secrets
  change. SSH sessions, `hib exec` and tmux all see it. (Not E2B's `envVars`: those reach only
  processes E2B starts, not SSH logins, and can't be changed after create.)
- **Swap:** the egress proxy replaces the placeholder with the real value only for requests to that
  secret's bound sites. Sent anywhere else, the placeholder is a meaningless string.

```
git clone https://github.com/solev/private-repo
  git ─▶ gh (credential helper) ─▶ "x-access-token : gho_hib7Qk…"   (placeholder)
  git ─▶ HTTPS to github.com ─▶ [host egress proxy]
                                  site bound to GITHUB_TOKEN? yes
                                  decode Basic auth, swap placeholder → real gho_… token
                                ─▶ github.com   ✓ clone works, and the machine never saw the token
```

### 4.2 Egress proxy

- **Interception (E2B stays unmodified):**
  - OSS E2B already sends every machine's outgoing TCP through its own firewall proxy, using
    per-machine iptables rules that it *appends* (`-i veth-<n> … -j REDIRECT`). That proxy only
    allows or blocks. It never decrypts, has no secret injection (that's E2B Cloud only), and can't
    be pointed at another proxy.
  - `hibd` inserts one rule *above* E2B's:
    `iptables -t nat -I PREROUTING 1 -i veth-+ -p tcp --dport 443 -j REDIRECT --to-port <hibd>`.
    All machine HTTPS reaches `hibd` first. `hibd` finds the real destination with
    `SO_ORIGINAL_DST`, as E2B's proxy does. Port 80 and all other ports stay with E2B.
  - Side effect: E2B's per-machine *domain* allowlists no longer cover 443 (unused in v1). Its block
    on private and internal addresses still applies, because it runs earlier, inside each machine's
    network namespace.
  - Which machine sent it: the source address is the machine's slot IP. E2B doesn't expose the
    slot → machine mapping, so `hibd` works it out on the host (S1). v1 needs this only for the
    audit log, because every machine has the same secrets.
  - Fallback: `HTTPS_PROXY` set in the machine. This is still safe, because a request that bypasses
    the proxy only carries a useless placeholder.
- **Most traffic is untouched.** The proxy reads the TLS SNI (the hostname) without decrypting:
  - **Not a bound site** (npm, PyPI, …): bytes pass straight through. Nothing is decrypted.
  - **Bound site:** the proxy terminates TLS with a certificate from the Hibernaut CA, which machines
    trust. It swaps the placeholder, then re-encrypts to the real site and verifies that site's real
    certificate.
- **Swap locations:** headers (including decoded Basic auth), the URL query, and small text bodies
  (JSON or form data up to 1 MB). All other traffic streams through unmodified (git packs, SSE).
- **Defense in depth:** if a response contains a real value, it's scrubbed back to the placeholder.
- **Audit log:** every swap is logged as time, machine, site, method, path and secret name. View it
  with `hib log`.
- **Trust inside the image:**
  - Our CA goes into `/usr/local/share/ca-certificates/` and `update-ca-certificates` runs. That
    covers git, curl, `gh` and other Go tools, and apt.
  - `NODE_EXTRA_CA_CERTS` points at our CA. That covers Node, npm, Claude Code and OpenCode (Bun).
  - `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE` and `CODEX_CA_CERTIFICATE` point at the **full system
    bundle** (`/etc/ssl/certs/ca-certificates.crt`), never at our CA alone. These variables
    *replace* the default roots, so our CA alone would break every site we don't intercept.
  - Tools with built-in roots or certificate pinning can't talk to *bound* sites. Everything else
    still works, because other sites pass through untouched.
  - E2B has a hook that installs a CA into machines (`caBundle`), but in OSS it's always empty and
    no API sets it. So our CA is baked into the image.

### 4.3 GitHub

**Connecting (on the laptop):**

- **Day one:** `hib connect github --from-gh` copies the laptop's `gh auth token` into the vault.
- **Proper v1:** GitHub's device flow (you approve a code in the browser) with our own "Hibernaut"
  GitHub OAuth App.
  - The app is created once by hand, with Device Flow enabled.
  - Scopes: `repo`, `read:org`, `workflow` (without it, agents can't push changes to CI files),
    `user:email`.
  - OAuth App tokens (`gho_…`) don't expire by default, so v1 has nothing to refresh.
- **Narrower access:** `hib connect github --token github_pat_…` accepts a fine-grained token
  limited to chosen repos.
- **Later:** a GitHub App, which limits access to selected repos and uses short-lived tokens that
  `hibd` refreshes (8 h tokens, 6-month refresh tokens).

**Inside machines:**

- `GH_TOKEN=<placeholder>` makes `gh` work. It takes precedence over any gh config, and gh won't
  write a login to disk while it's set.
- For git, the image runs `gh auth setup-git`, so `gh` is git's credential helper and answers with
  the `GH_TOKEN` placeholder. git sends it as Basic auth, which the proxy decodes and swaps.
- `git@github.com:` URLs are rewritten to `https://github.com/`.
- git `user.name` / `user.email` come from the GitHub profile, fetched once at connect.
- Bound sites: `github.com`, `api.github.com`, `uploads.github.com`.

### 4.4 AI tools

| Tool | Connect once with | Inside the machine | Notes |
| --- | --- | --- | --- |
| Claude Code | **Subscription:** `claude setup-token` on your laptop (Pro/Max). **Or** an API key. | `CLAUDE_CODE_OAUTH_TOKEN` **or** `ANTHROPIC_API_KEY` placeholder, never both (the API key wins) | Swapped on `api.anthropic.com`. Onboarding pre-completed in the image. Runs as non-root `user`, which `--dangerously-skip-permissions` requires. `--bare` ignores subscription tokens. |
| Codex | **API key** or a ChatGPT plan (below) | `CODEX_API_KEY` placeholder | Swapped on `api.openai.com` |
| OpenCode | provider API keys | `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, … placeholders | Never log in to a Claude subscription through OpenCode: users report account bans (anomalyco/opencode#6930) |

**Codex with a ChatGPT plan** (S6). Options, best first:

1. `CODEX_ACCESS_TOKEN` (an `at-…` token meant for CI). If it bills to the ChatGPT plan, it's a
   static token and swaps like an API key.
2. **Placeholder login file.**
   - Codex reads its ChatGPT login from `~/.codex/auth.json` and decodes the JWTs there without
     checking their signatures.
   - `hibd` writes an auth.json with look-alike JWTs: same claims, far-future expiry, fake
     signature. The proxy swaps them for the real token on `chatgpt.com`.
   - `hibd` refreshes the real token on the host. So there are no refresh races between machines,
     and no real token in any machine.
3. Fallback: copy the real auth.json into the machine (the boxd way). The real token then sits in
   the machine, and rotating refresh tokens can log other machines out.

**Policy:**

- Anthropic's Agent SDK docs say third-party developers may not offer claude.ai login or rate
  limits in their products without approval.
- Using your own `setup-token` in the official CLI is what that command is for. So v1 (just you)
  is fine.
- A hosted Hibernaut for other people should default to API keys, and settle the subscription
  question with Anthropic (and OpenAI) first.

### 4.5 Threat model (v1)

| If an agent or a prompt injection… | Then |
| --- | --- |
| reads env vars or files | it gets placeholders only |
| sends them to its own server | the placeholder isn't swapped, so it's useless |
| uses a credential against the real site (e.g. deletes a repo) | **not prevented in v1.** It shows in the audit log. Mitigation: narrower tokens (fine-grained, GitHub App). |
| gets hold of a golden image | it only contains placeholders (multi-user will need a per-machine identity check) |
| connects to the host's own services (E2B API, databases, `hibd`) or Azure's metadata endpoint | blocked by E2B: each machine's firewall drops traffic to private, loopback and link-local addresses, including `169.254.169.254`. Still reachable: E2B's internal service address (`192.0.2.1`) and the host's public IP, where the NSG allows only key-based SSH. `hibd` listens on localhost, apart from the interception port, which accepts only machine interfaces. Live check in M3 (S8). |

**Host compromise** exposes the vault. Later mitigation: keep the vault key in Azure Key Vault,
accessed through the VM's managed identity.

## 5. Machines

### Names and state

- `hibd` stores name ↔ E2B sandbox ID in SQLite. The name is also written into E2B metadata
  (`hib.name`) so the mapping can be rebuilt. E2B metadata stays in the control plane (the machine
  can't see it) and can be filtered with `GET /v2/sandboxes?metadata=…`.
- States: running → paused (idle) → deleted. The host itself can also be asleep.
- Every machine is created with `autoPause: true`, memory pauses (never filesystem-only, which
  can't auto-resume) and `autoResume: {policy: any}`.

### Idle and wake

E2B does the pausing and waking. `hibd` only decides what counts as busy.

- **Pause:** every machine has a 15-minute E2B timeout (configurable; `0` = never pause). When it
  runs out, E2B pauses the machine instead of killing it.
- **Busy** means any of:
  - an SSH session through the bridge (`hib exec` runs over SSH too),
  - CPU use above a small threshold (`GET /sandboxes/{id}/metrics`),
  - an agent process alive (`claude`, `codex`, `opencode`), even when it's idle waiting on an LLM.
    `hibd` checks with `pgrep` through envd.
- `hibd` checks running machines every 60 s. While a machine is busy, it pushes the timeout back
  (`POST /sandboxes/{id}/timeout`). Nothing inside a machine calls the host.
- It polls only machines E2B reports as running. Any call to a paused machine, even a status
  check, would wake it.
- **Wake:**
  - Traffic to a paused machine through E2B's proxy resumes it, and SSH goes through that proxy.
  - `hib` commands resume explicitly with `POST /v2/sandboxes/{id}/connect`.
  - Resume takes ~0.4 s.
- **The host:**
  - Running machines live in RAM and die with the host.
  - Paused machines survive: they live in Postgres and in snapshot files on disk. Templates have
    already survived many deallocations.
  - Restarting E2B loses running machines too: its routing state (Redis) isn't saved to disk.
  - So before the host deallocates (idle or nightly) or E2B restarts, `hibd pause-all` pauses every
    machine. idle-deallocate also waits while any machine is running.
  - When `hib` can't reach the host, it starts the Azure VM (cold start is a few minutes).

### SSH path

```
ssh app.hib ─▶ ProxyCommand: hib bridge app ─▶ ssh host "hibd bridge app"
            ─▶ (resume if paused) ─▶ E2B proxy, websocket to port 8081 ─▶ websocat ─▶ sshd :22
```

- This is E2B's documented SSH pattern: the image runs `websocat` on port 8081, forwarding to
  sshd. `hibd bridge` talks to E2B's proxy on localhost, so it needs no public domain or TLS.
- The laptop-to-machine SSH session is end-to-end encrypted; the host only relays bytes.
- E2B's proxies close connections that are idle for ~10 minutes (610 s / 620 s), so `hib init`
  sets `ServerAliveInterval 60` for `*.hib`.
- E2B's proxy isn't exposed outside the host. If it ever is, create machines with E2B's private
  ingress, and have `hibd` send each machine's `trafficAccessToken`.
- `hib exec app -- cmd` is `ssh app.hib -- cmd`. Interactive sessions and exec then get the same
  user, environment and wake-up path.
- The machine user is `user`, with passwordless sudo. Your public key is baked into your images
  (single user). Later it's written at create time.
- **Host keys:** in v1 all machines from one image share its host key, which `hib` pins in its own
  `known_hosts`. Later, SSH host certificates.

### Images and fork

All three are native E2B operations. Hibernaut only wraps them.

| Command | E2B call | Result |
| --- | --- | --- |
| `hib image build dev` | template build from `images/dev/Dockerfile` | The base image all machines start from |
| `hib image save app rocket` | `POST /sandboxes/{id}/snapshots` `{name: "rocket"}` | An image with memory, so processes keep running; it outlives `app` |
| `hib fork app --count 3` | `POST /sandboxes/{id}/fork` | `app` pauses for a moment and keeps running; 3 new machines in the same state |

Copies carry everything in the machine. That's fine, because a machine holds only placeholders and
your public key. They also share the SSH host key (see SSH path).

## 6. Decisions

| # | Decision | Why |
| --- | --- | --- |
| D1 | **SSH is the only control channel in v1** | Already open and key-based. No certs, domains or API keys to run. |
| D2 | **Placeholder swap is the one credential mechanism** | One design covers GitHub, Anthropic, OpenAI and any API. Stronger than syncing tokens into machines. |
| D3 | **Single user first** | Lets us bake keys and placeholders into images. Per-machine identity comes with multi-user. |
| D4 | **Glue around E2B, behind an `Engine` interface** | No fork of E2B to maintain. Swappable later (plain Firecracker, Cloud Hypervisor, …). |
| D5 | **Go for `hib` and `hibd`** | Static binaries, the same ecosystem as E2B, and first-class TLS/HTTP/SSH libraries. |
| D6 | **The host never listens to machines** | `hibd` polls machines through envd. Machines have no path to host services except the proxied web. |

## 7. Implementation plan

Each milestone ends with a demo you can run.

| # | Milestone | Demo |
| --- | --- | --- |
| M0 | E2B and `hibd` under systemd, started on boot. E2B's storage moves out of the repo checkout (`~/infra/packages/orchestrator/tmp/`) to `/var/lib/e2b`. `hibd pause-all` runs before the host deallocates. | Reboot host → `hib ls` works, and a machine paused before the reboot resumes (S4) |
| M1 | `dev` image; `hib new / ls / rm`; SSH bridge and `*.hib`; `hib exec` | `hib new a && ssh a.hib`, `hib exec a -- uname -a` |
| M2 | Idle pause and wake on access | `ssh a.hib` → leave it 15 min → `ssh a.hib`: tmux session still there |
| M3 | Vault and egress proxy (iptables hook, swap, audit log) | Dummy secret bound to a test site arrives swapped; `env` in the machine shows only the placeholder; the machine can't reach host services (S8) |
| M4 | GitHub connect, git/gh wiring | Fresh machine: `git clone` a private repo, `gh pr list` |
| M5 | Claude Code, Codex, OpenCode | `hib exec a -- claude -p "say hi"` with no login |
| M6 | Golden images and fork | `hib new t1 --from rocket` → repo, dependencies and running app in place; `hib fork t1 --count 3` |

Repo layout:

```
cmd/hib/          laptop CLI
cmd/hibd/         host daemon (+ subcommands the CLI calls over SSH)
internal/         engine (E2B), machines, vault, egress, sshbridge
images/dev/       Dockerfile + files baked into machines (CA cert, gitconfig, agent configs)
azure/  vm/       existing host provisioning
```

State: SQLite (pure Go). Secrets are encrypted with AES-GCM using a root-only key file (later: Azure
Key Vault through the VM's managed identity).

## 8. Open questions and spikes

| # | Question | Status |
| --- | --- | --- |
| S1 | Can `hibd` intercept machine HTTPS without breaking E2B, and tell which machine sent it? | **Hook: answered in code.** E2B appends its redirects, so one rule inserted above them wins (§4.2). **Open:** the slot IP → machine mapping (E2B's API doesn't expose it), and a live test that the hook survives E2B adding and removing machines. Spike before M3. |
| S2 | Can E2B's proxy carry a long-lived SSH websocket, and wake a paused machine? | **Yes.** It's E2B's documented SSH pattern, and traffic on any port resumes a paused machine. Needs keepalives (~10-min idle cutoff). |
| S3 | Can E2B fork a machine or turn it into a template? | **Yes, natively** (§5 Images and fork). |
| S4 | Do paused machines survive a host reboot? | **Yes, in principle.** Postgres and snapshot files persist; Redis doesn't. Live test in M0. |
| S5 | Does Claude Code work with a placeholder `CLAUDE_CODE_OAUTH_TOKEN` through the proxy? | **Open.** Live test in M5: any local token checks, which extra hosts it calls, and how long a `setup-token` token really lasts. |
| S6 | Can Codex on a ChatGPT plan work with no real token in the machine? | **Open.** Does an `at-…` token bill to the plan? Does the placeholder-JWT auth.json work end to end? |
| S7 | Which env vars and config does each tool need to trust our CA and run unattended? | **Answered** (§4.2 Trust, §4.4). |
| S8 | Can machines reach host services or the Azure metadata endpoint? | **No, per E2B's code:** private, loopback and link-local addresses are dropped (§4.5). Live check in M3. |

Research: [e2b-capabilities](research/e2b-capabilities.md) (§10 covers egress, `caBundle` and SSH) ·
[agent-auth](research/agent-auth.md).
