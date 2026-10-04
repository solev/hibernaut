# M5 AI tools

Status: **in progress** · Spec: §4.1, §4.2 (Token endpoints), §4.3, §4.4 · Spikes: S6 and S9,
both answered

**Goal:** `codex` and `copilot` work in every machine with no login, and no real ChatGPT or GitHub
token ever enters a machine.

## Design

The M5 code is built against this section. Code that has to differ updates it in the same commit.

**Files on the host:**

| Path | Owner, mode | Holds |
| --- | --- | --- |
| `/var/lib/hibernaut/vault.json` | root, 0644 | Adds `codex-access`, bound to `chatgpt.com`, and `codex-refresh` and `codex-id`, scrub-only and bound to `auth.openai.com`. None has an environment variable. The `github` secret gains `*.githubcopilot.com`. |
| `/var/lib/hibernaut/codex.json` | root, 0644 | The Codex profile: account, plan, expiry, health, and the refresh in progress. No token. Written atomically. |
| `/var/lib/hibernaut/codex-retired.json` | root, 0644 | Replaced real values that the leak scan still looks for, in the vault's format and sealed with its key. |
| `/var/lib/hibernaut/codex.lock` | root, 0600 | Held for any refresh or login change, and always taken before the vault's lock. |
| `/var/lib/hibernaut/codex-login.lock` | root, 0600 | Held while `hibd connect codex` runs, so one login runs at a time and nothing deletes its directory. |
| `/usr/local/lib/codex-0.160.0` | root, 0755 | Codex's package, helpers and resources. |
| `/usr/local/bin/codex` | root, symlink | Entrypoint for `hibd connect codex`. |

**Packages:**

- `internal/codex` (new): the login, its profile, refreshing, the login file machines get, and the
  token endpoint. `codex.go` holds the contract: names, sites, types and errors.
- `internal/vault`: a secret's environment variable is optional. `CheckSecret`, which `hib secret`
  uses, still requires one. `Put` checks that variables are unique only when there is one. `rt.`
  joins the known prefixes, so a refresh token's placeholder keeps it.
- `internal/egress/swap`: `Secret.ScrubOnly`, and `Fingerprint`: the first 32 hex digits of a
  value's SHA-256, which tells values apart without keeping them.
- `internal/egress`: `Server.Endpoints`, the requests the proxy answers itself, and
  `Server.Rejected`, which hears about 401s.
- `internal/hibapi`: `Codex`, `CodexHealth`, `Response.Codex`, `Status.Codex` and
  `LeakScan.Secrets`. All are optional, so the protocol stays at version 1.
- `internal/machines`: `Setup.CodexAuth`.

**The login file** (`codex.RenderAuthFile`) is pretty JSON with Codex's keys in Codex's order:
`auth_mode` `"chatgpt"`, `OPENAI_API_KEY` null, `tokens` (`id_token`, `access_token`,
`refresh_token` and `account_id`) and `last_refresh`. The tokens are the vault's placeholders, and
the account ID is real. `last_refresh` is the profile's `Published`, so the file changes only when
a placeholder does. `ParseAuthFile` reads the real tokens from the file `codex login` writes.

**The profile** comes from the tokens: the email and plan from the ID token, the account from the
access token, and `Expires` from the access token's `exp`. It never holds a token.
Its on-disk form also keeps a fingerprint of the placeholders. An interrupted publication uses
that fingerprint to recover `Published`, even when claims outside the profile changed.

**`codex.Store`** is the only code that changes the login or calls OpenAI. Its methods are safe in
many goroutines and processes at once. All of them run as root.

- `Load` returns the profile, or `ErrNotConnected`. It never waits and never writes. An attempt
  left in the profile is in progress while another process holds the lock. Otherwise it shows as
  published if the vault's refresh token is no longer the one the attempt sent or replaced.
  An unpublished refresh is stuck. An unpublished login keeps the old profile and drops the
  attempt, because it sent nothing to OpenAI. A published login recovers a new profile from the
  tokens, with `ConnectedAt` and `LastRefresh` from the attempt's start and no old failure,
  retry, rejection or client-refresh state. The next holder of the lock records the same verdict.
