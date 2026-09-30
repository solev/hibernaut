# Hibernaut v1 spec: machines that come ready

Status: **draft** · 2026-09-30 · Owner: Martin
Research behind it: [e2b-capabilities](research/e2b-capabilities.md) · [agent-auth](research/agent-auth.md) ·
[codex-chatgpt-auth](research/codex-chatgpt-auth.md) · [copilot-auth](research/copilot-auth.md)

## TL;DR

- A **machine** is a named, persistent Firecracker microVM on our host. It **sleeps when idle and
  wakes when you touch it**, with its files and running processes intact.
- You **connect GitHub and Codex once**. After that, every machine can `git clone` private repos and
  run `codex` and `copilot` (GitHub Copilot CLI) without any login.
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
2. Connect GitHub and Codex (ChatGPT plan) **once**. They work on every machine, including new
   ones, and so does the GitHub Copilot CLI, which uses the GitHub connection.
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
hib init                       # pick the host, register your SSH key, add "Host *.hib" to ~/.ssh/config
hib connect github --from-gh   # copies your laptop's gh login; the Copilot CLI uses it too
hib connect codex              # shows a code → approve it in the browser (your ChatGPT plan)
```

Daily use:

```bash
hib new app                                     # ~1 s, from the "dev" image
ssh app.hib                                     # wakes it first if it was paused
  git clone https://github.com/solev/rocket && cd rocket
  codex                                         # already logged in
  copilot                                       # already logged in
hib exec app -- 'cd rocket && codex exec "Add a /health endpoint"'
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
                            egress proxy ◀──── outgoing HTTPS ────────────────  git · gh · codex · copilot
                               │  swap placeholder → real value, bound sites only
                               └──────────────▶ github.com · chatgpt.com · githubcopilot.com
```

| Piece | Runs on | Job |
| --- | --- | --- |
| `hib` | laptop | The CLI. Talks to the host only over SSH. Starts the Azure VM if it's asleep. |
| `hibd` | host (systemd) | Machine names and state, vault, egress proxy, SSH bridge, idle policy. |
| E2B | host (systemd) | The engine: boots, pauses and resumes Firecracker VMs, and builds images. |
| `dev` image | E2B template | Linux, sshd + websocat (the SSH endpoint), git + gh (gh is git's credential helper), Node/Python, Codex, the Copilot CLI, and our CA certificate. |

**Control channel = SSH.** Each `hib <cmd>` runs `ssh host hibd <cmd> --json`. SSH is already open
and key-based, so v1 needs no HTTPS API, domain, TLS certificates or API keys.

## 4. Credentials (the core idea)

### 4.1 One mechanism: secrets bound to sites

Every credential is a **bound secret**: a name, a real value (encrypted, host only), the sites it may
be sent to, and a **placeholder**.

- **Placeholder** = the same prefix and length as the real token plus randomness (e.g.
  `gho_hib…`), so tools accept it as a real token. It is stable, so rotating the real value changes
  nothing inside machines.
- **Delivery:** `hibd` writes a managed block of placeholders (`GH_TOKEN=…`, …) into each
  machine's `/etc/environment`. Tools that read a login file instead (Codex, §4.4) get that file
  with placeholders in it. `hibd` writes both on create, on resume, and whenever secrets change.
  SSH sessions, `hib exec` and tmux all see them. (Not E2B's `envVars`: those reach only processes
  E2B starts, not SSH logins, and can't be changed after create.)
- **Swap:** the egress proxy replaces the placeholder with the real value only for requests to that
  secret's bound sites. Sent anywhere else, the placeholder is a meaningless string.
- **Health:** `hib status` tests each secret against its site (for GitHub, `GET /user`) and shows
  whether it still works.

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
- **Token endpoints:** some tools trade their login for fresh tokens: Codex refreshes, and Copilot
  may exchange (S9). The proxy handles these few endpoints itself, so a new real token never
  reaches a machine. It either answers the request with placeholders, or keeps the new real token
  on the host and passes the machine a placeholder for it (§4.4).
- **Audit log:** every swap is logged as time, machine, site, method, path and secret name. View it
  with `hib log`.
- **Trust inside the image:**
  - Our CA goes into `/usr/local/share/ca-certificates/` and `update-ca-certificates` runs. That
    covers git, curl, `gh` and other Go tools, and apt.
  - `NODE_EXTRA_CA_CERTS` points at our CA. That covers Node, npm and the Copilot CLI.
  - `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE` and `CODEX_CA_CERTIFICATE` point at the **full system
    bundle** (`/etc/ssl/certs/ca-certificates.crt`), never at our CA alone. These variables
    *replace* the default roots, so our CA alone would break every site we don't intercept.
  - Tools with built-in roots or certificate pinning can't talk to *bound* sites. Everything else
    still works, because other sites pass through untouched.
  - E2B has a hook that installs a CA into machines (`caBundle`), but in OSS it's always empty and
    no API sets it. So our CA is baked into the image.

### 4.3 GitHub

**Connecting (on the laptop):**

- **First (M4): `hib connect github --from-gh`** copies the laptop's `gh auth token` into the
  vault. Zero setup.
  - It's gh's own token (`gho_…`, no expiry) with gh's scopes: `repo`, `read:org`, `gist`.
  - Without `workflow`, agents can't push changes to `.github/workflows/`. `hib connect` checks
    the scopes and suggests `gh auth refresh -s workflow` first.
  - The laptop and the host share this token. GitHub keeps at most ten tokens per user, app and
    scope set, so logging gh in on many devices can revoke it. `hib status` shows when that
    happens, and running the command again fixes it.
- **Later (M7): device flow** (you approve a code in the browser) with our own "Hibernaut" GitHub
  OAuth App. The host gets its own token, which you can revoke separately.
  - The app is created once by hand, with Device Flow enabled.
  - Scopes: `repo`, `read:org`, `workflow`.
  - OAuth App tokens (`gho_…`) don't expire by default, so there's nothing to refresh.
- Only how the token reaches the vault differs. Everything after that is one code path, covered by
  the M4 end-to-end test (§8).
- **Narrower access:** `hib connect github --token github_pat_…` accepts a fine-grained token
  limited to chosen repos.
- **Later still:** a GitHub App, which limits access to selected repos and uses short-lived tokens
  that `hibd` refreshes (8 h tokens, 6-month refresh tokens).

**Inside machines:**

- `GH_TOKEN=<placeholder>` makes `gh` work. It takes precedence over any gh config, and gh won't
  write a login to disk while it's set. The Copilot CLI reads it too (§4.4).
- For git, the image runs `gh auth setup-git`, so `gh` is git's credential helper and answers with
  the `GH_TOKEN` placeholder. git sends it as Basic auth, which the proxy decodes and swaps.
- `git@github.com:` URLs are rewritten to `https://github.com/`.
- git `user.name` comes from your GitHub profile. `user.email` is your GitHub noreply address
  (`<id>+<login>@users.noreply.github.com`), so no email scope is needed and commits still link to
  you.
