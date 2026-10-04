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

- **Placeholder** = the same prefix and initial length as an opaque real token plus randomness
  (e.g. `gho_hib…`), so tools accept it as a real token. JWT placeholders carry the real claims,
  an expiry of 2100-01-01 and a fake signature (§4.4). Their encoded length may differ.
  A renewal of the same credential keeps its placeholder byte for byte, even when the real
  token's length changes. A new placeholder is made only when the token type or prefix changes,
  or when a JWT's exposed identity claims change. JWT identity comparison uses every decoded
  claim except the top-level renewal fields `exp`, `iat`, `nbf`, `jti`, `auth_time`, `nonce`,
  `at_hash`, `c_hash`, `s_hash` and `sid`. All other claims count as identity, including `sub`,
  `iss`, `aud`, `email`, `email_verified` and all fields under `https://api.openai.com/auth`
  (account, plan, user ID and FedRAMP status). JWT headers, signatures, claim order and JSON
  whitespace do not change identity.
- **Delivery:** `hibd` writes a managed block of placeholders (`GH_TOKEN=…`, …) into each
  machine's `/etc/environment`. Tools that read a login file instead (Codex, §4.4) get that file
  with placeholders in it. `hibd` writes both on create, on resume, and whenever secrets change.
  SSH sessions, `hib exec` and tmux all see them. (Not E2B's `envVars`: those reach only processes
  E2B starts, not SSH logins, and can't be changed after create.)
- **Swap:** the egress proxy replaces placeholders only in the `Authorization` request header,
  and only for that secret's bound sites. Basic auth is decoded before swapping and re-encoded.
  Other headers, URLs, and bodies keep the placeholder. Sent elsewhere, it is meaningless.
- **Health:** `hib status` checks each secret bound to `api.github.com` with `GET /user`, and shows
  whether it still works. There's no general way to test other keys, so they show as unchecked.

```
git clone https://github.com/solev/private-repo
  git ─▶ gh (credential helper) ─▶ "x-access-token : gho_hib7Qk…"   (placeholder)
  git ─▶ HTTPS to github.com ─▶ [host egress proxy]
                                  site bound to secret github? yes
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
  - Which machine sent it: the source address is the machine's slot IP. `hibd` derives the slot
    from that address, finds `/run/netns/ns-<slot>`, then reads the sandbox ID from the matching
    `/sys/fs/cgroup/e2b/sbx-<sandboxID>-<runid>/` cgroup. v1 needs this only for the audit log,
    because every machine has the same secrets.
  - Fallback: `HTTPS_PROXY` set in the machine. This is still safe, because a request that bypasses
    the proxy only carries a useless placeholder.
- **Most traffic is untouched.** The proxy reads the TLS SNI (the hostname) without decrypting:
  - **Not a bound site** (npm, PyPI, …): bytes pass straight through. Nothing is decrypted.
  - **Bound site:** the proxy terminates TLS with a certificate from the Hibernaut CA, which machines
    trust. It swaps the placeholder, then re-encrypts to the real site and verifies that site's real
    certificate.
- **Swap locations:** only the `Authorization` request header, for any scheme. For Basic, the
  proxy decodes the username and password, swaps placeholders, and re-encodes them. Other headers,
  the URL path and query, and all bodies stream through untouched. Bodies are never buffered.
  This prevents a request from storing the real value in a gist, git blob, issue, or comment.
  Non-TLS connections pass through when no SNI is found.
- **Defense in depth:** every intercepted response is scrubbed against all real values in the
  vault. Headers, trailers, and the whole body are scrubbed, including binary bodies, without
  buffering the whole response. Matches cover raw values,
  JSON escapes with and without HTML escaping, URL query escapes, and standard and URL-safe
  base64, padded and unpadded, at all three byte alignments. Each encoded match becomes the
  placeholder's encoded form.
  - Base64 matches need at least eight invariant characters. Shorter base64 forms are not scrubbed,
    so tiny patterns do not corrupt unrelated content. Mixed boundary characters can retain at
    most eight bits of an embedded value. End-of-data patterns include the final partial group
    and padding. A reflected Basic
    password can retain at most four leading bits, not its final bytes. These limits concern
    base64 only. Raw and escaped matches replace the whole value.
  - Replacements may change length after rotation. The proxy removes response `Content-Length`
    so HTTP/1.1 and HTTP/2 framing stay valid.
  - The proxy strips client `Accept-Encoding` and lets its upstream transport request and decode
    gzip. Any non-identity `Content-Encoding` left after transport decoding fails closed with 502.
    This includes gzip on Range requests and encodings such as br, zstd, and deflate.
  - If the audit log write fails, the request fails closed before forwarding.
- **WebSocket:** the upgrade request's `Authorization` is swapped and its response headers are
  scrubbed. After the upgrade, frames are neither swapped nor scrubbed.
- **Token endpoints:** some tools trade their login for fresh tokens: Codex refreshes, and Copilot
  may exchange (S9). The proxy handles these few endpoints itself, so a new real token never
  reaches a machine. It either answers the request with placeholders, or keeps the new real token
  on the host and passes the machine a placeholder for it (§4.4).
  Authorization-only swapping does not handle refresh-token bodies. The token endpoint handler
  in M5 handles Codex's refresh-token body on the host.
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
  - Machines made before the image is rebuilt do not trust the CA. Recreate them after
    `hib image build dev`.

### 4.3 GitHub

**Connecting (on the laptop):**

- **First (M4): `hib connect github --from-gh`** copies the laptop's `gh auth token` into the
  vault. Zero setup. The host checks it first with `GET /user`, which gives the account and, in
  the `X-OAuth-Scopes` header, the scopes. If GitHub refuses it, nothing is stored.
  - It's gh's own token (`gho_…`, no expiry) with gh's scopes: `repo`, `read:org`, `gist`.
  - Without `workflow`, agents can't push changes to `.github/workflows/`. `hib connect` warns
    and suggests `gh auth refresh -h github.com -s workflow`, then connecting again.
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
- **Narrower access:** `hib connect github --token` accepts a fine-grained token limited to chosen
  repos. It reads the token from a hidden prompt or stdin, never an argument, which would land in
  shell history.
- **Later still:** a GitHub App, which limits access to selected repos and uses short-lived tokens
  that `hibd` refreshes (8 h tokens, 6-month refresh tokens).

**Inside machines:**

- `GH_TOKEN=<placeholder>` makes `gh` work. It takes precedence over any gh config, and gh won't
  write a login to disk while it's set. The Copilot CLI reads it too (§4.4).
- For git, the image's `/etc/gitconfig` makes `gh` git's credential helper for GitHub, so it
  answers as `x-access-token` with the `GH_TOKEN` placeholder. git sends it as Basic auth, which
  the proxy decodes and swaps. It's the system file, rather than `gh auth setup-git`, so every
  user in the machine gets it.
- `git@github.com:` and `ssh://git@github.com/` URLs are rewritten to `https://github.com/`.
- git `user.name` comes from your GitHub profile. `user.email` is your GitHub noreply address
  (`<id>+<login>@users.noreply.github.com`), so no email scope is needed and commits still link to
  you. `hibd` writes both into each machine's `/etc/gitconfig` when it's made, and again whenever
  they change.
- Bound sites: `github.com`, `api.github.com` and `uploads.github.com`. Copilot's hosts join in M5
  (§4.4).

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
| makes a bound site store or echo a value | only Authorization is swapped. See the limits below. |
| gets credentials echoed in WebSocket frames | frames are not scrubbed after upgrade (§4.2) |
| uses a credential against the real site (e.g. deletes a repo, or spends your AI quota) | **not prevented in v1.** It shows in the audit log. Mitigation: narrower tokens (fine-grained, GitHub App). |
| triggers a token refresh or exchange | the proxy handles it on the host (§4.2 Token endpoints). New real tokens never reach the machine. |
| gets hold of a golden image | it only contains placeholders (multi-user will need a per-machine identity check) |
| connects to the host's own services (E2B API, databases, `hibd`) or Azure's metadata endpoint | blocked: E2B's firewall drops traffic to private, loopback and link-local addresses, including `169.254.169.254`. Azure's WireServer (`168.63.129.16`) is a public address E2B doesn't block, so the orchestrator runs with `IPAddressDeny` for it and an nft rule drops it on the sandbox veth. Still reachable: E2B's internal service address (`192.0.2.1`) and the host's public IP, where the NSG allows only key-based SSH. `hibd` listens on localhost, apart from the interception port, which accepts only machine interfaces. Verified live in M1 (`TestMachineNetwork`) and M3 (`TestNoHostAccess`, S8). |

**Bound sites:** stored content keeps the placeholder because only `Authorization` is swapped.
Responses from bound sites are scrubbed in raw, escaped, and base64 forms (§4.2). A bound site
that stores or shows request headers can still leak the value through another site, another
encoding, or WebSocket frames. Bind secrets only to the services that use them, not arbitrary
echo or storage services.

**Host compromise** exposes the vault. Later mitigation: keep the vault key in Azure Key Vault,
accessed through the VM's managed identity.

## 5. Machines

### Names and state

- `hibd` stores name ↔ E2B sandbox ID in SQLite. The name is also written into E2B metadata
  (`hib.name`) so the mapping can be rebuilt. E2B metadata stays in the control plane (the machine
  can't see it) and can be filtered with `GET /v2/sandboxes?metadata=…`.
- States: running → paused (idle) → deleted. The host itself can also be asleep.
- Every machine is created with `autoPause: true`, memory snapshots (never filesystem-only) and
  auto-resume off (`autoResume: {enabled: false}`), so traffic to a paused machine doesn't wake
  it. `hibd` resumes machines itself: only its users can wake one, and only once it has checked
  there's room (Room on the host).

### Idle and wake

E2B does the pausing. `hibd` decides what counts as busy, and does the waking.

- **Pause:** every machine has an idle time, its E2B timeout: 15 minutes, or what
  `hib new --idle` says (1m to 720h, and `0` means never pause). When it runs out, E2B pauses the
  machine instead of killing it.
- **Busy** means any of:
  - an SSH session through the bridge (`hib exec` runs over SSH too): the bridge renews the
    timeout while it's open, and closing it starts the idle clock.
  - at least 10% of a CPU used since the last look, read from the machine's cgroup on the host
    (E2B's metrics API returns every sample since the machine started, so it only grows).
  - a coding agent running (`codex`, `copilot` or `claude`, or the programs `HIBD_AGENTS`
    names), even when it's idle waiting on an LLM. `hibd` checks with `pgrep` through envd.
- `hibd daemon`, a systemd service, looks at running machines every 15 s, all at once, so one
  that doesn't answer can't hold up the rest. While a machine is busy, it pushes the timeout
  back (`POST /sandboxes/{id}/timeout`), never shortening it. Nothing inside a machine calls the
  host.
- E2B won't push a machine's end past its start plus the tier's `max_length_hours`. The seeded
  `base_v1` tier has 1. `install-e2b.sh` raises it to 720 (30 days), and a resume starts the
  clock again.
- It looks only at machines E2B reports as running: a paused machine can't be busy, and `hibd`
  never wakes one to check.
- **Wake:**
  - SSH to a paused machine (`ssh NAME.hib`, `hib exec`) wakes it: the bridge in `hibd` resumes
    it (`POST /v2/sandboxes/{id}/connect`) before connecting through E2B's proxy.
  - Other traffic doesn't: E2B's proxy answers `502 The sandbox was not found`.
  - Waking a machine over SSH takes ~0.6 s.
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

### Room on the host

One host runs every machine, so `hibd` checks there's room before it starts or resumes one.
Numbers are for today's host (4 vCPU, 16 GB, 123 GB disk) and the `dev` image (2 vCPU, 2 GiB).

- **Memory is the hard limit.** Firecracker takes a running machine's RAM from the host's
  huge-page pool (3200 × 2 MiB = 6.25 GiB, set in `install-e2b.sh`): about three `dev` machines
  at once. Paused machines need none. On its own, E2B refuses a start or resume that doesn't fit
  at once and safely, but says only `500 Failed to place sandbox … please retry`.
- **CPU is shared, not reserved.** Each machine's 2 vCPUs share the host's 4. Busy machines slow
  each other down, but nothing fails.
- **Disk:** a machine's own disk is capped at 10 GiB (the tier's `disk_mb`), but every pause
  writes a new snapshot layer (120–370 MB seen). E2B never deletes these, not even a removed
  machine's: it only marks the row deleted. A pause that can't be written still stops the
  machine, losing what was running in it.
- **Before a start or resume, `hibd` checks:**
  - free huge pages (`HugePages_Free` − `HugePages_Rsvd`) cover the machine's RAM.
  - free disk covers pausing every running machine and this one (their RAM, plus a margin for
    what they wrote to disk).
- **No room:** a clear error that names the running machines and says what to do:
  `hib pause NAME` frees memory, `hib rm` frees disk. Two starts can race the check, so when E2B
  refuses anyway, `hibd` checks again and gives the same message.
- **Snapshot cleanup:** `hibd` deletes the build directories in
  `/var/lib/e2b/local-template-storage` that nothing can resume or start from any more. It
  reads E2B's database (through `docker exec psql`), the machines and each directory's headers,
  and keeps:
  - every machine's current snapshot, and every image's current build under each tag: the one
    E2B put there last, as `env_build_assignments` records (a build can be in more than one
    image or machine), and the one finished last.
  - the older builds their headers point into (a snapshot is a chain of diffs).
  - the image builds a running machine may have started from.
  - the builds E2B's layer cache points to.
  - anything changed in the last hour, unless only removed images and machines use it.

  It deletes nothing while E2B is writing a build (for up to 6 hours: E2B never finishes a
  build it stopped writing), or if it can't read a header it needs. It runs when `hibd` starts,
  when a machine is removed, and hourly, which catches a machine removed between two looks. It
  runs beside the busy watcher, never holding it up. `hibd cleanup -n` says what it would delete
  and why it keeps the rest.
- E2B bounds its own cache in `/var/lib/e2b/build`: it deletes files after 25 hours, evicts at
  85% disk, and empties it when the orchestrator starts.
- `hib ls` shows the room left.

### Snapshot compaction

Planned for M9.

- Every pause writes a diff against the machine's last snapshot, and E2B never merges them. The
  latest snapshot needs many of the diffs before it, so cleanup can't free those: after 20
  pauses of an idle `dev` machine, it needed 13 of them (3.6 GiB), one more with every pause
  (M2's `TestPauseCycles`). Resume time didn't grow (0.7–1.5 s).
- First, E2B's own options, which its local setup leaves off (LaunchDarkly flags at their
  defaults): `memfile-diff-dedup` (4 KiB dedup of each diff, and a budget that copies parent
  pages into the new one, so old diffs stop being needed), `guest-pause-reclaim` (fstrim,
  drop_caches and compact_memory before a pause) and `collapse-envd-heap`.
- If the chain still grows, `hibd` merges it into one build, and cleanup deletes the old diffs.

### Sizes

Planned for M8.

- A machine's size is picked at `hib new` (`--size small|medium|large`, default `medium`) and
  kept for life. E2B fixes CPU and RAM per image build, because a machine's memory snapshot is
  that size, so each size of an image is its own E2B template (`hib-dev`, `hib-dev-small`, …).

  | Size | vCPU | RAM | At once on today's host |
  | --- | --- | --- | --- |
  | `small` | 1 | 1 GiB | 6 |
  | `medium` (default) | 2 | 2 GiB | 3 |
  | `large` | 4 | 4 GiB | 1, plus a small or medium one |

- Sizes can mix: they share the huge-page pool (Room on the host). Disk is 10 GiB for every size.
- Saved images and forks (Images and fork) keep the size of the machine they came from.

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
- The machine user is `user`, with passwordless sudo. Your public key is written into the machine
  at create time (single user).
- **Host keys:** each machine makes its own SSH host key at create time; `hibd` pins it and `hib`
  verifies against that pin in its own `known_hosts` (`StrictHostKeyChecking yes`). Later, SSH host
  certificates.

### Images and fork

All three are native E2B operations. Hibernaut only wraps them.

| Command | E2B call | Result |
| --- | --- | --- |
| `hib image build dev` | template build from the `images/dev/` files (`images/images.go`) | The base image all machines start from |
| `hib image save app rocket` | `POST /sandboxes/{id}/snapshots` `{name: "rocket"}` | An image with memory, so processes keep running; it outlives `app` |
| `hib fork app --count 3` | `POST /sandboxes/{id}/fork` | `app` pauses for a moment and keeps running; 3 new machines in the same state |

Copies carry everything in the machine. That's fine, because a machine holds only placeholders and
your public key. A fork carries the origin's SSH host key until it's re-keyed (see SSH path).

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
| M2 | **Idle and wake**, and room on the host (a clear error when it's full, snapshot cleanup) | An idle machine pauses, and `ssh` wakes it with its tmux session still running. A busy one stays awake. A full host refuses one more machine with a clear message. |
| M3 | **Vault, egress proxy, leak scan** | A secret works on its bound site and arrives as a placeholder anywhere else. Machines can't reach host services (S8). The leak scan finds nothing. |
| M4 | **GitHub** via `--from-gh` | Fresh machine: clone a private repo → commit → push a branch → open a PR → close it and delete the branch |
| M5 | **AI tools:** Codex on the ChatGPT plan, GitHub Copilot CLI | `codex` and `copilot` answer a one-line prompt with no login. The leak scan finds nothing. |
| M6 | **Golden images and fork** | A machine started from a saved image has its repo, dependencies and running app in place; fork ×3 |
| M7 | **GitHub device flow** (own OAuth App) | Approve once in the browser, then the M4 test passes on the new token |
| M8 | **Machine sizes** (`hib new --size small`) | A small machine has 1 vCPU and 1 GiB. One made without `--size` still has 2 vCPUs and 2 GiB |
| M9 | **Snapshot compaction** | An idle machine paused 20 times needs no more disk than a bound, and resumes as fast as after one pause |
| M10 | **CLI friendliness:** help for every command, suggestions for typos, `hib rm` asks first, completions, `--json` | No new one: help and mistakes are unit-tested, and the existing tests pass with `hib rm --yes` |

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
  memory and disk, for every real secret value. Expected hits: zero. E2B writes uncompressed
  version 3 headers on this host. The scan refuses compressed version 4 and later headers.
- The scan selects the newest build by creation time, regardless of status. It fixes that build
  ID before polling once a second for up to 15 minutes. A failed build fails immediately.
  Pending builds and incomplete uploads never cause a scan of an older ready build.
- E2B can mark a pause successful before its local upload finishes. A complete build has both
  readable headers, a non-empty `snapfile`, and valid `metadata.json` for that build. The headers
  must identify that build and cover their logical sizes. Every mapped data file must reach its
  furthest storage offset plus length. The same checks apply to all referenced builds. Missing or
  short files fail the scan. A timeout returns an error, not a clean verdict.
- An empty diff needs no new data file if its mappings use only ancestors or zero blocks.
  Missing headers are not empty diffs. Filesystem-only snapshots cannot prove memory is clean
  and fail the scan.
- A canary value written into one machine on purpose must be found in both memory and disk.
  Canary runs hold an exclusive host lock. Before creating a machine, they remove machines and
  vault secrets left under the reserved name `leakscan-canary-` followed by 12 lowercase hex
  digits. That makes a rerun after a crash converge without removing normal machines or secrets.
- Saved images use the same newest-build selection and completion checks.

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
  branches and PRs. A run first removes `e2e-` branches and PRs that killed runs left.
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
| S1 | Can `hibd` intercept machine HTTPS without breaking E2B, and tell which machine sent it? | **Answered live and in code.** One iptables-nft rule inserted above E2B's appended redirects stays first while machines are created, removed, paused and resumed. A redirected connection exposes the slot source IP and local veth IP, and `internal/slots` maps that slot to the sandbox cgroup for audit. |
| S2 | Can E2B's proxy carry a long-lived SSH websocket, and wake a paused machine? | **Yes.** It's E2B's documented SSH pattern, and traffic on any port resumes a paused machine. Needs keepalives (~10-min idle cutoff). |
| S3 | Can E2B fork a machine or turn it into a template? | **Yes, natively** (§5 Images and fork). |
| S4 | Do paused machines survive a host reboot? | **Yes, tested live in M0** (`TestSurvivesHostRestart`). A running machine is paused on the way down, by the guard on a reboot or by `quiesce` before the nightly deallocate, and resumes afterwards with its files and processes. E2B's pause returns before the snapshot is written, so E2B must stop cleanly before power-off (§5 Idle and wake). |
| S5 | Does Claude Code work with a placeholder `CLAUDE_CODE_OAUTH_TOKEN` through the proxy? | **Deferred.** Claude Code comes after v1 ([milestones → Later](milestones/README.md#later-not-v1)). |
| S6 | Can Codex on a ChatGPT plan work with no real token in the machine? | **Yes, per Codex's source** ([codex-chatgpt-auth](research/codex-chatgpt-auth.md)). Codex reads the JWTs in its login file without checking signatures, and refreshes only near expiry or after a 401. So a placeholder login file plus host-only refresh works (§4.4). `CODEX_ACCESS_TOKEN` is simpler, but only Business/Enterprise workspaces can create one. Live test in M5. |
| S7 | Which env vars and config does each tool need to trust our CA and run unattended? | **Answered** (§4.2 Trust, §4.4). |
| S8 | Can machines reach host services or the Azure metadata endpoint? | **No, tested live in M3** (`TestNoHostAccess`). A machine probed every port the host listens on, at every host address, the gateway, `169.254.169.254` and `168.63.129.16`. Only SSH on the host's public IP and E2B's service endpoint at `192.0.2.1` answered (§4.5). |
| S9 | Does the Copilot CLI trade the GitHub token for its own short-lived token? Which hosts get which token? | **Open.** GitHub's docs confirm `GH_TOKEN` with gh's `gho_` token, and list `api.github.com/copilot_internal/*` and `*.githubcopilot.com` as required hosts ([copilot-auth](research/copilot-auth.md)). The exchange isn't documented, and the CLI ships as a compiled binary. Trace one `copilot -p` run through an intercepting proxy at the start of M5. |

Research: [e2b-capabilities](research/e2b-capabilities.md) (§10 covers egress, `caBundle` and SSH) ·
[agent-auth](research/agent-auth.md) · [codex-chatgpt-auth](research/codex-chatgpt-auth.md) ·
[copilot-auth](research/copilot-auth.md).