- `Public` returns the placeholders and the account, for the login file. It needs no lock and
  normally no key. A pending attempt needs the key to recover the profile against the same vault
  snapshot as the placeholders, even while the publisher holds the lock. It never writes.
  It checks that the profile stayed the same while reading the vault, and tries again if not.
  After three changing reads it returns an error asking the caller to try again.
  A profile without an attempt whose account or placeholder fingerprint disagrees with the vault
  is refused, with a hint to run `hib connect codex`.
- `EnsureFresh(ctx, reason)` refreshes if the reason calls for it, then returns `Public`.
- `CommitLogin(ctx, tokens)` replaces the login under the lock: it retires the old values, puts the
  new ones, and starts a new profile. Another account is fine. It recovers any previous attempt
  first. Before replacing an existing profile, it writes and fsyncs a login attempt in the old
  profile, with the fingerprint of the refresh token being replaced, or empty if there are no
  Codex tokens. Recovery keeps `Published` unless a placeholder changed, as a completed login
  does. A first connect needs no attempt. A failure before its profile is saved leaves it not
  connected.
- `Retired` returns the replaced values under their original names, for the leak scan.

**When `EnsureFresh` refreshes:**

| Reason | Refreshes when | Otherwise |
| --- | --- | --- |
| `Background`: the daemon, each minute | Half the access token's lifetime (`iat` to `exp`) has passed. Never while dead or stuck, or before `RetryAt`. | Returns the placeholders. |
| `Client`: a machine's refresh request | A site answered 401 to the current real access token (`Rejected` is its fingerprint), or it expires within 5 minutes. At most one in 10 minutes (`ClientRefreshAt`). Past that, `ErrUnavailable`. | Returns the placeholders, whatever the health. |
| `Force`: `hibd codex refresh -force` | Always, even when stuck, but not while dead. | |

A refresh that's due fails with `ErrDead` while the login is dead, even with `Force`: OpenAI has
refused its refresh token, so only `hib connect codex` helps. It fails with `ErrStuck` while it's
stuck, except with `Force`.

**A refresh:**

1. Wait for `codex.lock`, in a way the caller can cancel. Then check again that a refresh is due,
   because another process may have just made one.
2. Write the attempt into the profile, with the fingerprint of the refresh token it sends, and
   fsync it.
3. `POST` the JSON `{"client_id", "grant_type": "refresh_token", "refresh_token"}` to the token
   URL, with no redirects, a 30 second limit, and at most 1 MiB read. `httptrace`'s `WroteRequest`
   tells whether the request went out.
4. The outcome sets the health:

| Outcome | Health | Then |
| --- | --- | --- |
| Not sent, or 429 | Unchanged | The attempt is cleared. The next waits 1 minute, doubling up to 60, or as long as `Retry-After` says, up to 60. |
| 401, or 400 with `invalid_grant`, `refresh_token_expired`, `refresh_token_reused` or `refresh_token_invalidated` | Dead | Nothing more until `hib connect codex`. |
| Any other 4xx | Unchanged | As for 429. |
| 200 with an `access_token` for the same account | OK | Publishing, below. A missing ID or refresh token keeps the old one. |
| Any other 200, a 5xx, or an error after the request went out | Stuck | Never retried by itself. |

**Publishing** runs to the end even if the caller gives up (`context.WithoutCancel`, with its own
limit):

1. Add the values being replaced to `codex-retired.json`. JWTs stay until their `exp`, and refresh
   tokens for 30 days. It holds at most 100, dropping the oldest. Each is named after its secret
   and the Unix second it's dropped, such as `codex-refresh-1793000000`, with `-2` added if that's
   taken. This file never creates the vault's key.
2. Put all three secrets in one `vault.Update`, with no variable and each one's sites.
3. Update the profile: the new `Expires` and `LastRefresh`, `Published` set to now if a placeholder
   changed, health OK, and no attempt, failures or retry time.

**The token endpoint** (`codex.Endpoint`, in the proxy) matches `POST auth.openai.com/oauth/token`
on intercepted connections, after the proxy's Host check. It reads a JSON or form body.