- Bound sites: `github.com`, `api.github.com`, `uploads.github.com`, plus Copilot's hosts (§4.4).

### 4.4 AI tools

v1 supports **Codex** on your ChatGPT plan and the **GitHub Copilot CLI**. Claude Code and OpenCode
come later.

| Tool | Connect once with | Inside the machine | Bound sites |
| --- | --- | --- | --- |
| Codex | `hib connect codex`: approve a code in the browser | A placeholder login file, `~/.codex/auth.json` | `chatgpt.com`, `auth.openai.com` |
| Copilot CLI | Nothing extra: it uses the GitHub connection | The same `GH_TOKEN` placeholder | Copilot's hosts (`*.githubcopilot.com`, `api.github.com`). Exact list from the S9 trace. |

**Codex on your ChatGPT plan** (S6, [codex-chatgpt-auth](research/codex-chatgpt-auth.md)):

- **The host gets its own login.** `hib connect codex` runs the official `codex login --device-auth`
  on the host, and you approve the code in the browser. Device login is in beta: turn it on in
  ChatGPT's security settings first.
  - We can't copy the laptop's login the way we copy gh's. ChatGPT refresh tokens are single-use,
    so whichever side refreshed first would log the other one out.
- **Machines get a placeholder login file.** Its JWTs carry your real claims (account, plan,
  email), a far-future expiry and a fake signature.
  - Codex reads them without checking signatures. It refreshes only when a token is within
    5 minutes of expiring, or after a 401. So on its own it never refreshes.
- **Only `hibd` refreshes the real token**, on the host, one refresh at a time.
  - The proxy swaps the placeholder access token on `chatgpt.com` for the current real one.
  - If Codex refreshes anyway (after a 401), the proxy answers that request itself with the same
    placeholders, once `hibd` has a fresh real token. It never goes to OpenAI.
- **Later: Business/Enterprise workspaces** could use a Codex access token instead
  (`hib connect codex --access-token`). It's a static `at-…` token, swapped like an API key through
  a `CODEX_ACCESS_TOKEN` placeholder. Plus and Pro accounts can't create one. Our account is Plus,
  so v1 leaves this out: we'd have nothing to test it against (D7).
