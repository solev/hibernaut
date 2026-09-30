# Agent CLI Auth, Persistence & Proxy/TLS Support — Research Notes

**Date checked:** 2026-09-30 (all primary sources fetched/read live on this date unless noted).
**Scope:** Claude Code, OpenAI Codex CLI, OpenCode, GitHub/`gh`/`git`, and TLS-interception behavior across common runtimes (Node, Bun, Python, Go, Rust, git, curl, containers).
**Method:** Primary sources only — vendor docs, official GitHub repos pinned to a commit/tag where possible, RFCs, official blog/engineering posts. Every claim below is inline-cited. Claims that could only be corroborated by community sources are explicitly flagged as **unverified/gap**.

**Versions/commits referenced:**
- Claude Code docs: `code.claude.com/docs/*` (no on-page version; docs cite CLI versions inline, e.g. v2.1.242, v2.1.268, v2.1.217, v2.1.196). `anthropics/claude-code` `.devcontainer/init-firewall.sh` at commit `d945a61bc6346abce607252d5667df8a3bf0461a` (2026-06-30). https://github.com/anthropics/claude-code-action/blob/main/docs/setup.md (main branch, fetched 2026-09-30).
- OpenAI Codex CLI: `openai/codex` at commits `d42056091aded7feb1d88ac7e83972108b2aa478` and `bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0` (both resolving to the default-branch tip on 2026-09-30). Public docs at `developers.openai.com/codex/*` (redirects to `learn.chatgpt.com/docs/*`).
- OpenCode: `github.com/anomalyco/opencode` (formerly `sst/opencode`) at commit `2fa3363c924c5c3e367b84a87ae478296a0ed59b`, release `v1.18.33` (published 2026-09-28).
- GitHub CLI: `cli/cli` at commit `fc4b137cdef0a6bd28fd461b7cf9c84a5812a8cd`; `cli/go-gh` at commit `859e813416733b742d6fc21df59a2058e04f0f38` (auth.go resolution logic also referenced from blob `4c54642f35b453b3a519ee9af4da7246fcbdd4a6`). `docs.github.com` pages fetched live 2026-09-30. RFC 8628 (Aug 2019).
- TLS runtimes: `nodejs/node` docs pinned v22.9.0; `oven-sh/bun` main branch (`624891b`); `psf/requests` main branch + PyPI `certifi`; `golang/go` pinned `go1.23.0`; `rustls/rustls-native-certs` + `rustls/webpki-roots` main branch READMEs; `git-scm.com/docs/*`; `curl/curl` docs pinned `curl-8_11_0`; Debian/Alpine update-ca-certificates: https://manpages.debian.org/bookworm/ca-certificates/update-ca-certificates.8.en.html and https://man.archlinux.org/man/update-ca-certificates.8.en manpage.

---

## Primary source URL abbreviations

To keep each finding compact, repeated source citations below use these pinned/primary URLs; code-source citations use GitHub blob URLs at the commit listed here.

- Claude Code docs: authentication https://code.claude.com/docs/en/authentication; env vars https://code.claude.com/docs/en/env-vars; network/proxy/TLS https://code.claude.com/docs/en/network-config; headless https://code.claude.com/docs/en/headless; setup https://code.claude.com/docs/en/setup; permissions https://code.claude.com/docs/en/permission-modes; Claude directory https://code.claude.com/docs/en/claude-directory; Agent SDK policy note https://code.claude.com/docs/en/agent-sdk; Bedrock https://code.claude.com/docs/en/amazon-bedrock; Vertex https://code.claude.com/docs/en/google-vertex-ai; Anthropic AUP https://www.anthropic.com/legal/aup; Claude Code firewall allowlist https://github.com/anthropics/claude-code/blob/d945a61bc6346abce607252d5667df8a3bf0461a/.devcontainer/init-firewall.sh; Claude Code Action setup-token doc https://github.com/anthropics/claude-code-action/blob/main/docs/setup.md.
- Codex docs and code: auth https://developers.openai.com/codex/auth; advanced config/CODEX_HOME https://developers.openai.com/codex/config-advanced; noninteractive `exec` https://developers.openai.com/codex/noninteractive; AGENTS.md https://developers.openai.com/codex/guides/agents-md; auth manager https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/login/src/auth/manager.rs; OAuth server https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/login/src/server.rs; OAuth client https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/login/src/oauth/client.rs; token data/JWT parsing https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/login/src/token_data.rs; auth storage https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/login/src/auth/storage.rs; model-provider hosts https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/model-provider-info/src/lib.rs; bearer headers https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/model-provider/src/bearer_auth_provider.rs; custom CA https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/http-client/src/custom_ca.rs; proxy readme https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/http-client/README.md; network-proxy readme https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/network-proxy/README.md; sandbox notes https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/core/README.md.
- OpenCode docs and code (commit `2fa3363c924c5c3e367b84a87ae478296a0ed59b`): repo https://github.com/anomalyco/opencode; auth source https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/auth/index.ts; CLI docs https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/web/src/content/docs/cli.mdx; provider docs https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/web/src/content/docs/providers.mdx; config docs https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/web/src/content/docs/config.mdx; network docs https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/web/src/content/docs/network.mdx; package runtime https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/package.json; GitHub Actions docs https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/web/src/content/docs/github.mdx; Claude OAuth issues https://github.com/anomalyco/opencode/issues/6930 and https://github.com/anomalyco/opencode/issues/18315.
- GitHub/gh/git: OAuth device flow docs https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#device-flow; RFC 8628 https://www.rfc-editor.org/rfc/rfc8628; scopes https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps; credential types https://docs.github.com/en/organizations/managing-programmatic-access-to-your-organization/github-credential-types; token formats https://github.blog/engineering/platform-security/behind-githubs-new-authentication-token-formats/; gh auth flow https://github.com/cli/cli/blob/fc4b137cdef0a6bd28fd461b7cf9c84a5812a8cd/internal/authflow/flow.go; gh login flow https://github.com/cli/cli/blob/fc4b137cdef0a6bd28fd461b7cf9c84a5812a8cd/pkg/cmd/auth/shared/login_flow.go; gh login command https://github.com/cli/cli/blob/fc4b137cdef0a6bd28fd461b7cf9c84a5812a8cd/pkg/cmd/auth/login/login.go; gh token precedence https://github.com/cli/go-gh/blob/859e813416733b742d6fc21df59a2058e04f0f38/pkg/auth/auth.go; gh config dir https://github.com/cli/go-gh/blob/859e813416733b742d6fc21df59a2058e04f0f38/pkg/config/config.go; gh git-credential setup https://github.com/cli/cli/blob/fc4b137cdef0a6bd28fd461b7cf9c84a5812a8cd/pkg/cmd/auth/shared/gitcredentials/helper_config.go; REST auth headers https://docs.github.com/en/rest/authentication/authenticating-to-the-rest-api; git credentials https://git-scm.com/docs/gitcredentials; git config https://git-scm.com/docs/git-config.
- TLS/runtime docs: Node TLS roots https://nodejs.org/download/release/v22.9.0/docs/api/tls.html#tlsrootcertificates and `NODE_EXTRA_CA_CERTS` https://nodejs.org/download/release/v22.9.0/docs/api/cli.html#node_extra_ca_certsfile; Bun CA source https://github.com/oven-sh/bun/blob/624891b/packages/bun-usockets/src/crypto/root_certs.cpp and cert env source https://github.com/oven-sh/bun/blob/624891b/src/runtime/socket/cert_files.rs; requests env CA source https://github.com/psf/requests/blob/main/src/requests/sessions.py; certifi https://pypi.org/project/certifi/; Go root loading https://github.com/golang/go/blob/go1.23.0/src/crypto/x509/root_unix.go and Linux files https://github.com/golang/go/blob/go1.23.0/src/crypto/x509/root_linux.go; rustls-native-certs https://github.com/rustls/rustls-native-certs; webpki-roots https://github.com/rustls/webpki-roots; curl CA docs https://github.com/curl/curl/blob/curl-8_11_0/docs/cmdline-opts/cacert.md and https://curl.se/docs/manpage.html; Debian update-ca-certificates https://manpages.debian.org/bookworm/ca-certificates/update-ca-certificates.8.en.html; Alpine update-ca-certificates https://man.archlinux.org/man/update-ca-certificates.8.en.

## Summary

All five agent CLIs support both a browser/OAuth-based consumer login and a static-API-key/token path, but they diverge sharply in **where credentials live, whether they auto-refresh, what hosts/headers are involved, and how TLS interception is configured** — which matters directly for running these tools inside a sandboxed/ephemeral Hibernaut environment behind a MITM proxy.