| Request | Answer |
| --- | --- |
| `grant_type` `refresh_token` with the current placeholder | First `Audit("codex-refresh")`, and 503 if that fails. Then `EnsureFresh` with `Client` and the last rejected fingerprint, and 200 with the placeholders it returns as `id_token`, `access_token` and `refresh_token`. |
| The same, but `ErrDead`, `ErrStuck` or `ErrNotConnected` | 401 `{"error": {"code": "refresh_token_invalidated", "message": "…"}}`. The message says to run `hib connect codex`. Codex may show its own text instead. |
| The same, but `ErrUnavailable` or another error | 503 `{"error": "temporarily_unavailable"}` |
| A body that doesn't parse, or no `grant_type` | 400 `{"error": "invalid_request"}` |
| Any other grant or refresh token | 403 `{"error": "access_denied", "error_description": "…"}` |
| A body over 64 KiB, or slower than 10 seconds | 413 or 408, from the proxy |

Every answer has `Cache-Control: no-store`, and nothing is forwarded. `Rejected` keeps the latest
fingerprint for `codex-access`, in memory. When Codex isn't connected there is no current
placeholder, so every `refresh_token` grant gets the 401, without an audit entry.

**The proxy** (`internal/egress`, `internal/proxy` and `hibd proxy`):

- A site is intercepted when any secret is bound to it, scrub-only or not. Only secrets that aren't
  scrub-only are swapped. Responses are scrubbed of every secret, as before.
- An endpoint matches the exact site, method and path, but not the query. The proxy reads the
  body within `MaxEndpointBody` and `EndpointReadTimeout`, and gives the handler an `Audit` that
  logs the request with its machine. It scrubs the answer with the vault as it is after the
  handler, or answers 503 if it can't load the vault.
- On a 401, including one to a WebSocket upgrade, `Rejected` hears about each secret the request
  swapped, with the fingerprint of the value it sent.
- `VaultSource` marks the secrets that `codex.ScrubOnly` names. `hibd proxy` makes one `Store` and
  one `Endpoint`, and sets `Endpoints` to `ep.Egress()` and `Rejected` to `ep.Rejected`.

**Who runs what:**

- `hib connect codex` runs `sudo -n hibd connect codex -json`. It gives up if hibd hasn't
  answered after 16 minutes, a minute more than the code lasts. If it wakes the host first, the
  16 minutes start after that, because the code only starts once hibd runs. It
  shows hibd's stderr as it comes (the code and the link) as plain text, then the account and
  plan. Codex colours its prompt even without a terminal, so `termsafe` drops colour codes, and
  escapes other terminal escapes as before.
- `hibd connect codex` needs root. It checks that `codex --version` is `codex.Version`, then runs
  `codex login --device-auth -c 'cli_auth_credentials_store="file"'` in a new 0700 directory under
  the state directory. The environment is exactly `HOME` and `CODEX_HOME`, both that directory,
  `PATH=/usr/bin:/bin` and `LANG=C.UTF-8`. Codex's output goes to hibd's stderr. Then hibd parses
  `auth.json`, calls `CommitLogin`, deletes the directory whatever happened, writes the setup into
  running machines, and replies with the profile.
  - A failed login adds a hint: turn on device code login in ChatGPT, under Settings, Security.
  - Login directories left by a crash are deleted when the next login starts, and when the daemon
    starts. Both take `codex-login.lock` without waiting. So a second `hib connect codex` is
    refused while one runs, and the daemon leaves a running login's directory alone.
  - The binary's path, the token URL and the HTTP client are `App` fields for tests, never
    settings.
- `hibd codex refresh [-force] [-json]` needs root. It calls `EnsureFresh` with `Background`, or
  with `Force`. It writes the setup into running machines if a placeholder changed, and replies
  with the profile.
- `hibd daemon` calls `EnsureFresh` with `Background` every minute, one call at a time, and logs
  each change of health once. It doesn't while the host is going to sleep, as it leaves machines
  alone then. Machines get a changed login file through the setup delivery the daemon already
  runs, because the file is part of `Setup.Hash`. At start, if GitHub is connected, it puts the
  `github` secret again with `github.Sites()` and the same value, which keeps its placeholder.