- **Unattended:** the image's `~/.codex/config.toml` sets `approval_policy = "never"` and
  `sandbox_mode = "danger-full-access"`. The microVM is the sandbox, and Codex's own (bubblewrap)
  sandbox may not work inside it. Update checks and analytics are off.

**GitHub Copilot CLI** (S9, [copilot-auth](research/copilot-auth.md)):

- It reads its login from `COPILOT_GITHUB_TOKEN`, then `GH_TOKEN`, then `GITHUB_TOKEN`. GitHub lists
  gh's `gho_` tokens as supported, so the `GH_TOKEN` placeholder already logs it in.
  - It needs a Copilot subscription. Fine-grained tokens need the **Copilot Requests** permission.
    Classic `ghp_` tokens don't work.
- It trusts the system certificate store and `NODE_EXTRA_CA_CERTS`, so it trusts our CA. GitHub
  documents intercepting proxies with custom certificates as supported.
- **Open (S9):** Copilot may trade the GitHub token for a short-lived Copilot token (through
  `api.github.com/copilot_internal/…`) and send that to its model hosts. That token would reach
  the machine in a response. If so, the proxy captures it (§4.2 Token endpoints): the real one
  stays on the host, the machine gets a placeholder, and the proxy swaps it back on the model
  hosts. A traffic trace settles this before M5.
- **Unattended:** `COPILOT_AUTO_UPDATE=false`. Scripted runs use
  `copilot -p "…" --allow-all-tools --no-ask-user`. Prompt mode requires allowing all tools.

**Policy:**

- OpenAI documents using a ChatGPT login on headless machines and trusted CI for private
  automation, with one refresh stream per login. That's what v1 does.
- GitHub documents the Copilot CLI in headless setups, containers, CI, and behind intercepting
  proxies.
- Both accounts are for one person. A hosted Hibernaut for other people needs each user's own
  logins, API keys by default, and a legal review first.

### 4.5 Threat model (v1)

| If an agent or a prompt injection… | Then |
| --- | --- |
| reads env vars or files | it gets placeholders only |
| sends them to its own server | the placeholder isn't swapped, so it's useless |
| uses a credential against the real site (e.g. deletes a repo, or spends your AI quota) | **not prevented in v1.** It shows in the audit log. Mitigation: narrower tokens (fine-grained, GitHub App). |
| triggers a token refresh or exchange | the proxy handles it on the host (§4.2 Token endpoints). New real tokens never reach the machine. |
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
  - an agent process alive (`codex`, `copilot`), even when it's idle waiting on an LLM.
    `hibd` checks with `pgrep` through envd.
- `hibd` checks running machines every 60 s. While a machine is busy, it pushes the timeout back
  (`POST /sandboxes/{id}/timeout`). Nothing inside a machine calls the host.
- E2B won't push a machine's end past its start plus the tier's `max_length_hours`, which is 1
  for the seeded `base_v1` tier. M2 raises it.
- It polls only machines E2B reports as running. Any call to a paused machine, even a status
  check, would wake it.
- **Wake:**
  - Traffic to a paused machine through E2B's proxy resumes it, and SSH goes through that proxy.
  - `hib` commands resume explicitly with `POST /v2/sandboxes/{id}/connect`.
  - Resume takes ~0.4 s.
- **The host:**
  - Running machines live in RAM and die with the host.
  - Paused machines survive: they live in Postgres and in snapshot files on disk. M0 tested this
    live across a reboot and a deallocate (S4).
  - Restarting E2B loses running machines too: its routing state (Redis) isn't saved to disk.
  - E2B's pause returns before the snapshot is written. The orchestrator finishes writing it in
    the background, and waits for that only when it stops cleanly.
  - So before the host deallocates (idle, nightly or `make down`) or E2B is reinstalled,
    `quiesce` pauses every machine, stops E2B and flushes to disk. If a machine can't be paused,
    the host stays up. idle-deallocate also waits while any machine is running.
  - The nightly shutdown is an in-VM timer (20:00 UTC): quiesce, then deallocate. Azure's
    schedule, which can't run anything inside the VM first, is an hour later and only a backstop.
  - On any clean shutdown, including Azure's deallocate, the `hibd-pause-all` guard unit stops
    before E2B and pauses every machine.
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
| D2 | **Placeholder swap is the one credential mechanism** | One design covers GitHub, ChatGPT, Copilot and any API key. Stronger than syncing tokens into machines. |
| D3 | **Single user first** | Lets us bake keys and placeholders into images. Per-machine identity comes with multi-user. |
| D4 | **Glue around E2B, behind an `Engine` interface** | No fork of E2B to maintain. Swappable later (plain Firecracker, Cloud Hypervisor, …). |
| D5 | **Go for `hib` and `hibd`** | Static binaries, E2B's language, first-class TLS/HTTP/SSH libraries, and built-in test tooling (race detector, fuzzing, `httptest`) for the proxy, where tests matter most. |
| D6 | **The host never listens to machines** | `hibd` polls machines through envd. Machines have no path to host services except the proxied web. |
| D7 | **A milestone is done when its end-to-end test passes on the real host** | Firecracker, E2B and the real sites can't be faked away. Details in §8. |
| D8 | **GitHub: reuse the laptop's gh login first, our own OAuth App later** | Zero setup to start. Only how the token reaches the vault differs, so the same tests cover both. |
| D9 | **v1 AI tools: Codex on the ChatGPT plan, and the Copilot CLI** | The subscriptions you already have. Copilot needs no extra connection. Claude Code and OpenCode come later. |