- **Claude Code** stores OAuth/session credentials outside its config dir (implied OS keychain for some flows), reads `ANTHROPIC_API_KEY`/`ANTHROPIC_AUTH_TOKEN`/`CLAUDE_CODE_OAUTH_TOKEN` as override paths, and has first-class, well-documented proxy (`HTTPS_PROXY`/`HTTP_PROXY`/`NO_PROXY`, no SOCKS) and CA-trust (`NODE_EXTRA_CA_CERTS`, `CLAUDE_CODE_CERT_STORE`) support — but a **`--bare`/headless mode intentionally skips OAuth/keychain reads entirely**, making static credential injection (`ANTHROPIC_API_KEY` or `apiKeyHelper`) the reliable automation path. A **native (non-npm) installer** exists with unconditional OS-trust-store access; npm installs need Node ≥22.15 for the same.
- **Codex CLI** stores everything in a single `auth.json` file under `$CODEX_HOME` (default `~/.codex`), with real JWT parsing/refresh logic in Rust, refreshing proactively against `auth.openai.com`. It is TLS-mature: a Codex-specific `CODEX_CA_CERTIFICATE` env var (falling back to `SSL_CERT_FILE`) is purpose-built for corporate MITM CAs, and its Rust TLS stack (`reqwest`+`rustls`+`rustls-native-certs`) is explicitly documented.
- **OpenCode** (canonical repo is now `anomalyco/opencode`, formerly `sst/opencode`) stores multi-provider credentials (API key, OAuth, or "wellknown") in a single JSON file, `~/.local/share/opencode/auth.json`, mode `0600`, and documents `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` + `NODE_EXTRA_CA_CERTS` explicitly. Its "Claude Pro/Max" OAuth login exists in the schema but is a documented ToS/ban risk per live issues on the repo.
- **GitHub/gh/git**: `gh auth login` is a standard RFC 8628 device flow requesting `repo, read:org, gist` scopes, storing the resulting long-lived `gho_` token in the OS keyring or `~/.config/gh/hosts.yml`, with clean env-var override precedence (`GH_TOKEN` > `GITHUB_TOKEN` > config file) and automatic git-credential-helper wiring.
- **TLS interception**: no single env var covers every tool. Node/Bun/Python-`requests`/Rust-`webpki-roots` all bundle their own root store and ignore the OS store by default; Go/curl(OpenSSL)/git(OpenSSL) use the OS store and respond to `SSL_CERT_FILE`; macOS-linked curl/git (Secure Transport) and Windows curl (Schannel) ignore `SSL_CERT_FILE`/`CURL_CA_BUNDLE` entirely and require OS-keychain/cert-store injection instead. **Statically-compiled Rust binaries using `webpki-roots` cannot pick up a runtime-injected CA at all** — this is the single biggest gotcha for any Rust-based CLI (Codex CLI is not in this category since it also ships `rustls-native-certs`, but this must be verified per binary/build).

---

## 1. Claude Code

Source: `code.claude.com/docs/en/{authentication,env-vars,network-config,headless,setup,cli-reference,claude-directory,agent-sdk}` (fetched 2026-09-30); `github.com/anthropics/claude-code/.devcontainer/init-firewall.sh` (commit `d945a61bc6346abce607252d5667df8a3bf0461a`, 2026-06-30); `github.com/anthropics/claude-code-action/docs/setup.md` (main); https://www.anthropic.com/legal/aup (fetched 2026-09-30).

### Auth precedence
- `ANTHROPIC_API_KEY`, when set, **overrides** an existing claude.ai OAuth/subscription login: "this key is used instead of your Claude Pro, Max, Team, or Enterprise subscription even if you are logged in. In non-interactive mode (`-p`), the key is always used when present. In interactive mode, you are prompted to approve the key once." — https://code.claude.com/docs/en/env-vars, `ANTHROPIC_API_KEY` row.
- Org-restriction policy (`forceLoginOrgUUID`/`forceLoginMethod`) can **block** `ANTHROPIC_API_KEY`/`ANTHROPIC_AUTH_TOKEN`/`apiKeyHelper` at startup entirely, since these can't be verified against an org membership — https://code.claude.com/docs/en/authentication, "Restrict login to your organization."
- `claude setup-token` and `/install-github-app` are exempted from `forceLoginOrgUUID` (only `forceLoginMethod` applies), so they can mint a token in a different org — same page.
- **Gap:** no single ranked list covering all five credential types (`ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN`, `apiKeyHelper`, stored OAuth) was found on one page; precedence above is assembled from separate statements.

### `/login` OAuth flow
- Default flow spins up a **local HTTP callback server**; browser opens automatically. Manual fallback ("paste code") only triggers when the browser can't reach the local callback (documented explicitly for **WSL2, SSH sessions, and containers**) — press `c` to copy the login URL, or paste a shown code into the `Paste code here if prompted` terminal prompt. — https://code.claude.com/docs/en/authentication.
- Config root: `~/.claude`, overridable via `CLAUDE_CONFIG_DIR` (supports multi-account setups) — same page.
- **Gap/unverified:** exact credential file name/path (e.g. `~/.claude/.credentials.json`) and macOS Keychain service name were **not found in Anthropic's own docs pages**; only corroborated by third-party community sources (not cited here as fact). The one primary-source confirmation of a system-keychain path is indirect: `--bare` mode explicitly states it "never reads OAuth credentials or the system keychain" — https://code.claude.com/docs/en/headless.
- Auto-refresh is explicitly documented for the **Console OAuth "profile"** flow: "the profile stores an OAuth login... Claude Code refreshes the profile's login automatically, and when refresh fails, requests fail with [Anthropic profile login expired]" — https://code.claude.com/docs/en/authentication. The claude.ai subscription login's own refresh behavior is not separately confirmed in text retrieved (reasonable inference, not a direct quote).

### `claude setup-token`
- Confirmed intended for **Claude Pro/Max subscribers**, generating a `CLAUDE_CODE_OAUTH_TOKEN` for headless/CI use: "`CLAUDE_CODE_OAUTH_TOKEN` for OAuth token authentication (**Pro and Max users** can generate this by running `claude setup-token` locally)" — https://github.com/anthropics/claude-code-action/blob/main/docs/setup.md, "Manual Setup (Direct API)".
- Used in GitHub Actions as: `env: { CLAUDE_CODE_OAUTH_TOKEN: ${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }} }` — same file.
- **Gap:** exact token lifetime not confirmed from an Anthropic-authored page in this pass (community reports ~1 year; not verifiable here as primary fact). Recommend fetching `claude setup-token --help` output directly or a deeper offset into `code.claude.com/docs/en/cli-reference`.

### `ANTHROPIC_API_KEY` vs `ANTHROPIC_AUTH_TOKEN` vs `apiKeyHelper`
- `ANTHROPIC_API_KEY` → sent as the `X-Api-Key` header.
- `ANTHROPIC_AUTH_TOKEN` → sets a custom value for the `Authorization` header directly (bearer-style), distinct from a first-party API key — typically paired with a custom `ANTHROPIC_BASE_URL` (e.g. corporate LLM gateway). — both: https://code.claude.com/docs/en/env-vars, "Variables" table.
- `apiKeyHelper`: a settings-file (or `--settings` JSON) key that supplies a **dynamically computed** API key; usable in `--bare`/headless mode as the alternative to a static `ANTHROPIC_API_KEY` — https://code.claude.com/docs/en/headless, "Start faster with bare mode". **Gap:** TTL/caching default for the helper's output could not be confirmed from a primary source in this pass (the `settings-reference` page's `apiKeyHelper` row is rendered via JS and did not resolve to plain text via fetch).

### Cloud providers (Bedrock / Vertex / Foundry)
- Selectable via `/login` "3rd-party platform" wizard or by pre-setting env vars: no browser login needed — https://code.claude.com/docs/en/authentication.
- Bedrock: `CLAUDE_CODE_USE_BEDROCK=1` (confirmed indirectly via a `/setup-bedrock` command-visibility gate) — https://code.claude.com/docs/en/amazon-bedrock. AWS creds come from the standard AWS SDK credential chain (docs list `aws configure` as optional, implying broader chain support).
- Vertex/Google Cloud Agent Platform: `CLAUDE_CODE_USE_VERTEX` (name confirmed via a worked example in the `env-vars` precedence section); uses `gcloud`/Application Default Credentials — https://code.claude.com/docs/en/google-vertex-ai.
- Cloud-provider sessions **always create/use an API-key-style credential**, never an OAuth profile: "Claude Code creates an API key without asking in these cases: You run against a cloud provider" — https://code.claude.com/docs/en/authentication.
- Bedrock-specific vars/headers: `ANTHROPIC_BEDROCK_BASE_URL`, `ANTHROPIC_BEDROCK_MANTLE_BASE_URL`, `ANTHROPIC_BEDROCK_REGION_PREFIX`, `ANTHROPIC_BEDROCK_SERVICE_TIER` (→ `X-Amzn-Bedrock-Service-Tier` header) — https://code.claude.com/docs/en/env-vars.

### Storage / persistence
- Config dir `~/.claude` (override `CLAUDE_CONFIG_DIR`); debug logs at `~/.claude/debug/<session-id>.txt`; `claude auth status` JSON exposes `configDirectory` (requires v2.1.268+) — `code.claude.com/docs/en/{authentication,network-config,cli-reference}`.
- Console-OAuth-profile sign-ins are stored **outside** the config directory (implying an OS-level store) — https://code.claude.com/docs/en/authentication.
- **Gap:** literal on-disk credential file path/schema and Keychain service name not found in Anthropic docs pages successfully fetched — flagged unverified from primary sources.