- `hibd status` and `hib status` show a Codex line from the profile, without calling OpenAI. Dead
  or stuck isn't OK, and says to run `hib connect codex`. A profile that can't be read fails the
  status, rather than showing Codex as not connected. The `codex-` secrets' rows are unchecked,
  with "part of the Codex login above".
- `hibd secret add` and `rm` refuse names that start with `codex-`.

**Machines and the leak scan:**

- `Setup.CodexAuth` is nil while Codex isn't connected, and nil leaves the file alone. Otherwise
  it's written to `/home/user/.codex/auth.json` through a temporary file and a rename, owned by
  `user`, with the directory 0700 and the file 0600. It's part of `Setup.Hash`.
- The daemon writes the whole setup into a running machine again when `Setup.Hash` changes, and
  at its first look after each resume, which it tells by a new `StartedAt`. E2B gives a resumed
  machine a new start time, and extending a timeout keeps it. So a pause and resume between two
  looks still gets the setup, and brings back a login file that `codex logout` deleted.
- The environment block skips secrets without a variable.
- The leak scan looks for the vault's values and the retired ones, under their original names. It
  lists the names it covered in `LeakScan.Secrets`.

**Pins and the image:**

- `images/dev/versions` sets `CODEX_VERSION=0.160.0`, `CODEX_SHA256` (the x86_64 musl package),
  `COPILOT_VERSION=1.0.91` and `COPILOT_SHA256`. The image's `tools` step, `vm/setup-host.sh` and
  CI read it. A test checks that `CODEX_VERSION` is `codex.Version`.
- Codex uses `codex-package-x86_64-unknown-linux-musl.tar.gz`, not the bare binary tarball. The
  package carries `codex-code-mode-host` and its resources, so code mode works. Both the image
  and the host extract the entire package with `--no-same-owner` into the root-owned
  `/usr/local/lib/codex-$CODEX_VERSION`. The image's start command keeps that package root-owned.
  They stage under `/usr/local/lib`, on the same filesystem, and replace `/usr/local/bin/codex`
  with a temporary symlink to the package's `bin/codex`, then `mv -T`. They require `bin/codex`,
  `bin/codex-code-mode-host` and `codex-package.json` before publishing. Other
  `/usr/local/lib/codex-*` directories are removed only after the symlink points at the new
  version. The host and CI use `vm/install-codex.sh`. It skips a complete package that reports the
  pinned version in a clean environment. A bare binary is migrated even if its version matches.
- The `tools` step is the last one. Builds reach `github.com` through the proxy, which intercepts
  it while GitHub is connected, so every download from there comes after `trust-ca`. websocat's
  download moves from `packages` into this step for the same reason. The step links Codex and
  installs Copilot into `/usr/local/bin`. It writes `/home/user/.codex/config.toml`, owned by
  `user`:
  `approval_policy = "never"`, `sandbox_mode = "danger-full-access"`,
  `check_for_update_on_startup = false`, `[analytics] enabled = false` and
  `cli_auth_credentials_store = "file"`. Copilot's settings turn off the banner and updates, with
  key names checked against `copilot help config`. `COPILOT_AUTO_UPDATE=false` goes into
  `/etc/environment` outside hibd's block, the way `trust-ca` adds its variables.
- `hibd.service` and `hibd-proxy.service` get `ReadWritePaths=/var/lib/hibernaut`, so both can
  refresh.

**Tests that need Codex:** the integration test runs the pinned Codex from `$HIB_CODEX`, or from
`PATH`, with a temporary `HOME` and `CODEX_HOME`. Without it, the test skips, unless
`HIB_REQUIRE_CODEX=1`, which CI sets. The end-to-end Codex tests skip loudly while Codex isn't
connected. No test reads or changes a real `~/.codex`.

## Tasks

**Spike first (S9)**