## 7. Implementation plan

Each milestone ends with an automated end-to-end test on the real host (D7, §8). Tasks, tests and
status per milestone are tracked in [`docs/milestones/`](milestones/README.md).

| # | Milestone | End-to-end test |
| --- | --- | --- |
| M0 | **Foundations.** Go repo, test harness, CI. E2B under systemd, started on boot. E2B's storage moves out of the repo checkout (`~/infra/packages/orchestrator/tmp/`) to `/var/lib/e2b`. Machines are paused before any host shutdown. | A running machine survives a reboot and the nightly deallocate, with its files and processes (S4) |
| M1 | **Machines.** `dev` image; `hib init / new / ls / rm / exec`; SSH bridge and `*.hib` | new → ssh → exec → rm. An idle SSH session survives 11 minutes. |
| M2 | **Idle and wake** | An idle machine pauses; `ssh` wakes it with its tmux session still running. A busy one stays awake. |
| M3 | **Vault, egress proxy, leak scan** | A secret works on its bound site and arrives as a placeholder anywhere else. Machines can't reach host services (S8). The leak scan finds nothing. |
| M4 | **GitHub** via `--from-gh` | Fresh machine: clone a private repo → commit → push a branch → open a PR → close it and delete the branch |
| M5 | **AI tools:** Codex on the ChatGPT plan, GitHub Copilot CLI | `codex` and `copilot` answer a one-line prompt with no login. The leak scan finds nothing. |
| M6 | **Golden images and fork** | A machine started from a saved image has its repo, dependencies and running app in place; fork ×3 |
| M7 | **GitHub device flow** (own OAuth App) | Approve once in the browser, then the M4 test passes on the new token |

Repo layout:

```
cmd/hib/          laptop CLI
cmd/hibd/         host daemon (+ subcommands the CLI calls over SSH)
internal/         engine (E2B), machines, vault, egress, sshbridge
e2e/              end-to-end tests (build tag `e2e`), run against the real host by `make e2e`
images/dev/       Dockerfile + files baked into machines (CA cert, gitconfig, agent configs)
third_party/e2b/  E2B's API spec and envd proto at the pinned commit; clients are generated
azure/  vm/       host provisioning, systemd units, quiesce and the deallocate timers
```