### Hosts
- Best documented (but explicitly non-exhaustive) list is the **contributor devcontainer firewall allowlist**: `registry.npmjs.org`, `api.anthropic.com`, `sentry.io`, `statsig.com`, `marketplace.visualstudio.com`, `vscode.blob.core.windows.net`, `update.code.visualstudio.com`, plus dynamically-fetched GitHub IP ranges — https://github.com/anthropics/claude-code/blob/d945a61bc6346abce607252d5667df8a3bf0461a/.devcontainer/init-firewall.sh. The commit message itself ("remove statsig.anthropic.com") confirms Anthropic previously used, then dropped, `statsig.anthropic.com` in favor of `statsig.com`.
- `api.anthropic.com` is the default API host, and `ANTHROPIC_BASE_URL` pointing elsewhere disables "Remote Control" (v2.1.196+) — https://code.claude.com/docs/en/env-vars.
- AWS-specific default: `ANTHROPIC_AWS_BASE_URL` defaults to `https://aws-external-anthropic.{region}.api.aws` — same page.
- **Gap:** treat the firewall-script list as indicative, not exhaustive (it's a dev-container convenience script, not a customer-facing network-requirements doc); claude.ai OAuth domains and native-installer update infrastructure were not separately confirmed.

### Headers
- `X-Api-Key` (from `ANTHROPIC_API_KEY`); `Authorization` (from `ANTHROPIC_AUTH_TOKEN`); `anthropic-workspace-id` (from `ANTHROPIC_AWS_WORKSPACE_ID`, sent on every request); `anthropic-beta` (extra values from `ANTHROPIC_BETAS`); `X-Amzn-Bedrock-Service-Tier`; arbitrary custom headers via `ANTHROPIC_CUSTOM_HEADERS` (`Name: Value`, newline-separated, validated); `x-api-key` for `ANTHROPIC_AWS_API_KEY` (takes precedence over AWS SigV4) — all: https://code.claude.com/docs/en/env-vars. `anthropic-version` was **not found** in Claude-Code-specific docs pages fetched (gap).

### Proxy / TLS / native build
- Proxy: respects `https_proxy`, `HTTPS_PROXY`, `http_proxy`, `HTTP_PROXY` (first one set wins, lowercase checked first); `NO_PROXY` supports comma/space-separated hosts or `*`. **"Claude Code does not support SOCKS proxies."** — https://code.claude.com/docs/en/network-config.
- Custom CA: `export NODE_EXTRA_CA_CERTS=/path/to/ca-cert.pem` — same page.
- **Native vs npm CA-trust difference (directly answers the native-build question):** "By default, Claude Code trusts both its bundled Mozilla CA certificates and your operating system's certificate store. Reading the OS store requires a runtime with `tls.getCACertificates`: the **native installer always has it**, and **npm installs need Node 22.15 or later**. On older Node versions, only the bundled set and `NODE_EXTRA_CA_CERTS` apply." — same page. I.e., contrary to a naive assumption that native builds ignore `NODE_EXTRA_CA_CERTS`, the reverse nuance holds: `NODE_EXTRA_CA_CERTS` works on both; it's *OS-store* trust that's native-guaranteed/Node-version-gated.
- `CLAUDE_CODE_CERT_STORE` (values `bundled`, `system`, comma-separated; default `bundled,system`) restricts which CA source(s) are trusted; set via `env` block or process env (no dedicated `settings.json` key) — same page.
- mTLS: `CLAUDE_CODE_CLIENT_CERT` / `CLAUDE_CODE_CLIENT_KEY` / `CLAUDE_CODE_CLIENT_KEY_PASSPHRASE` (re-read on stale-connection failures; toggle via `CLAUDE_CODE_DISABLE_MTLS_RELOAD_ON_STALE_CONNECTION=1`) — same page.
- **Cloud/Desktop-managed sessions ignore local TLS/proxy env** when set via a settings-file `env` block: `CLAUDE_CODE_CLIENT_CERT`, `_KEY`, `_KEY_PASSPHRASE`, `NODE_EXTRA_CA_CERTS`, `NODE_TLS_REJECT_UNAUTHORIZED`, `CLAUDE_CODE_OAUTH_SCOPES` — same page. This is a **gotcha for Hibernaut**: if Claude Code detects it's in a "cloud session" or Desktop-managed session, proxy/CA settings from repo-local config are ignored; only `~/.claude/settings.json` (or true process env) is honored.
- Only the proxy URL is validated at startup (unparseable → launch fails naming the bad var) — same page.
- Native install: `curl -fsSL https://claude.ai/install.sh | bash` (auto-updates in background); Homebrew/WinGet installs do **not** auto-update — https://code.claude.com/docs/en/setup.

### Unattended / headless / root / skills
- `-p`/`--print` flag → non-interactive mode, exits 0/non-zero — https://code.claude.com/docs/en/headless.
- `--bare` mode: skips hook/skill/command/subagent/plugin/MCP/memory/CLAUDE.md auto-discovery for speed and determinism; "recommended mode for scripted and SDK calls," will become default for `-p`. **Never reads OAuth credentials or system keychain** in bare mode — must supply `ANTHROPIC_API_KEY` or `apiKeyHelper` — same page.
- Permission modes for CI: `dontAsk` ("Locked-down CI and scripts") and `bypassPermissions` ("Isolated containers and VMs only"); `--dangerously-skip-permissions` requires a container/VM/sandbox and "on Linux and macOS, run it as a non-root user" — https://code.claude.com/docs/en/permission-modes. **Gap:** the literal runtime error string for root usage is community-sourced only, not found verbatim in Anthropic docs.
- Skills/extension mechanism: `.claude/` and `~/.claude/` for CLAUDE.md, `.mcp.json`, skills (`/docs/en/skills`), commands ("now the same mechanism" as skills), `.worktreeinclude`, subagents — https://code.claude.com/docs/en/claude-directory; Agent SDK confirms "Load automatically from your project's `.claude/` and from `~/.claude/`" — https://code.claude.com/docs/en/agent-sdk.

### Anthropic policy
- AUP: "Agentic use cases must still comply with the Usage Policy," pointing to a Help Center article (`support.anthropic.com/en/articles/12005017-using-agents-according-to-our-usage-policy`) for specifics — https://www.anthropic.com/legal/aup (not separately fetched in this pass; **gap**, recommend direct fetch for granular sandbox/credential-sharing language).
- Separate product-policy note (Agent SDK docs, not AUP text): "Unless previously approved, Anthropic does not allow third party developers to offer claude.ai login or rate limits for their products, including agents built on the Claude Agent SDK. Use the API key authentication methods described in the Quickstart instead." — https://code.claude.com/docs/en/agent-sdk. **This directly bears on Hibernaut**: reusing/relaying a user's claude.ai OAuth login through a third-party product is explicitly discouraged; API-key auth is the sanctioned path for third-party integration.

---

## 2. OpenAI Codex CLI

Source: `github.com/openai/codex` at commits `d42056091aded7feb1d88ac7e83972108b2aa478` and `bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0`; public docs `developers.openai.com/codex/*` (redirects to `learn.chatgpt.com/docs/*`), fetched 2026-09-30.

### Auth methods & precedence
Two primary sign-in methods plus CI/workload paths, per https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/login/src/auth/manager.rs:
- **ChatGPT OAuth** (`AuthMode::Chatgpt`) via `codex login` (browser flow).
- **API key** via `codex login --with-api-key` (piped via stdin), persisted as `OPENAI_API_KEY` in `auth.json`.
- Env vars: `CODEX_API_KEY` (Codex-specific, highest precedence), `OPENAI_API_KEY` (legacy), `CODEX_ACCESS_TOKEN` (PAT with `at-` prefix or Agent Identity JWT, for CI/workload identity).
- Bedrock API key / access keys (`AuthMode::BedrockApiKey` / `BedrockAccessKeys`) for AWS-hosted models.

**Precedence order** (`fn load_auth(...)`, https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/login/src/auth/manager.rs#L1489-L1594):
1. `CODEX_API_KEY` env var — "API key via env var takes precedence over any other auth method."
2. In-memory/ephemeral external ChatGPT auth store (process-local, host-app-injected) — "checked first so external auth takes precedence over any persisted credentials."
3. `CODEX_ACCESS_TOKEN` env var (PAT or Agent Identity JWT).
4. Persisted store (`auth.json` / OS keychain / "auto") loaded from `$CODEX_HOME`.

Public docs corroborate two-method framing: "Codex supports two ways for a person to sign in… Sign in with ChatGPT… Sign in with an API key" and `printenv OPENAI_API_KEY | codex login --with-api-key` — https://developers.openai.com/codex/auth. "When the process selects workload identity, Codex rejects `codex login` and `codex logout`" — same page.

### `codex login` OAuth flow
- **Local-callback (loopback) PKCE authorization-code flow**, not a device-code flow (a separate device-code path exists in `codex-rs/login/src/device_code_auth.rs`, not fully traced — flagged as a follow-up).
- https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/login/src/server.rs: binds an HTTP server to `127.0.0.1:<port>` (`DEFAULT_PORT`/`FALLBACK_PORT`), builds `redirect_uri = "http://127.0.0.1:{port}/auth/callback"`, opens the system browser to `{issuer}/oauth/authorize` with PKCE. Default issuer `pub(super) const DEFAULT_ISSUER: &str = "https://auth.openai.com";` (line 77).
- Code exchange POSTs to `{issuer}/oauth/token` via `OAuthClient::exchange_code` (https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/login/src/oauth/client.rs#L60-L71), returning `id_token`, `access_token`, `refresh_token`, written to `auth.json`.
- Client ID: `pub const CLIENT_ID: &str = "app_EMoamEEZ73f0CkXaXp7hrann";`, overridable via `CODEX_APP_SERVER_LOGIN_CLIENT_ID` — https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/login/src/auth/manager.rs.
- A local HTML success page confirms completion (https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/login/src/success_page.rs).

### `CODEX_HOME`
- `find_codex_home()` (https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/utils/home-dir/src/lib.rs#L1-L60): if `CODEX_HOME` is set, it must exist and be a canonicalizable directory or Codex errors; otherwise defaults to `~/.codex`.
- Docs: "Codex stores its local state under `CODEX_HOME`... Common files you may see there: `config.toml`... `auth.json` (if you use file-based credential storage) or your OS keychain/keyring; `history.jsonl`... logs and caches." — https://developers.openai.com/codex/config-advanced.
- `get_auth_file(codex_home)` → `codex_home.join("auth.json")` — https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/login/src/auth/storage.rs#L162-L163.

### `auth.json` / JWT parsing
`AuthDotJson` struct (https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/login/src/auth/manager.rs):
```rust
pub struct AuthDotJson {
    pub auth_mode: Option<AuthMode>,
    #[serde(rename = "OPENAI_API_KEY")]
    pub openai_api_key: Option<String>,
    pub tokens: Option<TokenData>,
    pub last_refresh: Option<DateTime<Utc>>,
    pub agent_identity: Option<AgentIdentityStorage>,
    pub personal_access_token: Option<String>,
    pub bedrock_api_key: Option<BedrockApiKeyAuth>,
    pub bedrock_access_keys: Option<BedrockAccessKeysAuth>,
}
```
`TokenData` (https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/login/src/token_data.rs#L1-L25): `id_token` and `access_token` are **both JWTs**; `refresh_token` is opaque; `account_id` also stored.

Claim parsing is real and load-bearing: `parse_chatgpt_jwt_claims()` (https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/login/src/token_data.rs#L172-L186) decodes the JWT payload (base64url, **no local signature verification** — trusts the issuer) and extracts a custom claim namespace `https://api.openai.com/auth` into `IdTokenInfo { email, chatgpt_plan_type, chatgpt_user_id, chatgpt_account_id, chatgpt_account_is_fedramp, raw_jwt }`. Only the raw JWT string is persisted; claims are re-derived on every load via custom serde hooks. `parse_jwt_expiration()` extracts the standard `exp` claim to drive refresh timing.

### Token refresh
- `should_refresh_proactively()` (https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/login/src/auth/manager.rs#L3001-L3023): refreshes if `exp` is within 5 min (`CHATGPT_ACCESS_TOKEN_REFRESH_WINDOW_MINUTES`), or if `exp` unparseable and `last_refresh` older than 8 days (`TOKEN_REFRESH_INTERVAL`).
- `request_chatgpt_token_refresh()` POSTs `grant_type=refresh_token` via `OAuthClient::refresh()` to `const REFRESH_TOKEN_URL: &str = "https://auth.openai.com/oauth/token";` (overridable via `CODEX_REFRESH_TOKEN_URL_OVERRIDE`). Revoke endpoint: `https://auth.openai.com/oauth/revoke`.
- `persist_tokens()` merges returned fields, sets `last_refresh`, and **writes back to `auth.json`/configured backend** on every successful refresh (https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/login/src/auth/manager.rs#L1599-L1621).
- Failure reasons (`RefreshTokenFailedReason::{Expired,Exhausted,Revoked,Other}`) map to user-facing "re-run `codex login`" messages.

### Hosts
- ChatGPT-auth API traffic: `pub const CHATGPT_CODEX_BASE_URL: &str = "https://chatgpt.com/backend-api/codex";` — https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/model-provider-info/src/lib.rs#L77.
- API-key traffic default: `https://api.openai.com/v1` — same file, line 434.
- OAuth/identity: `https://auth.openai.com` (`/oauth/authorize`, `/oauth/token`, `/oauth/revoke`) — https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/login/src/server.rs#L77.
- First-party ChatGPT hostnames for cookie/session handling: `chatgpt.com`, `chat.openai.com`, `chatgpt-staging.com` + subdomains — https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/http-client/src/chatgpt_hosts.rs#L1-L11.
- Realtime/voice sideband traffic may also go direct to `api.openai.com` (comment in `codex-rs/core/src/client.rs`).

### Headers
- Codex sends an `Authorization` header with a bearer token — https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/model-provider/src/bearer_auth_provider.rs#L29-L34.
- `ChatGPT-Account-ID: <account_id>` — https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/model-provider/src/bearer_auth_provider.rs#L35-L38.
- `X-OpenAI-Fedramp: true` for FedRAMP-routed accounts — https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/model-provider/src/bearer_auth_provider.rs#L39-L41.
- **No `OpenAI-Organization` header** found in current login/model-provider crates — only `ChatGPT-Account-ID` — this may differ from older/simpler Codex CLI releases (repo has grown to ~150 crates).

### Proxy / TLS
- **TLS backend: `rustls`, not native-tls/OpenSSL**, for the primary HTTP stack. https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/Cargo.toml: `reqwest = { version = "0.12", features = ["cookies"] }`, `rustls = "0.23.45"` (default-features off), `rustls-native-certs = "0.8.3"`, `rustls-pki-types`, `tokio-rustls`, plus a `"rustls-tls-native-roots"` reqwest feature and an internal `codex_utils_rustls_provider` crate. An `openssl-sys` dependency exists only for a narrow platform-specific purpose per an inline comment, not as the primary backend. https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/network-proxy/README.md: "HTTPS tunneling uses rustls via Rama's `rama-tls-rustls`; this avoids BoringSSL/OpenSSL symbol collisions."
- **Proxy support**: purpose-built `codex-http-client` crate with `OutboundProxyPolicy::RespectSystemProxy` (system settings + PAC/WPAD → env-var proxies → direct) and `OutboundProxyPolicy::ReqwestDefault` (reqwest's built-in env-proxy honoring) — https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/http-client/README.md. A separate `codex-network-proxy` crate runs its own **local HTTP proxy** (default `127.0.0.1:3128`) and **SOCKS5 proxy** (default `127.0.0.1:8081`) for sandbox network-policy enforcement; documents `HTTP_PROXY`/`HTTPS_PROXY`/`WS_PROXY`/`WSS_PROXY`/`ALL_PROXY=socks5h://...` — https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/network-proxy/README.md.
- **Custom/corporate CA — Codex-specific and purpose-built for MITM setups**: `CODEX_CA_CERTIFICATE` (takes precedence) falling back to `SSL_CERT_FILE` — https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/http-client/src/custom_ca.rs#L12-L63 (`pub const CODEX_CA_CERT_ENV: &str = "CODEX_CA_CERTIFICATE"; pub const SSL_CERT_FILE_ENV: &str = "SSL_CERT_FILE";`). Parses PEM bundles including OpenSSL-style `TRUSTED CERTIFICATE` blocks and CRLs; fails fast with actionable errors. No `REQUEST_CA_BUNDLE`-equivalent found.
- Programmatic strict-TLS path also exists: `HttpClientTlsConfig::with_root_certificate_pem` / `with_client_identity_pem` (https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/http-client/src/client_tls.rs) for explicit roots / mTLS identity.

### Unattended / CI / sandbox
- `codex exec "<prompt>"` for non-interactive mode; `--full-auto` is **deprecated** (prints a warning) — prefer `--sandbox workspace-write` — https://developers.openai.com/codex/noninteractive.
- `codex exec --sandbox {read-only|workspace-write|danger-full-access}`; default is read-only. `danger-full-access` recommended "only in a controlled environment."
- `--json` (JSONL event stream), `--output-schema`, `-o/--output-last-message`, `--ephemeral` (skip rollout persistence), `--ignore-user-config`, `--ignore-rules` (skip execpolicy rules) — for hermetic CI runs.
- CI auth typically via `CODEX_API_KEY`/`OPENAI_API_KEY` env var or `CODEX_ACCESS_TOKEN` (PAT) rather than interactive OAuth; `codex exec` reuses saved CLI credentials when present.
- **Sandboxing**: macOS uses Apple Seatbelt via `/usr/bin/sandbox-exec` (hardcoded path, deliberately avoiding PATH injection) — https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/core/README.md, https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/sandboxing/src/seatbelt.rs. Linux prefers **bubblewrap (`bwrap`)** on PATH, falling back to a bundled `codex-resources/bwrap` binary, or legacy Landlock (https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/codex-rs/linux-sandbox/src/landlock.rs); WSL2 supported, WSL1 explicitly rejected (can't create user namespaces). Windows has a native elevated/unelevated sandbox (`codex-rs/windows-sandbox-rs`). `CODEX_SANDBOX=seatbelt` env var set on Seatbelt children (repo `AGENTS.md`).
- Network-level policy (domain allowlists, MITM CA for HTTPS method restriction in "limited" mode) is layered separately via `codex-network-proxy` on top of process sandboxing.

### Global `AGENTS.md`
Confirmed: Codex builds an instruction chain — **1. Global scope**: reads `$CODEX_HOME/AGENTS.override.md` if present, else `$CODEX_HOME/AGENTS.md` (only the first non-empty file at this level); **2. Project scope**: walks from project root down to cwd; **3. Merge order**: concatenates root-down, closer files override earlier guidance — https://developers.openai.com/codex/guides/agents-md. Size cap `project_doc_max_bytes` (32 KiB default); configurable fallback filenames via `project_doc_fallback_filenames`. Repo stub confirms: https://github.com/openai/codex/blob/bcd6d9ab6b9f26f85d76d0c680b3f88b367bffa0/docs/agents_md.md.

---

## 3. OpenCode

Current canonical repo: **`github.com/anomalyco/opencode`** (formerly `sst/opencode`; `sst/opencode` HTTP 301-redirects there; `api.github.com/repos/anomalyco/opencode` confirms `archived: false`, latest release `v1.18.33`, 2026-09-28). The unrelated, Go-based **`github.com/opencode-ai/opencode` is archived**, its README stating: "Archived: Project has Moved... continued under the name **Crush**... developed by the original author and the Charm team" (successor: `github.com/charmbracelet/crush`). All citations below are pinned to `anomalyco/opencode` commit `2fa3363c924c5c3e367b84a87ae478296a0ed59b`.

### Auth methods
- Provider API keys via `opencode auth login` (interactive) or in-TUI `/connect` — https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/web/src/content/docs/cli.mdx#L140-L158.
- Native **OAuth** support in the credential schema — `Auth.Info` is a discriminated union of `oauth` (`refresh`, `access`, `expires`, optional `accountId`/`enterpriseUrl`), `api` (`key` + metadata), `wellknown` (`key` + `token`) — https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/auth/index.ts#L12-L31.
- Env vars / `.env` files are read for provider keys (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, etc.) — `cli.mdx:~150`; confirmed also in the GitHub Actions example (`ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}` as an env var) — https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/web/src/content/docs/github.mdx#L55-L58.
- 75+ providers via AI SDK + Models.dev — https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/web/src/content/docs/providers.mdx#L1-L9.
- Separate MCP-server OAuth: `opencode mcp auth [name]` — https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/web/src/content/docs/cli.mdx#L262-L296.
- Undocumented (source-only) override: `OPENCODE_AUTH_CONTENT` env var injects the entire `auth.json` content inline (https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/auth/index.ts#L57-L61) — not found in docs site content at this commit.

### Storage
- `~/.local/share/opencode/auth.json`, JSON keyed by normalized provider ID — https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/web/src/content/docs/providers.mdx#L17-L20, https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/web/src/content/docs/cli.mdx#L142, https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/auth/index.ts#L9.
- Written with explicit `0o600` permissions (https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/auth/index.ts#L76-L82).

### Claude Pro/Max status
- Native "Claude Pro/Max" OAuth flow exists in-product but is a **documented ToS/ban risk**, evidenced by live issues on the official repo (not third-party speculation):
  - Issue #18315, "Claude Pro/Max auth flow returns Invalid token" (closed, v1.2.27) — reporter describes the Claude Pro/Max OAuth flow breaking with "Invalid Token"/"Failed refresh token" — https://github.com/anomalyco/opencode/issues/18315.
  - Issue #6930, "Using opencode with Anthropic OAuth violates ToS & Results in Ban" (closed, v1.0.223) — reporter's Anthropic account was banned after using the OAuth login "suggested on Open Code's website"; states Anthropic engineers confirmed the ToS violation — https://github.com/anomalyco/opencode/issues/6930.
- The generic `Oauth` schema variant (`refresh`/`access`/`expires`/`accountId`/`enterpriseUrl`) supports this, but the actual Anthropic OAuth client/PKCE implementation was **not found hardcoded** in the repo via code search — likely delivered dynamically via Models.dev provider metadata (**inference, not directly verified**).

### Config
Formats: `opencode.json` / `opencode.jsonc` — https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/web/src/content/docs/config.mdx#L9-L21. Precedence (later overrides earlier, merged not replaced):
1. Remote `.well-known/opencode` org defaults
2. Global: `~/.config/opencode/opencode.json`
3. `OPENCODE_CONFIG` env var (custom path)
4. Project `opencode.json` (nearest `.git` ancestor)
5. `.opencode/{agents,commands,modes,plugins,skills,tools,themes}/`
6. `OPENCODE_CONFIG_CONTENT` env var (inline runtime override)
7. macOS managed config `/Library/Application Support/opencode/`
8. macOS MDM `.mobileconfig` (highest)
— https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/web/src/content/docs/config.mdx#L33-L58. TUI-specific settings live in a separate `tui.json`.

### Proxy / TLS / runtime
- "OpenCode respects standard proxy environment variables": `HTTPS_PROXY` (recommended), `HTTP_PROXY` (fallback), `NO_PROXY` (required to bypass proxy for local TUI↔server connection, e.g. `NO_PROXY=localhost,127.0.0.1`, "to prevent routing loops"). Proxy basic-auth via URL-embedded credentials; **NTLM/Kerberos not supported directly** — docs recommend routing through an LLM Gateway instead — https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/web/src/content/docs/network.mdx.
- Custom CA: `export NODE_EXTRA_CA_CERTS=/path/to/ca-cert.pem`, documented to work "for both proxy connections and direct API access" — https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/web/src/content/docs/network.mdx.
- **Runtime: Bun** (not Node/Go) — https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/package.json#L8-L16 (`bun run ./src/index.ts`, `bun test`, `bun run script/build.ts`). Partial Node compatibility exists in the storage layer via an `imports` map (`#db` → `db.bun.ts` / `db.node.ts`), but Bun is the canonical dev/build/runtime target. The install script downloads a **prebuilt standalone binary** per OS/arch — no separate JS runtime required for end users.
- TLS-trust implication: because the standalone binary targets the Bun/Node family and documents `NODE_EXTRA_CA_CERTS`, CA trust follows Node-style semantics (bundled root store + `NODE_EXTRA_CA_CERTS` addition).

### Headless / automation run modes
- `opencode run [message..]` — non-interactive, scriptable; `--format json`, `--attach <url>`, `--model`, `--agent`, `--file`, `--auto` — https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/web/src/content/docs/cli.mdx#L339-L386.
- `opencode serve` — "Start a **headless** OpenCode server for API access"; `OPENCODE_SERVER_PASSWORD` enables HTTP Basic auth (username default `opencode`, override `OPENCODE_SERVER_USERNAME`) — https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/web/src/content/docs/cli.mdx#L388-L409.
- `opencode web` — headless server + web UI (https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/web/src/content/docs/cli.mdx#L501-L503); `opencode attach [url]` attaches a TUI to a running `serve`/`web` backend.
- **GitHub Actions**: `opencode github run` (typical in CI) + `opencode github install` for PR/issue triage — https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/web/src/content/docs/cli.mdx#L207-L223.

### Docker/install patterns
- Install script downloads a version-pinned binary to `$OPENCODE_INSTALL_DIR` → `$XDG_BIN_DIR` → `$HOME/bin` → `$HOME/.opencode/bin` — README:85-97.
- No official end-user "run OpenCode in Docker" image; `packages/containers/` only hosts **CI build containers** (`base`, `bun-node`, `rust`, `tauri-linux`, `publish`) to speed GitHub Actions jobs — https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/containers/README.md.
- Canonical CI automation env pattern:
  ```yaml
  - name: Run OpenCode
    uses: anomalyco/opencode/github@latest
    env:
      ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
    with:
      model: anthropic/claude-sonnet-4-20250514
  ```
  — https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/web/src/content/docs/github.mdx#L24-L60. Also supports `use_github_token: true` (no separate GitHub App install) and an `oidc_base_url` (default `https://api.opencode.ai`) for OIDC install-token exchange.

---

## 4. GitHub / `gh` / `git`

Source: `docs.github.com/*`, `git-scm.com/docs/*`, RFC 8628, `cli/cli` @ `fc4b137cdef0a6bd28fd461b7cf9c84a5812a8cd`, `cli/go-gh` @ `859e813416733b742d6fc21df59a2058e04f0f38`, GitHub engineering blog. Fetched/checked 2026-09-30.

### Device flow
RFC 8628 (device authorization grant): client requests a device code → server returns device code + user code + verification URI → user visits URI on another device, enters code → client polls token endpoint → token issued on approval. https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#device-flow: GitHub's device flow is "used for headless apps, such as CLI tools."

In `gh`: `authflow.AuthFlow` builds an `oauth.Flow` and calls `flow.DetectFlow()` (https://github.com/cli/cli/blob/fc4b137cdef0a6bd28fd461b7cf9c84a5812a8cd/internal/authflow/flow.go#L28-L99); `DisplayCode` prints/clipboard-copies the user code, `BrowseURL` opens the verification URL (lines 44-77). `gh`'s hardcoded OAuth app client ID: `"178c6fc778ccc68e1d6a"` (line 22) — i.e. `gh` authenticates as GitHub's own published "GitHub CLI" OAuth app.

### Scopes
Default (device/web) scopes: `repo`, `read:org`, `gist` — https://github.com/cli/cli/blob/fc4b137cdef0a6bd28fd461b7cf9c84a5812a8cd/internal/authflow/flow.go#L39. `admin:public_key` added if the user opts into git-credential caching / uploading a new SSH key (https://github.com/cli/cli/blob/fc4b137cdef0a6bd28fd461b7cf9c84a5812a8cd/pkg/cmd/auth/shared/login_flow.go#L128-L129). Manual `--with-token` paste requires the same minimum `repo, read:org, gist` (https://github.com/cli/cli/blob/fc4b137cdef0a6bd28fd461b7cf9c84a5812a8cd/pkg/cmd/auth/shared/login_flow.go#L159-L160, https://cli.github.com/manual/gh_auth_login). Scope reference table (`repo`, `repo:status`, `repo_deployment`, `public_repo`, `repo:invite`, `security_events`, `admin:org`/`write:org`/`read:org`, `admin:public_key`/`write:public_key`, `gist`, `workflow`, etc.) — https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps.

### Token types/prefixes and lifetimes
| Credential type | Prefix | Lifespan | Associated with |
|---|---|---|---|
| Classic PAT | `ghp_` | Long-lived (custom or none); auto-revoked after 1yr unused | User |
| Fine-grained PAT | `github_pat_` | Configurable, up to 1 year or none (org may cap) | User |
| OAuth app token | `gho_` | Long-lived by default; optional 8h expiry + `offline_access` refresh (6mo) | User |
| GitHub App user token | `ghu_` | Short-lived (8h) | User |
| GitHub App installation token | `ghs_` | Short-lived (1h) | App installation |
| GitHub App refresh token | `ghr_` | Long-lived (6mo) | User |
| Actions `GITHUB_TOKEN` | — | Job duration | Workflow run |

Source: https://docs.github.com/en/organizations/managing-programmatic-access-to-your-organization/github-credential-types. Prefix/checksum design rationale: https://github.blog/engineering/platform-security/behind-githubs-new-authentication-token-formats/. Auto-revocation after 1yr unused / on expiry date: https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens, https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/token-expiration-and-revocation. Fine-grained PAT org max-lifetime policy: https://docs.github.com/en/organizations/managing-programmatic-access-to-your-organization/setting-a-personal-access-token-policy-for-your-organization#enforcing-a-maximum-lifetime-policy-for-personal-access-tokens. **`gh`'s own OAuth app requests only `repo, read:org, gist` (no `offline_access`), so `gh auth login` issues non-expiring `gho_` tokens** unless GitHub globally flips expiring-tokens on for that app.

### Storage & precedence
Default storage: OS credential store/keyring (macOS Keychain, Windows Credential Manager, Linux secret service), falling back to plaintext `hosts.yml` if unavailable — https://github.com/cli/cli/blob/fc4b137cdef0a6bd28fd461b7cf9c84a5812a8cd/pkg/cmd/auth/login/login.go#L69-L72, https://cli.github.com/manual/gh_auth_login. Config dir resolution: `GH_CONFIG_DIR` → `XDG_CONFIG_HOME` → `AppData` (Windows) → `HOME`, default `~/.config/gh`; hosts file = `ConfigDir()/hosts.yml` — https://github.com/cli/go-gh/blob/859e813416733b742d6fc21df59a2058e04f0f38/pkg/config/config.go#L212-L229. Example on-disk entry: `github.com: {api_host: ..., users: {octocat: {oauth_token: gho_...}}}` — https://github.com/cli/cli/blob/fc4b137cdef0a6bd28fd461b7cf9c84a5812a8cd/docs/api-host.md#L8-L13.

**Precedence** (https://github.com/cli/go-gh/blob/859e813416733b742d6fc21df59a2058e04f0f38/pkg/auth/auth.go#L66-L95, `tokenForHost`): for github.com/tenancy hosts, `GH_TOKEN` > `GITHUB_TOKEN` > `hosts.yml` `oauth_token`; for GHE hosts, `GH_ENTERPRISE_TOKEN` > `GITHUB_ENTERPRISE_TOKEN` > config file. If `GH_TOKEN`/`GITHUB_TOKEN` is set, `gh auth login` **refuses** to write to config, printing a warning to `unset` the var first — https://github.com/cli/cli/blob/fc4b137cdef0a6bd28fd461b7cf9c84a5812a8cd/pkg/cmd/auth/login/login.go#L186-L190.

### Git HTTPS auth mechanics
- Basic auth with PAT as password: "When Git prompts you for your password, enter your personal access token" — https://docs.github.com/en/get-started/git-basics/about-remote-repositories.
- `credential.helper` — external programs invoked with `get`/`store`/`erase` over the `git-credential` plumbing protocol — https://git-scm.com/docs/gitcredentials. Plaintext `store` backend persists `https://<user>:<token>@host` lines in `.git-credentials` — https://git-scm.com/docs/git-credential-store.
- URL rewriting: `url.<base>.insteadOf` and header injection: `http.extraHeader` — both documented git-config keys, https://git-scm.com/docs/git-config. Standard token-injection pattern: `git config url."https://<TOKEN>@github.com/".insteadOf "https://github.com/"`.
- GitHub also recommends **Git Credential Manager (GCM)** for transparent OAuth/2FA handling with OS-keychain storage — https://docs.github.com/en/get-started/git-basics/caching-your-github-credentials-in-git.

### `gh` ↔ git credential helper
`gh auth setup-git` explicitly wires git: sets `credential.<host>.helper` to `!<gh> auth git-credential`, first clearing any pre-existing chain — https://github.com/cli/cli/blob/fc4b137cdef0a6bd28fd461b7cf9c84a5812a8cd/pkg/cmd/auth/shared/gitcredentials/helper_config.go#L22-L58; invoked automatically (with a prompt) during `gh auth login` when HTTPS is selected — https://github.com/cli/cli/blob/fc4b137cdef0a6bd28fd461b7cf9c84a5812a8cd/pkg/cmd/auth/shared/login_flow.go#L80-L220. `ConfiguredHelper`/`IsOurs` detect existing config by basename match (`gh`, minus `.exe`) — same file, lines 79-99.

### Header/token validation
"In most cases, you can use `Authorization: Bearer` or `Authorization: token`... if you are passing a JSON web token (JWT), you must use `Authorization: Bearer`." — https://docs.github.com/en/rest/authentication/authenticating-to-the-rest-api. `token` remains accepted for backward compatibility.

### Origin/IP restrictions
No IP/origin-based validation was found in docs.github.com/cli.cli/git-scm.com for PATs/OAuth tokens. Restrictions found are ownership/scope-based: fine-grained PATs are scoped to a single resource owner + optional repo list, and orgs can require **approval** before non-public access is granted (https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens); orgs/enterprises can set classic-PAT access policies and max-lifetime policies; SAML-SSO orgs require explicit per-token authorization for classic PATs. **Gap:** GHEC's separate org/enterprise **IP-allowlist** feature was not directly verified in this pass (`docs.github.com/en/organizations/keeping-your-organization-secure/.../managing-allowed-ip-addresses-for-your-organization`) — flagged as a follow-up if IP-based conditional access needs confirming.

---

## 5. TLS interception across runtimes

Source: `nodejs/node` docs pinned v22.9.0; `oven-sh/bun` main (`624891b`); `psf/requests` main + PyPI `certifi`; `golang/go` pinned `go1.23.0`; `rustls/rustls-native-certs` + `rustls/webpki-roots` READMEs; https://git-scm.com/docs/git-config; `curl/curl` docs pinned `curl-8_11_0`; `curl.se/docs/manpage.html`; Debian/Alpine update-ca-certificates: https://manpages.debian.org/bookworm/ca-certificates/update-ca-certificates.8.en.html and https://man.archlinux.org/man/update-ca-certificates.8.en.

| Tool/Runtime | Default trust store | Env var / config for custom CA | Notes / gotchas |
|---|---|---|---|
| **Node.js** | Bundled Mozilla NSS snapshot fixed at release, identical across platforms — NOT OS store (https://nodejs.org/download/release/v22.9.0/docs/api/tls.html#tlsrootcertificates) | `NODE_EXTRA_CA_CERTS=file` (adds to bundle) — https://nodejs.org/download/release/v22.9.0/docs/api/cli.html#node_extra_ca_certsfile | Read once at process start only (not env-mutable at runtime); ignored under setuid-root; fully bypassed if code explicitly passes a `ca` option |
| **Bun** | Bundled Mozilla/CCADB roots by default, like Node (https://github.com/oven-sh/bun/blob/624891b/packages/bun-usockets/src/crypto/root_certs.cpp) | `NODE_EXTRA_CA_CERTS` (confirmed via tests + https://github.com/oven-sh/bun/blob/624891b/src/runtime/socket/cert_files.rs#L27-L38); `NODE_USE_SYSTEM_CA=1` / `--use-system-ca` switches to OS store (https://github.com/oven-sh/bun/blob/624891b/packages/bun-usockets/src/crypto/root_certs.cpp#L62-L67); `install.ca`/`install.cafile` in `bunfig.toml` for `bun install` only | `NODE_USE_SYSTEM_CA` verified only in source/tests, not in public bun.sh prose docs as of check date |
| **Python (`requests`/`certifi`)** | Bundled `certifi` Mozilla snapshot, independent of OS/stdlib `ssl` (https://pypi.org/project/certifi/) | `REQUESTS_CA_BUNDLE` (checked first) → `CURL_CA_BUNDLE` fallback (https://github.com/psf/requests/blob/main/src/requests/sessions.py#L855-L860), only when `verify` truthy/None and `trust_env` set | stdlib `ssl.create_default_context()` is a **separate** trust path that can use the OS store — an OS-trusted CA is invisible to `requests` unless also set via these env vars |
| **Go (`crypto/x509`)** | OS trust store, platform-specific: Linux/Unix file/dir probe list (`/etc/ssl/certs/ca-certificates.crt` Debian, `/etc/ssl/cert.pem` Alpine — https://github.com/golang/go/blob/go1.23.0/src/crypto/x509/root_linux.go#L9-L16); native Keychain/CryptoAPI on macOS/Windows | `SSL_CERT_FILE` (single PEM, overrides all defaults), `SSL_CERT_DIR` (colon-separated, Unix only) — https://github.com/golang/go/blob/go1.23.0/src/crypto/x509/root_unix.go#L18-L61 | Simplest to redirect for a MITM CA: set `SSL_CERT_FILE` and it fully overrides system defaults on Unix |
| **Rust — bare `rustls`** | None — no roots without an add-on crate | N/A directly | Requires `rustls-native-certs` or `webpki-roots` |
| **Rust — `rustls-native-certs`** | OS store (checks `SSL_CERT_FILE` first, then Windows schannel/`security-framework` Keychain merge/`openssl-probe` on Linux) | `SSL_CERT_FILE` honored first — https://github.com/rustls/rustls-native-certs | Recommended add-on for CLIs that must honor corporate MITM CAs at runtime |
| **Rust — `webpki-roots`** | Mozilla/CCADB snapshot **compiled in** at build time, no filesystem/OS interaction | **None at runtime** | **Critical gotcha**: a binary built with `webpki-roots` cannot pick up any runtime-injected CA (OS store, `SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`) — CA must be embedded at build time or the binary recompiled. Whether a given Rust CLI release uses this vs. `rustls-native-certs` vs. `native-tls` is a **per-binary build-time choice**, not discoverable from rustls docs alone — must inspect that binary's `Cargo.toml`/build features (Codex CLI ships both `rustls` and `rustls-native-certs` per its `Cargo.toml`, so it is in the "runtime-configurable" camp, not the frozen-`webpki-roots` camp) |
| **Rust — `native-tls`/`openssl` crate** | OS store, delegating to system OpenSSL/Secure Transport/Schannel | Same env vars as that OS's OpenSSL/curl backend | Behaves like curl/git-on-OpenSSL |
| **git** | Depends on linked libcurl backend: OpenSSL (Linux, system bundle) vs Secure Transport (macOS Keychain) vs Schannel (Windows cert store) | `http.sslCAInfo`/`GIT_SSL_CAINFO`, `http.sslCAPath`/`GIT_SSL_CAPATH`, `http.sslBackend` — https://git-scm.com/docs/git-config; only effective on OpenSSL/GnuTLS backends | On stock macOS git (Secure-Transport-linked), `http.sslCAInfo`/env override is effectively inert — CA must go into the macOS Keychain instead (inferred by combining git-scm docs with curl's Secure-Transport docs; not stated by git-scm.com itself) |
| **curl** | Depends on TLS backend: OpenSSL/GnuTLS/wolfSSL (flat file/dir bundle) vs Secure Transport (macOS Keychain) vs Schannel (Windows cert store) | `--cacert`/`CURL_CA_BUNDLE`, `--capath`/`SSL_CERT_DIR`, `SSL_CERT_FILE` — https://github.com/curl/curl/blob/curl-8_11_0/docs/cmdline-opts/cacert.md and https://curl.se/docs/manpage.html | **All ignored when built with Schannel** (Windows); `CURL_SSL_BACKEND` selects among MultiSSL-compiled backends at runtime |
| **Docker/Debian/Alpine** | `/etc/ssl/certs/ca-certificates.crt` (Debian) managed by `ca-certificates` package; `/etc/ssl/cert.pem` (Alpine) | Drop `.crt` into `/usr/local/share/ca-certificates/`, run `update-ca-certificates` — Debian/Alpine update-ca-certificates: https://manpages.debian.org/bookworm/ca-certificates/update-ca-certificates.8.en.html and https://man.archlinux.org/man/update-ca-certificates.8.en | Fixes the OS-level bundle; only helps tools that read the OS store (Go, curl/OpenSSL, git/OpenSSL) — does **not** help Node.js, Bun (bundled mode), Python `requests`/`certifi`, or `webpki-roots`-based Rust binaries, which each need their own env var/config as above |

**Which agent tools need special handling for MITM interception (synthesis):**
- **Claude Code** (Node/npm or native): set `NODE_EXTRA_CA_CERTS` (works on both installer types per its own docs); optionally `CLAUDE_CODE_CERT_STORE=bundled,system` to also trust OS store. Watch for the "cloud/Desktop-managed session ignores env" gotcha.
- **Codex CLI** (Rust/rustls + rustls-native-certs): set `CODEX_CA_CERTIFICATE` (Codex-specific, preferred) or `SSL_CERT_FILE` (fallback, also picked up by `rustls-native-certs`). Since it ships `rustls-native-certs`, it is **not** frozen like a bare-`webpki-roots` binary.
- **OpenCode** (Bun-compiled binary): set `NODE_EXTRA_CA_CERTS` (documented explicitly for both proxy and direct API paths).
- **`gh`/`git`**: on Linux, `GIT_SSL_CAINFO`/`SSL_CERT_FILE` work (OpenSSL-linked); on macOS, inject into the system/login Keychain instead, since Apple's git build uses Secure Transport and ignores CA-file env vars. `gh` itself uses Go's `crypto/x509`, so `SSL_CERT_FILE` should work for `gh`'s own API calls, independent of git's own libcurl TLS path.
- **curl** (used as a bootstrap/installer in many container base images): `CURL_CA_BUNDLE`/`SSL_CERT_FILE`/`--cacert` work only on non-Schannel builds; check the base image's curl TLS backend.

---

## Implications for Hibernaut — Tool × Auth Method Matrix

| Tool | Auth method | Placeholder-swap works?* | Credential-file sync? | Hosts | Headers/encodings | Image env/config | Gotchas |
|---|---|---|---|---|---|---|---|
| **Claude Code** | `ANTHROPIC_API_KEY` | **Yes** — static, no session state | Not needed (single env var) | `api.anthropic.com` (+ Bedrock/Vertex/AWS variants) | `X-Api-Key` | `ANTHROPIC_API_KEY`, `NODE_EXTRA_CA_CERTS`, `HTTPS_PROXY`/`NO_PROXY` | Overrides an existing OAuth login automatically; in `-p` mode always wins, no confirmation |
| Claude Code | claude.ai OAuth (`/login`) | **No** — bound to local callback/keychain, refresh flow, and (per docs) 3rd-party relay of claude.ai login is discouraged by policy | Credential file path/keychain entry **not confirmed from primary docs** (gap) | same + OAuth/auth domains (unconfirmed exhaustive list) | Bearer via internal OAuth flow | `CLAUDE_CONFIG_DIR` for multi-account isolation | WSL2/SSH/container fallback to paste-code flow is itself evidence Anthropic expects this in sandboxed contexts, but recommend API-key path per Agent SDK policy note |
| Claude Code | `setup-token` → `CLAUDE_CODE_OAUTH_TOKEN` | **Partial** — long-lived bearer-ish token, generated once outside the sandbox, then injected as env var; not a live session | Not needed (single env var, but must be generated by the user externally first) | same | `Authorization`-style (via env, not doc-detailed) | `CLAUDE_CODE_OAUTH_TOKEN` | Tied to Pro/Max subscription seat, not API billing; lifetime unconfirmed (~1yr community estimate) |
| Claude Code | `ANTHROPIC_AUTH_TOKEN` (gateway) | **Yes** | Not needed | Custom via `ANTHROPIC_BASE_URL` | `Authorization` header value | `ANTHROPIC_AUTH_TOKEN` + `ANTHROPIC_BASE_URL` | Best fit if Hibernaut fronts a corporate LLM gateway |
| **Codex CLI** | `CODEX_API_KEY` / `OPENAI_API_KEY` | **Yes** — highest env precedence | Not needed | `api.openai.com/v1` | `Authorization: Bearer` | `CODEX_API_KEY`, `CODEX_CA_CERTIFICATE`/`SSL_CERT_FILE`, `HTTPS_PROXY` | Clean, well-documented; preferred automation path |
| Codex CLI | ChatGPT OAuth (`codex login`) | **No** — local-callback+PKCE, JWT refresh cycle, tied to `auth.openai.com` | **Yes, well-defined**: single `$CODEX_HOME/auth.json` file, JSON, documented schema | `chatgpt.com/backend-api/codex`, `auth.openai.com` | `Authorization: Bearer` + `ChatGPT-Account-ID` | `CODEX_HOME` (redirect to shared/mounted dir) | `auth.json` is copyable/mountable across environments since it's a flat file — best candidate among all 5 tools for literal credential-file sync if OAuth is required |
| Codex CLI | `CODEX_ACCESS_TOKEN` (PAT/Agent Identity JWT) | **Yes** — explicitly designed for CI/workload identity | Not needed | same | `Authorization: Bearer` | `CODEX_ACCESS_TOKEN` | Purpose-built for exactly Hibernaut's use case (unattended, workload identity) |
| **OpenCode** | Provider API key (`ANTHROPIC_API_KEY` etc., via env/`.env`) | **Yes** | Not needed | Per-provider (e.g. `api.anthropic.com`) | Per-provider native header | env var / `.env` in project | Simplest, most portable path |
| OpenCode | OAuth (incl. "Claude Pro/Max") | **No / risky** — documented ToS violations + account bans on official repo | **Yes**, single file: `~/.local/share/opencode/auth.json` (JSON, mode 0600) | Per-provider OAuth issuer (Anthropic's) | Bearer-style, per schema | `OPENCODE_AUTH_CONTENT` (inline JSON override, source-only) | **Avoid** — real ban risk documented in opencode issues #6930/#18315 |
| OpenCode | API/wellknown key stored in `auth.json` | **Yes**, copy the file or set `OPENCODE_AUTH_CONTENT` | **Yes** | Per-provider | Per-provider | `~/.local/share/opencode/auth.json`, `NODE_EXTRA_CA_CERTS`, `HTTPS_PROXY` | Bun binary — CA trust follows Node-style semantics |
| **`gh`/git** | `GH_TOKEN`/`GITHUB_TOKEN` env var | **Yes** — top precedence, bypasses config entirely | Not needed | `api.github.com`, `github.com` | `Authorization: Bearer` or `token` | `GH_TOKEN` | `gh` refuses to persist to `hosts.yml` while these are set — safe for ephemeral containers |
| `gh`/git | stored OAuth (`hosts.yml`/keyring) | **Partial** — file/keyring copy works but not "swap in a placeholder" | **Yes**: `~/.config/gh/hosts.yml` (plaintext fallback) or OS keyring (not file-copyable) | same | `Authorization` | `GH_CONFIG_DIR` | If real deployment uses OS keyring (not plaintext), file-sync won't work — must force plaintext fallback or use env var instead |
| git (plain, no `gh`) | PAT via Basic auth / `insteadOf` / `extraHeader` | **Yes** | `.git-credentials` (plaintext) or `http.extraHeader` in repo/global config | `github.com` | Basic (`user:token`) or custom header | `url."https://TOKEN@github.com/".insteadOf`, `.gitconfig` | Straightforward for git-only automation; independent of `gh` |

*"Placeholder-swap" = can a static token/env-var be substituted for a live session without touching a stateful credential/session file.

### Recommended v1 auth path for Hibernaut

1. **Prefer static, non-refreshing credentials wherever a tool offers one**, since these need no session persistence, no proxy-aware OAuth callback, and no refresh-token lifecycle inside an ephemeral sandbox:
   - Claude Code → `ANTHROPIC_API_KEY` (or `ANTHROPIC_AUTH_TOKEN` if fronting a gateway).
   - Codex CLI → `CODEX_API_KEY` or, if CI/workload-identity semantics are wanted, `CODEX_ACCESS_TOKEN` (this is explicitly the tool's own recommended automation path).
   - OpenCode → provider API key via env var/`.env`, **never** the Claude Pro/Max OAuth flow (documented ban risk).
   - `gh`/git → `GH_TOKEN` (or `GITHUB_TOKEN`), a scoped fine-grained PAT if possible; for plain git, `http.extraHeader`/`insteadOf` URL rewriting with the same token, avoiding a stored credential file.
2. **Where a subscription/OAuth login is unavoidable** (e.g. user specifically wants Claude Pro/Max or ChatGPT Plus billing instead of API billing):
   - Claude Code: use `claude setup-token` **generated once outside the sandbox** to mint `CLAUDE_CODE_OAUTH_TOKEN`, injected as an env var per session — avoids needing to sync/refresh a credentials file inside the sandbox, and matches Anthropic's own stated intent for that command.
   - Codex CLI: `$CODEX_HOME/auth.json` is a well-documented, single flat JSON file with self-contained refresh logic — this is the most viable *file-sync* candidate among all tools if OAuth must persist across container runs (mount/copy `auth.json`; Codex CLI will refresh and rewrite it in place).
   - OpenCode: avoid Anthropic-OAuth login entirely; if a provider's own OAuth is required, sync `~/.local/share/opencode/auth.json` (documented schema, `0600`) or set `OPENCODE_AUTH_CONTENT` at container start.
3. **TLS interception**: standardize the sandbox image on injecting a CA via **both** paths simultaneously, since no single mechanism covers every tool:
   - `NODE_EXTRA_CA_CERTS=/etc/hibernaut/ca.pem` — covers Claude Code, OpenCode (Bun), any Node-based tooling.
   - `SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt` (the full system bundle *after* adding the Hibernaut CA, **not** the Hibernaut CA alone) — covers Codex CLI (via its `rustls-native-certs` dependency and explicit fallback), Go tools, curl/git on OpenSSL-linked Linux builds. Per the Go row above, `SSL_CERT_FILE` *overrides all defaults*, so pointing it at a file holding only the MITM CA would break every non-intercepted TLS connection. *(Correction added during spec review.)*
   - `CODEX_CA_CERTIFICATE=/etc/ssl/certs/ca-certificates.crt` — Codex's own preferred variable, set it too for certainty (same full-bundle rule). The same applies to `REQUESTS_CA_BUNDLE`, which replaces `certifi`'s roots.
   - Run `update-ca-certificates` after placing the CA in `/usr/local/share/ca-certificates/` in the base image, to cover any tool relying on the OS bundle directly (Go, curl/git on OpenSSL).
   - If the base image is Debian/Ubuntu (not macOS), the git/curl Secure-Transport caveat does not apply — the OS-bundle injection above is sufficient for git/curl.
   - Set `HTTPS_PROXY`/`HTTP_PROXY`/`NO_PROXY` uniformly; explicitly confirm Claude Code's "cloud/Desktop-managed session ignores env" exception does not apply to Hibernaut's execution mode (verify empirically — flagged gap above).
4. **Do not attempt SOCKS proxying for Claude Code** (explicitly unsupported); use an HTTP(S) proxy front for any interception layer that must support all five tools uniformly.
5. Treat all "gap" items flagged above (exact Claude Code credential file path/TTLs, exhaustive host lists, AUP specifics, GHEC IP-allowlisting) as items for a follow-up empirical-verification pass before finalizing production credential-handling code.

---

## Open gaps requiring follow-up (do not treat as resolved)

- Claude Code: exact on-disk credential file path/schema and Keychain service name; `apiKeyHelper` TTL default; `setup-token` exact lifetime; exhaustive production hostname list beyond the devcontainer firewall script; literal root-usage error string; AUP Help Center article specifics (`support.anthropic.com/en/articles/12005017`) not fetched.
- Codex CLI: device-code auth path (`codex-rs/login/src/device_code_auth.rs`) not fully traced.
- OpenCode: Anthropic OAuth client/PKCE implementation location not found in source (inferred to be Models.dev-delivered).
- GitHub: GHEC IP-allowlist feature not directly verified.
- TLS: Node's historical `--use-openssl-ca` build flag not found in current docs (likely obsolete); `GODEBUG=x509ignoreCN` not confirmed relevant to trust-store selection; git-on-macOS Secure-Transport behavior is a strong inference, not a git-scm.com direct statement; per-binary rustls feature selection (`webpki-roots` vs `rustls-native-certs`) must be verified per release for any Rust tool not already source-confirmed here.