- [x] Trace one `copilot -p` run through an intercepting proxy (mitmproxy on the laptop is enough):
      hosts, which token goes where, any token exchange (endpoint, response fields, lifetime), and
      what gets written under `~/.copilot`. The capture holds real tokens: keep it in a temp dir
      and delete it afterwards.
- [x] Same for one `codex exec` run on a ChatGPT login: confirm the token only goes to
      `chatgpt.com` and `auth.openai.com`. Also run Codex with a placeholder login file, and see
      how it refreshes.
- [x] Write the findings into the research docs and the spec (S6, S9, bound sites).

**Image**

- [x] `images/dev/versions` pins Codex 0.160.0 and the Copilot CLI 1.0.91 by version and SHA-256.
      The host reads the same file.
- [x] Codex's canonical package, with code-mode-host, installed under
      `/usr/local/lib/codex-0.160.0`, with `/usr/local/bin/codex` linked to its `bin/codex`.
      Copilot installed from its release tarball into `/usr/local/bin`.
- [x] Builds download from `github.com` only after `trust-ca`, since the proxy intercepts it.
      websocat's download moves there too.
- [x] `~/.codex/config.toml`: `approval_policy = "never"`, `sandbox_mode = "danger-full-access"`,
      `check_for_update_on_startup = false`, analytics off, and
      `cli_auth_credentials_store = "file"`.
- [x] `COPILOT_AUTO_UPDATE=false`, and Copilot's settings without the banner.

**Proxy**

- [x] Scrub-only secrets: their sites are intercepted and responses are scrubbed of them, but they
      are never swapped. Codex's refresh and ID tokens are scrub-only.
- [x] Token endpoints: the proxy answers a few requests itself. It reads their bodies, capped, and
      audits them. Codex's refresh is the only one.
- [x] A site's 401 to a swapped token is reported, so a refresh happens only when it's needed.

**Codex (ChatGPT plan)**

- [x] The pinned Codex installed on the host, used only for the device login.
- [x] `hib connect codex`: `hibd` runs `codex login --device-auth` with a throwaway `CODEX_HOME`
      and a clean environment, and `hib` shows its output (the code and link) as it comes. The
      tokens go into the vault as `codex-access`, `codex-refresh` and `codex-id`, then the
      directory is deleted. Connecting again replaces the login. A clear hint if device login
      isn't turned on in ChatGPT's security settings.
- [x] Placeholder login file: the real claims, a far-future `exp` and fake signatures, and a
      placeholder refresh token. `hibd` writes it to `~/.codex/auth.json` (mode 0600, owned by
      `user`) on create, on resume, and when the placeholders change.
- [x] `codex-access` is swapped on `chatgpt.com`. `codex-refresh` and `codex-id` are scrub-only,
      bound to `auth.openai.com`.
- [x] Refresher in `hibd daemon`: at half the token's lifetime, one refresh at a time across
      processes. The new tokens are in the vault before anything uses them, and old values stay in
      the leak scan. A refresh with an unknown outcome is never retried by itself.
- [x] Token endpoint: the proxy answers `POST auth.openai.com/oauth/token` itself, as §4.4 says.
      It never goes to OpenAI.
- [x] `hibd codex refresh [-force]`, so tests can make a real refresh.
- [x] `hib status`: the account, the plan, the expiry, and whether the login is dead or stuck,
      with "run `hib connect codex`".
- [x] `hib secret` refuses names that start with `codex-`.
- [x] `hibd.service` and `hibd-proxy.service` may write `/var/lib/hibernaut`, so both can refresh.

**Copilot CLI**

- [x] `*.githubcopilot.com` joins the GitHub secret's sites.
- [x] `hibd daemon` adds it to a GitHub connection made before M5.

## Tests

Unit (fake clock):

- [x] The placeholder login file has the layout Codex writes. Its JWTs decode the way Codex
      decodes them (payload only), with the real claims and a far-future `exp`.
- [x] Refresher: rotates and saves, keeps placeholders that still fit, and retires old values.
      Many concurrent refreshes cause one upstream refresh. Each outcome (not sent, 200, 401, 400
      `invalid_grant`, 429, 5xx, no answer, a malformed answer, a crash mid-refresh) leaves the
      right health and retry time.