State: SQLite (pure Go). Secrets are encrypted with AES-GCM using a root-only key file (later: Azure
Key Vault through the VM's managed identity).

## 8. Testing

**Rule:** every milestone's demo is an automated test. "It worked once by hand" doesn't count (D7).

| Layer | Runs on | Time | Covers |
| --- | --- | --- | --- |
| Unit + fuzz | laptop, CI | seconds | Placeholders, the swap engine, vault crypto, the `/etc/environment` block, idle policy, the CLI ↔ `hibd` JSON |
| Integration | laptop, CI | seconds | The real proxy against fake GitHub, ChatGPT and Copilot servers over TLS, driven by real `git`, `gh`, `curl` and Node clients. `hibd` against a fake engine. Device flow against a fake GitHub. |
| Host integration | CI (Linux, sudo), host | ~1 min | The iptables hook and `SO_ORIGINAL_DST`, with a network namespace standing in for a machine |
| End-to-end | the Azure host | minutes | Real E2B, Firecracker, GitHub, ChatGPT and Copilot. One scenario per milestone (§7). |

**Seams that keep the fast layers fast** (interfaces with test fakes):

- `Engine` (E2B) → an in-memory fake.
- Original-destination lookup (Linux `SO_ORIGINAL_DST`) → a fixed answer, so the proxy tests run
  on macOS too.
- DNS and dialing → github.com and friends resolve to local fake servers signed by a test CA.
- Clock → idle-policy tests don't sleep.

**The leak scan** tests goal 3 directly:

- After each end-to-end scenario, `hibd` pauses every test machine and searches its snapshot, both
  memory and disk, for every real secret value. Expected hits: zero. (If E2B stores snapshots
  compressed, the scan decompresses first. Checked in M3.)
- A canary value written into one machine on purpose must be found. That proves the scan works.
- Saved images are scanned the same way.

**Other invariants with their own tests:**

- A real value never reaches a site it isn't bound to (fuzzed).
- Traffic to other sites is byte-for-byte untouched and keeps the site's real certificate.
- Streams (git packs, SSE, WebSocket) flow without buffering.
- The vault file, logs and audit log never contain a real value.

**End-to-end details:**

- `make e2e` starts the host if it's asleep, deploys, installs E2B if needed, and runs
  `go test -tags e2e` from the laptop through an SSH tunnel.
- It uses the logins you connected with the normal `hib connect` commands. Browser approvals
  (Codex, and GitHub from M7) happen once by hand. Everything after that is automated.
- GitHub tests use a private fixture repo (`solev/hibernaut-e2e`) and always clean up their
  branches and PRs.
- AI tests use one-line prompts ("reply with OK"), which cost almost nothing.
- Slow scenarios (host reboot and deallocation, 11-minute idle SSH) run only with
  `make e2e-slow`, before a milestone closes.

**Commands:** `make test` (unit + integration), `make test-host` (Linux, sudo), `make e2e`,
`make e2e-slow`. CI runs `make lint test` and checks generated code on every push; `test-host`
joins CI in M3, with the first host test. CI is parked for now: GitHub Actions is off for the
repo (see the README).

## 9. Open questions and spikes

| # | Question | Status |
| --- | --- | --- |
| S1 | Can `hibd` intercept machine HTTPS without breaking E2B, and tell which machine sent it? | **Hook: answered in code.** E2B appends its redirects, so one rule inserted above them wins (§4.2). **Open:** the slot IP → machine mapping (E2B's API doesn't expose it), and a live test that the hook survives E2B adding and removing machines. Spike before M3. |
| S2 | Can E2B's proxy carry a long-lived SSH websocket, and wake a paused machine? | **Yes.** It's E2B's documented SSH pattern, and traffic on any port resumes a paused machine. Needs keepalives (~10-min idle cutoff). |
| S3 | Can E2B fork a machine or turn it into a template? | **Yes, natively** (§5 Images and fork). |
| S4 | Do paused machines survive a host reboot? | **Yes, tested live in M0** (`TestSurvivesHostRestart`). A running machine is paused on the way down, by the guard on a reboot or by `quiesce` before the nightly deallocate, and resumes afterwards with its files and processes. E2B's pause returns before the snapshot is written, so E2B must stop cleanly before power-off (§5 Idle and wake). |
| S5 | Does Claude Code work with a placeholder `CLAUDE_CODE_OAUTH_TOKEN` through the proxy? | **Deferred.** Claude Code comes after v1 ([milestones → Later](milestones/README.md#later-not-v1)). |
| S6 | Can Codex on a ChatGPT plan work with no real token in the machine? | **Yes, per Codex's source** ([codex-chatgpt-auth](research/codex-chatgpt-auth.md)). Codex reads the JWTs in its login file without checking signatures, and refreshes only near expiry or after a 401. So a placeholder login file plus host-only refresh works (§4.4). `CODEX_ACCESS_TOKEN` is simpler, but only Business/Enterprise workspaces can create one. Live test in M5. |
| S7 | Which env vars and config does each tool need to trust our CA and run unattended? | **Answered** (§4.2 Trust, §4.4). |
| S8 | Can machines reach host services or the Azure metadata endpoint? | **No, per E2B's code:** private, loopback and link-local addresses are dropped (§4.5). Live check in M3. |
| S9 | Does the Copilot CLI trade the GitHub token for its own short-lived token? Which hosts get which token? | **Open.** GitHub's docs confirm `GH_TOKEN` with gh's `gho_` token, and list `api.github.com/copilot_internal/*` and `*.githubcopilot.com` as required hosts ([copilot-auth](research/copilot-auth.md)). The exchange isn't documented, and the CLI ships as a compiled binary. Trace one `copilot -p` run through an intercepting proxy at the start of M5. |

Research: [e2b-capabilities](research/e2b-capabilities.md) (§10 covers egress, `caBundle` and SSH) ·
[agent-auth](research/agent-auth.md) · [codex-chatgpt-auth](research/codex-chatgpt-auth.md) ·
[copilot-auth](research/copilot-auth.md).
