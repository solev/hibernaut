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
hib connect github             # approve a code in the browser, for machines and the Copilot CLI
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
hib new task-1 --image rocket    # ready in ~1 s with all of that in place
hib fork app --count 2           # app-1 and app-2: copies of app as it is now. app keeps running
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
| `hibd` | host (systemd) | Machine names and state, vault, egress proxy, SSH bridge, idle policy, and from M12 links between machines. |
| E2B | host (systemd) | The engine: boots, pauses and resumes Firecracker VMs, and builds images. |
| `dev` image | E2B template | Linux, sshd + websocat (the SSH endpoint), git + gh (gh is git's credential helper), Node/Python, Codex, the Copilot CLI, and our CA certificate. |

**Control channel = SSH.** Each `hib <cmd>` runs `ssh host hibd <cmd> --json`. SSH is already open
and key-based, so v1 needs no HTTPS API, domain, TLS certificates or API keys. Commands that need
root on the host run `sudo -n hibd` instead: `connect`, `disconnect`, `secret add`, `secret rm`,
`status`, `fork`, `image save`, and from M12 `pause` and `rm`. Those two need it because when
`hibd proxy` doesn't confirm that it closed a machine's links, they cut them in the kernel
themselves.

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
  with placeholders in it. Their secrets have no env var. `hibd` writes both on create, after
  every resume (at the daemon's next look, within 15 seconds), and whenever secrets change.
  SSH sessions, `hib exec` and tmux all see them. (Not E2B's `envVars`: those reach only processes
  E2B starts, not SSH logins, and can't be changed after create.)
- **Swap:** the egress proxy replaces placeholders only in the `Authorization` request header,
  and only for that secret's bound sites. Basic auth is decoded before swapping and re-encoded.
  Other headers, URLs, and bodies keep the placeholder. Sent elsewhere, it is meaningless.
  A **scrub-only** secret is never swapped. Its sites are still intercepted, and responses are
  still scrubbed of it. Codex's refresh and ID tokens are scrub-only (§4.4).
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
    most eight bits of an embedded value, four at each end. Patterns for a value's ending include
    the final partial group and padding. They swap the value's last character exactly at the end
    of data and wherever the bits that character holds from the next byte are zero, as they are
    for a newline. A reflected Basic password can retain at most four leading bits, not its final
    bytes. These limits concern base64 only. Raw and escaped matches replace the whole value.
  - Replacements may change length after rotation. The proxy removes response `Content-Length`
    so HTTP/1.1 and HTTP/2 framing stay valid.
  - The proxy strips client `Accept-Encoding` and lets its upstream transport request and decode
    gzip. Any non-identity `Content-Encoding` left after transport decoding fails closed with 502.
    This includes gzip on Range requests and encodings such as br, zstd, and deflate.
  - If the audit log write fails, the request fails closed before forwarding.
- **WebSocket:** the upgrade request's `Authorization` is swapped and its response headers are
  scrubbed. After the upgrade, frames are neither swapped nor scrubbed. Codex's model stream is a
  WebSocket. The S6 trace found no token in its frames in either direction, but that holds only
  for the pinned version, so the trace is repeated whenever the pin moves (§4.4).
- **Token endpoints:** Codex refreshes its login. (Copilot has no exchange, S9.) The proxy
  answers `POST auth.openai.com/oauth/token` itself, so a new real token never reaches a machine
  (§4.4). It's the only request whose body the proxy reads: at most 64 KiB, within 10 seconds.
  The answer is scrubbed against the vault as it is after the refresh. Everything else on
  `auth.openai.com` passes through with nothing swapped.
- **Audit log:** every swap is logged as time, machine, site, method, path and secret name, and
  so is every refresh request the proxy answers. View it with `hib log`.
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

- **Default (M7): `hib connect github`** runs GitHub's device flow on the host, with our own
  "Hibernaut" GitHub OAuth App. `hibd` asks GitHub for a code, `hib` shows it and opens
  `github.com/login/device`, and you approve it in the browser. The token goes straight into the
  vault and never touches the laptop, and you can revoke it on its own.
  - The app is created once by hand, with Device Flow enabled and token expiration opted out. Its
    client ID is built into `hibd`. It isn't secret, and the device flow needs no client secret.
  - Scopes: `repo`, `read:org`, `workflow`.
  - Since GitHub's change of 2026-08-14, new OAuth Apps issue tokens that expire after 8 hours,
    with a refresh token. The Hibernaut app opts out under **Optional features**, so its tokens
    (`gho_…`) don't expire and there's nothing to refresh. `hibd` refuses a token that expires.
  - A code lasts 15 minutes. Ctrl-C in `hib` stops `hibd` waiting, but a token GitHub granted
    just before is still stored. `hib status` shows whether it was.
  - An org that restricts OAuth Apps hides its repos from the token until an owner approves the
    app.
- **`hib connect github --from-gh`** (M4) copies the laptop's `gh auth token` into the vault.
  Zero setup.
  - It's gh's own token (`gho_…`, no expiry) with gh's scopes: `repo`, `read:org`, `gist`.
  - Without `workflow`, agents can't push changes to `.github/workflows/`. `hib connect` warns
    and suggests `gh auth refresh -h github.com -s workflow`, then connecting again.
  - The laptop and the host share this token. GitHub keeps at most ten tokens per user, app and
    scope set, so logging gh in on many devices can revoke it. `hib status` shows when that
    happens, and running the command again fixes it.
- The host checks every token with `GET /user` before storing it. That gives the account and, in
  the `X-OAuth-Scopes` header, the scopes. If GitHub refuses it, nothing is stored. One connect
  runs at a time.
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
- Bound sites: `github.com`, `api.github.com`, `uploads.github.com` and `*.githubcopilot.com`,
  for the Copilot CLI (§4.4). A GitHub connection made before M5 gets the Copilot hosts when
  `hibd daemon` starts.

### 4.4 AI tools

v1 supports **Codex** on your ChatGPT plan and the **GitHub Copilot CLI**. Claude Code and OpenCode
come later.

| Tool | Connect once with | Inside the machine | Bound sites |
| --- | --- | --- | --- |
| Codex | `hib connect codex`: approve a code in the browser | A placeholder login file, `~/.codex/auth.json` | `chatgpt.com`. `auth.openai.com` is intercepted for refreshes only. |
| Copilot CLI | Nothing extra: it uses the GitHub connection | The same `GH_TOKEN` placeholder | `api.github.com` and `*.githubcopilot.com` (S9) |

**Codex on your ChatGPT plan** (S6, [codex-chatgpt-auth](research/codex-chatgpt-auth.md)):

- **The host gets its own login.** `hib connect codex` runs the official `codex login --device-auth`
  on the host, and you approve the code in the browser. Device login is in beta: turn it on in
  ChatGPT's security settings first.
  - The host has the same pinned Codex as the image, used only for this login. It runs with a
    throwaway `CODEX_HOME` and a clean environment. The tokens go into the vault, then the
    directory is deleted.
  - We can't copy the laptop's login the way we copy gh's. ChatGPT refresh tokens are single-use,
    so whichever side refreshed first would log the other one out.
  - Connecting again replaces the login, even with another account.
- **Three managed secrets:** `codex-access` holds the access token and is swapped on
  `chatgpt.com`. `codex-refresh` and `codex-id` are scrub-only and bound to `auth.openai.com`.
  None has an env var. `hib secret` can't set or remove names that start with `codex-`.
- **Machines get a placeholder login file.** Its JWTs carry your real claims (account, plan,
  email), a far-future expiry and a fake signature. Its refresh token is a placeholder too. The
  account ID is real.
  - Codex reads them without checking signatures. It refreshes only when a token is within
    5 minutes of expiring, or after a 401. So on its own it never refreshes.
  - `hibd` owns the file while Codex is connected. It writes it on create, after every resume, and
    when the placeholders change. A refresh keeps the access and refresh placeholders, but the ID
    token's claims include refresh times, so its placeholder changes. After `codex logout` in a
    machine, pause and resume the machine, and the file is back within 15 seconds.
- **Only `hibd` refreshes the real token**, on the host.
  - The daemon refreshes once half the access token's lifetime has passed (it lasts 10 days). It
    backs off while OpenAI can't be reached.
  - One refresh at a time, across the daemon, the proxy and `hib connect codex`. The new tokens
    are in the vault before anything uses them. Old values stay in the leak scan until they expire.
  - A refresh whose outcome is unknown is never retried by itself: the host crashed during it, or
    the request went out and the answer was a 5xx or never came. OpenAI may have used the refresh
    token, and using it again could end the login. The current access token keeps working until it
    expires, and `hib status` says to run `hib connect codex` before then.
  - `hib status` shows the account, the plan, when the token expires, and whether the login is
    dead (OpenAI refused the refresh token) or stuck (an unknown outcome). Either way the fix is
    `hib connect codex`.
- **The proxy answers Codex's refresh request itself** (§4.2 Token endpoints). It never goes to
  OpenAI.
  - It carries the machine's placeholder refresh token: the proxy logs it, then answers 200 with
    the current placeholders. First it refreshes for real, but only if a site answered 401 to the
    current real access token, or the token expires within 5 minutes. That's at most once every
    10 minutes. Past that it answers 503, and Codex tries again later.
  - A refresh is due, but the login is dead or stuck: 401 with an error that names
    `hib connect codex`. Codex stops and shows it.
  - Any other grant, such as a login inside the machine or another refresh token: 403. While Codex
    is connected, machines can't log in to ChatGPT themselves.
- **Later: Business/Enterprise workspaces** could use a Codex access token instead
  (`hib connect codex --access-token`). It's a static `at-…` token, swapped like an API key through
  a `CODEX_ACCESS_TOKEN` placeholder. Plus and Pro accounts can't create one. Our account is Plus,
  so v1 leaves this out: we'd have nothing to test it against (D7).
- **Unattended:** the image's `~/.codex/config.toml` sets `approval_policy = "never"` and
  `sandbox_mode = "danger-full-access"`. The microVM is the sandbox, and Codex's own (bubblewrap)
  sandbox may not work inside it. Update checks and analytics are off, and
  `cli_auth_credentials_store = "file"` keeps the login in the file.

**GitHub Copilot CLI** (S9, [copilot-auth](research/copilot-auth.md)):

- It reads its login from `COPILOT_GITHUB_TOKEN`, then `GH_TOKEN`, then `GITHUB_TOKEN`. GitHub lists
  gh's `gho_` tokens as supported, so the `GH_TOKEN` placeholder already logs it in.
  - It needs a Copilot subscription. Fine-grained tokens need the **Copilot Requests** permission.
    Classic `ghp_` tokens don't work.
- It trusts the system certificate store and `NODE_EXTRA_CA_CERTS`, so it trusts our CA. GitHub
  documents intercepting proxies with custom certificates as supported.
- **No exchange** (S9, live trace): Copilot sends the `GH_TOKEN` value itself to `api.github.com`
  (`/copilot_internal/user`) and to its plan's API host, such as
  `api.enterprise.githubcopilot.com`. The proxy swaps it there like any GitHub request, so no new
  token reaches the machine. Copilot's config directory holds no token.
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
| triggers a token refresh | the proxy answers it on the host (§4.2 Token endpoints). New real tokens never reach the machine, and a machine can make the host refresh at most once every 10 minutes. |
| sends another refresh token or a login through the proxy | refused with 403 while Codex is connected (§4.4) |
| gets hold of a golden image | it only contains placeholders (multi-user will need a per-machine identity check) |
| connects to the host's own services (E2B API, databases, `hibd`) or Azure's metadata endpoint | blocked: E2B's firewall drops traffic to private, loopback and link-local addresses, including `169.254.169.254`. Azure's WireServer (`168.63.129.16`) is a public address E2B doesn't block, so the orchestrator runs with `IPAddressDeny` for it and an nft rule drops it on the sandbox veth. Still reachable: E2B's internal service address (`192.0.2.1`) and the host's public IP, where the NSG allows only key-based SSH. `hibd` listens on localhost, apart from the interception and link ports, which accept only machine interfaces. Verified live in M1 (`TestMachineNetwork`) and M3 (`TestNoHostAccess`, S8). |
| reaches another of your machines (M12) | only on a port you published by hand, and then on every one of your machines: publishing trusts them all. Nothing is reachable until `hib port add` says so. `hibd` takes the caller's identity from the veth the connection arrived on, not from the address it claims, and the target from its own map, so a machine can't steer a link at a host service. Secrets are not swapped on a link. A link can wake a paused target, bounded by a room reserve and a wake rate limit. |

**Bound sites:** stored content keeps the placeholder because only `Authorization` is swapped.
Responses from bound sites are scrubbed in raw, escaped, and base64 forms (§4.2). A bound site
that stores or shows request headers can still leak the value through another site, another
encoding, or WebSocket frames. Bind secrets only to the services that use them, not arbitrary
echo or storage services.

**Host compromise** exposes the vault. Later mitigation: keep the vault key in Azure Key Vault,
accessed through the VM's managed identity.

## 5. Machines

### Names and state

- The name lives in E2B metadata (`hib.name`), next to `hib.image`, `hib.size`, `hib.idle` and
  `hib.forkof`. E2B metadata stays in the control plane (the machine can't see it) and can be
  filtered with `GET /v2/sandboxes?metadata=…`, so `hibd` lists machines by asking E2B and needs
  no database of its own. Labels are set at create and can't be changed afterwards: E2B has no
  metadata `PATCH` or `PUT`.
- What `hibd` keeps itself is files in `HIBD_STATE_DIR` (`/var/lib/hibernaut`): the vault, the
  logins, each machine's pinned SSH host key, and from M12 the published ports. They're written
  atomically and guarded by `flock`.
- What must not outlive a boot is in `/run/hibernaut`, which root owns: the quiescing marker,
  which `quiesce` makes and from M12 `hibd pause-all` too, and from M12 the control socket
  (`links.sock`) through which `hibd pause` and `hibd rm` tell `hibd proxy` to close a machine's
  links, and the next kernel guard token. From M12 systemd-tmpfiles makes the directory at boot,
  because `hibd-proxy.service` can't start without it.
- States: running → paused (idle) → deleted. The host itself can also be asleep.
- Every machine is created with `autoPause: true`, memory snapshots (never filesystem-only) and
  auto-resume off (`autoResume: {enabled: false}`), so traffic to a paused machine doesn't wake
  it. `hibd` resumes machines itself, and only once it has checked there's room (Room on the
  host). Its users wake a machine by logging in; from M12 another of their machines can wake it
  too, by connecting to a port they published (Links between machines).
- `hibd` sets each new machine up by running commands in it through E2B's envd: `machine-init`
  (the host key and your public key), then the env block, git's identity and Codex's login file.
  E2B's proxy sometimes ends a command's stream early, though the command may still run. So
  every setup step is safe to run twice, even at once, and `hibd` runs setup again, up to 3 tries
  within 2 minutes.

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
- From M12, bytes on a link renew the target's timeout too, but they are deliberately **not** a
  busy signal: a link both wakes and renews its target only while the calling machine is busy or
  has an SSH session of its own, so machines can't hold each other up (Links between machines).
  From M12 `hibd` also watches machines with `idle=0` for the same reason — they never pause, so
  there is nothing to renew, but whether they are working decides whether their links count.
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
  - From M12, a connection from another of your machines to a port you published wakes it the
    same way, unless a person paused it by hand (Links between machines).
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
- **Saves and forks need more at their peak.** A save holds the machine's memory twice for a
  moment, and each fork copy needs its own (Images and fork). `hibd` checks room for the whole
  operation first, and again before each copy.
- **No room:** a clear error that names the running machines and says what to do:
  `hib pause NAME` frees memory, `hib rm` frees disk. Two starts can race the check, so when E2B
  refuses anyway, `hibd` checks again and gives the same message.
- **A reserve for people.** From M12 a machine can wake another by connecting to a published
  port, so the check keeps one `dev` machine's memory free that only a person's command may use,
  however big the machine a link wakes. A link that doesn't fit is refused at once and says so,
  rather than taking the last of the room or holding the host awake.
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

- Every pause writes a diff against the machine's last snapshot, and E2B never merges them. The
  latest snapshot needs many of the diffs before it, so cleanup can't delete those: after 20
  pauses of an idle `dev` machine, it needed 13 of them (3.6 GiB), one more with every pause
  (M2's `TestPauseCycles`). Resume time didn't grow (0.7–1.5 s).
- So cleanup also trims old builds (M9). A root is a build something may start from or resume:
  what M2 keeps, plus any build a machine may be running from or may yet resume, which keeps a
  replaced snapshot for an hour. In every other build, cleanup punches holes in `memfile` and
  `rootfs.ext4` (`fallocate(PUNCH_HOLE|KEEP_SIZE)`) wherever no root's headers map. A hole reads
  as zeros. Files keep their sizes, and headers, snapfiles and metadata stay, so hib's checks of
  a chain still pass.
- It fails closed. A root, or a build a root reads, that's missing or has a header the strict
  parser (`internal/snapheader`) rejects stops all trimming in that run, and so does storage
  that isn't ext4. Deletion still goes ahead, as in M2. An unfinished save stops both, until
  `hibd` can prove it over (Images and fork).
- After each cleanup, a machine's old builds hold only its live memory and disk data, plus what
  its pauses of the last hour wrote. In `TestTrim`, 10 pauses took 3.9 GiB, and 553 MiB after
  the trim. Not bounded: one build directory a pause (30 to 130 KB of headers and snapfile),
  saved images, and how long cleanup waits while E2B is busy.
- `HIBD_TRIM` in `/etc/hibernaut/hibd.env` picks whose old builds are trimmed. `e2e`, the
  default until trimming has run on real machines, takes only the end-to-end tests' machines.
  `all` takes every machine's, and `off` none. Roots never depend on it.
- `hibd cleanup -n` says what it would trim, or why it trims nothing. The daemon trims for up to
  two minutes at a time, and picks up where it stopped a minute later.
- Not tried: E2B's own options, which its local setup leaves off (`memfile-diff-dedup`,
  `guest-pause-reclaim` and `collapse-envd-heap`). Merging a chain into one build is for later.

### Sizes

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
- A sleeping laptop answers no keepalives. The machine's sshd and the host's drop it after 15
  missed ones a minute apart (`ClientAliveCountMax 15`), about when the host's TCP gives up on
  unacknowledged data (`tcp_retries2`). So a session outlasts a shorter sleep.
- E2B's proxy isn't exposed outside the host. If it ever is, create machines with E2B's private
  ingress, and have `hibd` send each machine's `trafficAccessToken`.
- `hib exec app -- cmd` is `ssh app.hib -- cmd`. Interactive sessions and exec then get the same
  user, environment and wake-up path.
- The machine user is `user`, with passwordless sudo. Your public key is written into the machine
  at create time (single user).
- **Host keys:** each machine makes its own SSH host key at create time; `hibd` pins it and `hib`
  verifies against that pin in its own `known_hosts` (`StrictHostKeyChecking yes`). A machine
  made from a saved image or a fork gets a new one (Images and fork). Later, SSH host
  certificates.

### Links between machines

Built in [M12](milestones/m12-machine-links.md). By default machines can't reach each other at
all. You publish one port at a time, from the laptop, and after that your other machines reach it
by name.

```
hib port add api 8080     # api:8080 is published to your machines as api.hib:8080
hib port ls
hib port rm api 8080
```

- **Off until you publish.** `hib port add` is the only way in, it runs on the laptop through
  `hibd` over SSH, and machines have no path to `hibd`'s API, so a machine can't publish
  anything, not even its own port.
- **Everything or nothing.** A published port is reachable from every one of your machines.
  Publishing trusts them all. Narrowing it to named sources (`--from`) comes later.
- **Names.** A machine gets a virtual address from `198.18.0.0/15` the first time one of its
  ports is published, and keeps it for life; addresses are never reused. `hibd` writes a managed
  `/etc/hosts` block in every machine through the same delivery that writes the env block, so
  `api.hib` resolves with no DNS server and no image rebuild.
- **The path.** An iptables rule redirects a machine's TCP to `198.18.0.0/15` into the link listener
  `hibd proxy` serves on port 5444, which takes the caller's identity from the veth the connection
  arrived on, checks the port is published, wakes the target if it's paused, dials the target's
  current slot and copies bytes. The client never learns the host is in the path.
- **On by default.** `HIBD_LINK_PORT=0` in `/etc/hibernaut/hibd.env` turns links off for the whole
  host: nothing is redirected, `hib port` says links are off and `hib ls` shows no ports. With
  links on, `hibd proxy` serves them only once its checks pass: the kernel guard is installed, its
  control socket answers, the host's firewall drops what machines send to the link range and E2B's
  orchestrator may not reach it. Until they do, egress works and links stay off, and it checks
  again every minute.
- **Never publishable:** 8081 (the SSH bridge) and 49983 (E2B's agent). TCP only.
- **Secrets are not swapped on a link.** It doesn't go through the egress proxy, so there is no
  TLS interception and no placeholder swap: a machine that sends a placeholder over a link sends
  the placeholder.
- **Localhost counts.** E2B's agent relays a machine's `127.0.0.1` listeners onto its own
  interface, so publishing a port also publishes a service that only ever bound to localhost, and
  its callers look local to it. `hib port add` warns when it sees one.
- **Waking.** A connection to a published port wakes a paused target, after the room check and
  its reserve, and at most once every 30 s per target. A machine a person paused by hand isn't
  woken by a link, including while `hib save` has it briefly running. Nothing wakes while the
  host is quiescing.
- **Links can't hold a machine awake on their own.** A machine can't link to itself, and a link
  both wakes and renews its target only while the *calling* machine has activity of its own — an
  SSH session, a busy signal, something you did. A machine woken by a link gets only as long as
  that activity has earned, never a fresh idle. So a ring of machines talking to each other winds
  down instead of keeping the host running, and a retry from inside the ring doesn't start it up
  again.
- **A link never outlives either machine's run.** When a machine pauses, stops or is removed, the
  links through it are cut at once, not when something next notices. E2B hands a paused machine's
  address to another machine seconds later, so a link that lingered could end up pointed at the
  wrong one. The last word on that is the kernel's, not `hibd`'s: the firewall carries link
  traffic only for the exact run `hibd` has proved was still running in the last second, so a
  wedged or killed `hibd` stops links rather than leaking them, and a slot handed on to another
  machine carries nothing of the old one's.
- **Unpublishing cuts what's open.** `hib port rm` closes the connections that are already using
  the port, not just the next ones, and leaves the machine's other ports alone.
- **Lifetime.** Ports belong to the machine and go with `hib rm`. Copies don't inherit them: a
  fork or a machine from a saved image starts with none.
- **Audit.** A line when a link opens and one when it closes, in its own log: who called, which
  machine and port, whether it was refused and why, whether it woke the target, bytes and how
  long it was open. Repeated refusals share a line with a count, and one machine's refusals take
  at most 64 lines in 10 s plus one per kind of refusal, so a machine trying every port can't push
  the owner's history out of the log. `hib log --links` shows the newest entries, and
  `hib port ls` shows how many links are open now. `hib ls` lists each machine's published ports
  in its `PORTS` column.
- The laptop reaches a published port with `ssh -L 8080:localhost:8080 api.hib`, not through the
  link path.

### Images and fork

Built in M6 on E2B's named snapshot, which saves a running machine, memory included, as a
template. E2B's native fork copies every label, `hib.name` included, can't change them afterwards,
and refuses a paused machine, so Hibernaut doesn't use it
([e2b-images-and-fork](research/e2b-images-and-fork.md)).

| Command | E2B calls | Result |
| --- | --- | --- |
| `hib image build dev` | template build from the `images/dev/` files (`images/images.go`) | The base image all machines start from |
| `hib image save app rocket` | `POST /sandboxes/{id}/snapshots` `{name: "hibimg-rocket"}` | An image with memory, so processes keep running. It outlives `app`. |
| `hib new task-1 --image rocket` | create from `hibimg-rocket` | A machine with everything `app` had, in about a second |
| `hib fork app --count 2` | a hidden snapshot `hibfork-<id>-<ms>`, then a create per copy | `app-1` and `app-2` in `app`'s state. `app` pauses for a few seconds and keeps running. |
| `hib image ls`, `hib image rm rocket` | `GET /snapshots`, `DELETE /templates/{id}` | E2B refuses to delete an image a machine still uses |

- **Names:** built-in images are `hib-<name>`, saved images `hibimg-<name>` and fork templates
  `hibfork-<origin ID>-<ms>`, so a saved image can't replace a built-in one. If a later catalog
  adds a name someone saved, the built-in image wins, and `hib image ls` shows the saved one as
  shadowed. `hib image rm` only removes saved images.
- **Copies carry everything in the machine:** files, packages, running processes and env vars.
  That's fine, because a machine set up through `hib` holds only placeholders and your public
  key. A secret you typed into the machine yourself goes into the copy too.
- **Each copy gets its own identity:** its name and labels, a new SSH host key that `hibd` pins,
  the host's current env block, and the idle policy. The host's git identity and Codex login file
  replace the origin's when the host has them. Otherwise the origin's stay. From M12, a copy also
  starts with no published ports and no virtual address: those belong to the machine, not to its
  disk, so a fork of a published machine is unreachable until you publish it. `hib fork` says so.
  Copies share `boot_id` and `/etc/machine-id`. The kernel reseeds its RNG when a copy starts
  (vmgenid), so they don't share randomness.
- **E2B's checkpoint can fail** (1 in about 21 saves on our host). It returns 500 and leaves the
  machine paused at its last good snapshot, losing what it did since. So `hibd` first pauses the
  machine, waits until that snapshot is on disk, and resumes it. A failed save then costs at most
  what the machine did since the save began. `hibd` resumes it, says nothing was saved, and never
  retries on its own. E2B can hide the failed save's template for a few minutes, so `hibd` looks
  for it for up to 90 s and deletes it. If it can't confirm it's gone, it says so instead, and how
  to remove an image. The pause may drop SSH sessions into the machine.
- **Fork** names copies `app-1`, `app-2` and so on with the lowest free numbers, 1 to 10 at a
  time. It keeps the copies it made and stops at the first failure. `hibd` deletes the hidden
  template once its last copy is gone.
- A save or fork takes turns with `hib pause` and `hib rm` of any machine. Waking by SSH doesn't
  wait.
- Ctrl-C stops a save or fork before dispatch. Once E2B starts saving, hibd no longer cancels
  that call on client disconnect. The POST and readback have a ten-minute backstop. SSH loss or
  SIGPIPE can still end hibd, so this does not promise an image appears. Check `hib image ls` and
  `hib ls` before retrying. A fork's hidden template is swept as before. Persistent per-save
  records inhibit snapshot cleanup until a later lifecycle fact proves the save over. A running
  listing settles only a successful save. After a failed save, cleanup waits until a later pause,
  removal or host restart proves it safe.

## 6. Decisions

| # | Decision | Why |
| --- | --- | --- |
| D1 | **SSH is the only control channel in v1** | Already open and key-based. No certs, domains or API keys to run. |
| D2 | **Placeholder swap is the one credential mechanism** | One design covers GitHub, ChatGPT, Copilot and any API key. Stronger than syncing tokens into machines. |
| D3 | **Single user first** | Lets us bake keys and placeholders into images. Per-machine identity comes with multi-user. |
| D4 | **Glue around E2B, behind an `Engine` interface** | No fork of E2B to maintain. Swappable later (plain Firecracker, Cloud Hypervisor, …). |
| D5 | **Go for `hib` and `hibd`** | Static binaries, E2B's language, first-class TLS/HTTP/SSH libraries, and built-in test tooling (race detector, fuzzing, `httptest`) for the proxy, where tests matter most. |
| D6 | **The host never listens to machines as a control channel** | `hibd` polls machines through envd; nothing in a machine can ask the host to do anything. The two ports machines can reach — the egress interception port, and from M12 the link port — accept only machine interfaces, take no commands, and carry only the machine's own traffic. A link's target comes from `hibd`'s map, never from the caller. |
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
| M6 | **Golden images and fork** | A machine started from a saved image has its repo, dependencies and running app in place. So do two forks of it, and the origin keeps running. |
| M7 | **GitHub device flow** (own OAuth App) | Approve once in the browser, then the M4 test passes on the new token |
| M8 | **Machine sizes** (`hib new --size small`) | A small machine has 1 vCPU and 1 GiB. One made without `--size` still has 2 vCPUs and 2 GiB |
| M9 | **Snapshot compaction:** cleanup trims old snapshots | After 10 pauses and a trim, a machine's snapshots take under half the disk they did, and no more than its latest snapshot maps. Its memory and disk read the same after a cold resume, and in an image, a fork and a machine from the image. |
| M10 | **CLI friendliness:** help for every command, suggestions for typos, `hib rm` asks first, completions, `--json` | No new one: help and mistakes are unit-tested, and the existing tests pass with `hib rm --yes` |
| M11 | **Bot faces:** the Hibernaut bot's face on the lines about a machine, awake or asleep, and moving on the live line | No new one: faces are only for terminals, and are unit-tested |
| M12 | **Machine links:** `hib port add NAME PORT` publishes a port, and your other machines reach it as `NAME.hib:PORT`. Off until you publish. | `TestMachineToMachine`: a published port is reachable by name and wakes a paused target; an unpublished port, a reserved port and an unallocated address are refused |

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

State: files in `/var/lib/hibernaut`, written atomically and guarded by `flock`. There is no
database: machine identity lives in E2B's metadata labels. Secrets are encrypted with AES-GCM
using a root-only key file (later: Azure Key Vault through the VM's managed identity).

## 8. Testing

**Rule:** every milestone's demo is an automated test. "It worked once by hand" doesn't count (D7).

| Layer | Runs on | Time | Covers |
| --- | --- | --- | --- |
| Unit + fuzz | laptop, CI | seconds | Placeholders, the swap engine, vault crypto, the `/etc/environment` block, idle policy, the CLI ↔ `hibd` JSON |
| Integration | laptop, CI | seconds | The real proxy against fake GitHub, ChatGPT and Copilot servers over TLS, driven by real `git`, `gh`, `curl` and Node clients. `hibd` against a fake engine. Device flow against a fake GitHub. |
| Host integration | CI (Linux, sudo), host | ~1 min | The iptables chain and `SO_ORIGINAL_DST`, with a network namespace standing in for a machine. From M12, that the link rule sits ahead of the egress rule and that both survive `Keep`, and the kernel guard against real nftables and conntrack. |
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
- From M12: a machine reaches another only on a published port, the caller's identity comes from
  its veth and not from the address it claims, and a refused link is closed without a byte
  written, so `TestNoHostAccess` still finds nothing listening to machines.

**End-to-end details:**

- `make e2e` starts the host if it's asleep, deploys, installs E2B if needed, and runs
  `go test -tags e2e` from the laptop through an SSH tunnel.
- It uses the logins you connected with the normal `hib connect` commands. Browser approvals
  (Codex, and GitHub from M7) happen once by hand. Everything after that is automated.
- GitHub tests use a private fixture repo (`solev/hibernaut-e2e`) and always clean up their
  branches and PRs. A run first removes `e2e-` branches and PRs that killed runs left.
- AI tests use one-line prompts ("reply with OK"), which cost almost nothing.
- Slow scenarios (host reboot and deallocation, 11-minute idle SSH, and from M12 the waits for
  idle timeouts and link leases) run only with `make e2e-slow`, before a milestone closes.

**Commands:** `make test` (unit + integration), `make test-host` (Linux, sudo, and from M12 `nft`
and `conntrack`), `make e2e`, `make e2e-slow`. CI runs `make lint test` and checks generated code
on every push; `test-host` joins CI in M3, with the first host test. CI is parked for now: GitHub
Actions is off for the repo (see the README). `make test-linux-remote` runs every package's unit
tests on the host as its ordinary user, with the pinned Codex.

## 9. Open questions and spikes

| # | Question | Status |
| --- | --- | --- |
| S1 | Can `hibd` intercept machine HTTPS without breaking E2B, and tell which machine sent it? | **Answered live and in code.** One iptables-nft rule inserted above E2B's appended redirects stays first while machines are created, removed, paused and resumed. A redirected connection exposes the slot source IP and local veth IP, and `internal/slots` maps that slot to the sandbox cgroup for audit. |
| S2 | Can E2B's proxy carry a long-lived SSH websocket, and wake a paused machine? | **Yes.** It's E2B's documented SSH pattern, and traffic on any port resumes a paused machine. Needs keepalives (~10-min idle cutoff). |
| S3 | Can E2B fork a machine or turn it into a template? | **Yes, with care, checked live and in E2B's source** ([e2b-images-and-fork](research/e2b-images-and-fork.md)). A named snapshot saves a running machine as a template, and new machines start from it with only their own labels. Native fork copies every label, so M6 forks by a named snapshot and creates. E2B's checkpoint failed 1 in about 21 times, leaving the machine paused at its last good snapshot, so `hibd` makes a fresh one first (§5 Images and fork). |
| S4 | Do paused machines survive a host reboot? | **Yes, tested live in M0** (`TestSurvivesHostRestart`, and `TestWakeOnSSH` for the nightly deallocate). A running machine is paused on the way down, by the guard on a reboot or by `quiesce` before the nightly deallocate, and resumes afterwards with its files and processes. E2B's pause returns before the snapshot is written, so E2B must stop cleanly before power-off (§5 Idle and wake). |
| S5 | Does Claude Code work with a placeholder `CLAUDE_CODE_OAUTH_TOKEN` through the proxy? | **Deferred.** Claude Code comes after v1 ([ideas](ideas.md#claude-code)). |
| S6 | Can Codex on a ChatGPT plan work with no real token in the machine? | **Yes, checked live** with codex-cli 0.160.0 ([codex-chatgpt-auth](research/codex-chatgpt-auth.md) §11). A placeholder login file made by `vault.Placeholder` works with only the `Authorization` bearer swapped on `chatgpt.com`. Codex never refreshed on its own, and sent no token outside `Authorization`, including in WebSocket frames. Its refresh calls and error handling were traced too (§4.4). `CODEX_ACCESS_TOKEN` is simpler, but only Business/Enterprise workspaces can create one. |
| S7 | Which env vars and config does each tool need to trust our CA and run unattended? | **Answered** (§4.2 Trust, §4.4). |
| S8 | Can machines reach host services or the Azure metadata endpoint? | **No, tested live in M3** (`TestNoHostAccess`). A machine probed every port the host listens on, at every host address, the gateway, `169.254.169.254` and `168.63.129.16`. Only SSH on the host's public IP and E2B's service endpoint at `192.0.2.1` answered (§4.5). |
| S9 | Does the Copilot CLI trade the GitHub token for its own short-lived token? Which hosts get which token? | **No exchange, traced live** with Copilot CLI 1.0.91 ([copilot-auth](research/copilot-auth.md) §9). The `gho_` token goes straight to `api.github.com` (`/copilot_internal/user`, `/copilot/mcp_registry`) and the plan's API host (`api.enterprise.githubcopilot.com` for this account). A placeholder swapped on those hosts works. So `*.githubcopilot.com` joins the GitHub secret's sites (§4.3). |
| S10 | Can one machine reach a port on another without opening a mesh, and without a path to the host? | **Built, not yet run on the host** ([M12](milestones/m12-machine-links.md)). E2B's per-sandbox firewall denies private, loopback and link-local addresses but not `198.18.0.0/15`, so a virtual address per machine in that range reaches `hibd` through one `nat PREROUTING` redirect, and `internal/slots` already maps the ingress veth back to the calling sandbox. Links can't hold the host awake by themselves: a link wakes and renews its target only while the calling machine has activity that isn't a link, and a machine can't link to itself. The hard part is cutting a link the moment either machine's run ends, because E2B gives a paused machine's address to another machine about three seconds later; the design watches each run's cgroup rather than polling, and backs that with a kernel firewall rule that carries link traffic only to a run `hibd` has re-proved alive within the last second, so a stalled `hibd` fails closed. Open until `TestMachineToMachine` passes: the added latency of the hop, measured warm as p50, p95 and p99 rather than promised, and whether the room reserve and the wake rate limit hold up under a worker fleet. |

Research: [e2b-capabilities](research/e2b-capabilities.md) (§10 covers egress, `caBundle` and SSH) ·
[agent-auth](research/agent-auth.md) · [codex-chatgpt-auth](research/codex-chatgpt-auth.md) ·
[copilot-auth](research/copilot-auth.md) · [e2b-images-and-fork](research/e2b-images-and-fork.md).