- [x] Token endpoint: each answer in §4.4, audited first, scrubbed, and never forwarded.
- [x] Scrub-only secrets are scrubbed but never swapped.

Integration (fake `chatgpt.com` and `auth.openai.com`, signed by the test CA, and the pinned
`codex`. CI installs it. Locally these skip without it.):

- [x] Real `codex exec` through the proxy: the fake backend sees the real access token, the
      client only placeholders.
- [x] The fake backend answers 401. Codex refreshes, the proxy answers locally, and the retry
      succeeds. The fake auth server sees exactly one refresh, from `hibd`.
- [x] Ten clients refreshing at once cause one upstream refresh.

End-to-end:

- [ ] `TestCodex`: in a fresh machine, `codex exec --skip-git-repo-check "Reply with exactly OK."`
      prints OK with no login prompt.
- [ ] `TestCodexRefresh`: `sudo hibd codex refresh -force` makes a real refresh. Codex still works
      in an existing machine and in a new one.
- [x] `TestCopilot`: in a fresh machine,
      `copilot -p "Reply with exactly OK." -s --allow-all-tools --no-ask-user` prints OK.
- [ ] The leak scan after all three covers `github`, `codex-access`, `codex-refresh` and
      `codex-id`, old values included, and finds nothing.

## Done when

- [ ] `make lint test`, `make test-host-remote`, CI and `make e2e` are green.
- [x] Spec: S6 and S9 marked answered, and bound sites updated.
- [x] README: `hib connect codex`, and Copilot working through the GitHub connection.

## Changes

- 2026-09-30: `hib connect codex --access-token` moved to Later. Our ChatGPT account is Plus, which
  can't create Codex access tokens, so there's nothing to test it against.
- 2026-10-04: S9 traced live: Copilot sends the GitHub token itself, with no exchange. So the
  exchange tasks and tests are gone, and `*.githubcopilot.com` joins the GitHub secret's sites.
  S6 checked live with codex-cli 0.160.0, which is now the pinned version. After a plan review:
  three managed Codex secrets (two of them scrub-only), refreshes at half the lifetime, refreshes
  with an unknown outcome never retried by themselves, the token endpoint's answers, and
  `hibd codex refresh` for tests.
- 2026-10-04: The live device login showed Codex's colour codes as text, such as `\x1b[94m`.
  Codex writes them whether or not it has a terminal, and `NO_COLOR` doesn't stop them. So
  `termsafe` now drops colour codes. Other terminal escapes are still escaped.
- 2026-10-04: The image's `tools` step is last, after `trust-ca`: builds reach `github.com`
  through the proxy, which intercepts it while GitHub is connected. websocat's download moved into
  it too. Codex installs as its full package under `/usr/local/lib/codex-0.160.0`, linked from
  `/usr/local/bin/codex`, because the bare binary lacks `codex-code-mode-host`.
- 2026-10-04: Built as designed, plus: the profile keeps a fingerprint of the placeholders, so a
  recovered publication gets `Published` right. While Codex isn't connected, the token endpoint
  answers every refresh with the 401, without an audit entry. The daemon doesn't refresh while
  the host is going to sleep.
- 2026-10-04: After the M5 reviews: `hibd connect codex` takes `codex-login.lock`, so one login
  runs at a time. `hibd status` fails on a profile it can't read, and shows the `codex-` secrets
  as part of the login. `Force` still fails on a dead login, since only reconnecting helps. hib's
  16 minutes for hibd start once the host is awake. The daemon writes the setup again at its
  first look after each resume, which it tells by the machine's new `StartedAt`. So a pause and
  resume between two looks restores Codex's login file and the rest of the setup. The engine
  contract checks that a resume moves `StartedAt` and extending a timeout doesn't. A login that
  replaces another records an attempt before publishing, as a refresh does. So an interrupted
  account switch recovers as the old login or the new one, never a mix of the two, and `Public`
  reads one generation. `Public` refuses a profile that doesn't match the vault without an
  attempt to explain it.
