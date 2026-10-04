# GitHub Copilot CLI auth for Hibernaut

**Date checked:** 2026-09-30. **Live trace:** 2026-10-04, Copilot CLI 1.0.91 (§9).

**Update (2026-10-04):** the live trace in §9 answers the main open question. Copilot CLI 1.0.91
does no token exchange. It sends the `GH_TOKEN` value itself to `api.github.com` and to the
plan's Copilot API host, and a placeholder swapped on those hosts works.

**Summary:** GitHub Copilot CLI can run headlessly from `GH_TOKEN`/`COPILOT_GITHUB_TOKEN`, and GitHub's docs explicitly list OAuth tokens from the GitHub CLI app (`gho_`) as supported. The CLI stores interactive OAuth tokens in the OS keychain, or plaintext `~/.copilot/config.json` on headless Linux without a credential store, but env-var auth is documented as the automation path. The npm package is a thin Node loader plus per-platform native executable; the binary contains Node/undici/SEA strings and honors Copilot's documented proxy/custom-CA paths. Official docs list `api.github.com/copilot_internal/*` and `*.githubcopilot.com`/plan-specific Copilot hosts as required, but I did **not** verify the CLI's exact token-exchange response format or cache from readable bundled code. OpenCode is officially supported by GitHub Copilot, but the pinned OpenCode source uses its own device-flow OAuth app and sends the stored OAuth token directly to `api.githubcopilot.com`.

## Sources and pinned artifacts

- **Verified** — GitHub Copilot CLI docs were read from `docs.github.com`: install, auth, command reference, config-dir reference, allowlist reference, network settings, and GitHub Actions usage. Key URLs: <https://docs.github.com/en/copilot/how-tos/copilot-cli/set-up-copilot-cli/install-copilot-cli>, <https://docs.github.com/en/copilot/how-tos/copilot-cli/set-up-copilot-cli/authenticate-copilot-cli>, <https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference>, <https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-config-dir-reference>, <https://docs.github.com/en/copilot/reference/copilot-allowlist-reference>, <https://docs.github.com/en/copilot/concepts/security-governance-and-network-settings/network-settings>, <https://docs.github.com/en/copilot/concepts/agents/copilot-cli/copilot-cli-in-github-actions>, and <https://docs.github.com/en/copilot/how-tos/copilot-cli/use-copilot-cli-in-actions>.
- **Verified** — Published npm package `@github/copilot@1.0.89` was inspected with `npm pack`; `package/package.json` says `version: 1.0.89`, `bin.copilot: npm-loader.js`, `buildMetadata.gitCommit: 0b434d20`, and optional platform packages `@github/copilot-{linux,linuxmusl,darwin,win32}-{x64,arm64}` (`package/package.json:2-4,16-18,26-38`). The wrapper imports `detect-libc`, resolves the platform package, and `spawnSync`s its `copilot` executable (`package/npm-loader.js:7-8`). Tarball shasums: `@github/copilot@1.0.89` = `ae8b863180f489752fb75a59b41b8a9c973ca184`; `@github/copilot-darwin-arm64@1.0.89` = `0e5b112eff3e0764791f57c0d6a41fb881ffe6cf`.
- **Verified** — The pinned platform package `@github/copilot-darwin-arm64@1.0.89` contains `bin.copilot-darwin-arm64: copilot` and exports `./copilot` (`platform/package/package.json:21-26`); `file` identified that executable as `Mach-O 64-bit executable arm64`, and `./copilot --version` printed `GitHub Copilot CLI 1.0.89.`
- **Verified** — `strings -a platform/package/copilot` included Node/undici/SEA strings such as `deps/undici/undici.js`, `Welcome to Node.js`, and `Operation cannot be invoked when not in a single-executable application`. This verifies a bundled Node/undici runtime signal, not a plain JS bundle.
- **Verified** — OpenCode source was pinned to `anomalyco/opencode@2fa3363c924c5c3e367b84a87ae478296a0ed59b` (release-era commit used by the existing Hibernaut research). Main files cited below: <https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/plugin/github-copilot/copilot.ts>, <https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/plugin/github-copilot/models.ts>, and <https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/auth/index.ts>.
- **Docs-only** — GitHub announced official OpenCode support on 2026-01-16: <https://github.blog/changelog/2026-01-16-github-copilot-now-supports-opencode/>.

## 1. Install and runtime

- **Docs-only** — Install options are npm (`npm install -g @github/copilot`, Node.js 22+), WinGet, Homebrew cask, the install script (`curl -fsSL https://gh.io/copilot-install | bash`, with `PREFIX` and `VERSION`), and direct executable downloads from `github/copilot-cli` releases. Source: install docs above.
- **Verified** — The npm package is not the CLI implementation itself; it is a Node ESM loader that resolves and executes an optional per-platform binary. Source: `package/package.json:16-38` and `package/npm-loader.js:7-8` from `@github/copilot@1.0.89`.
- **Verified** — The standalone package is a native executable with embedded Node/undici runtime signals, not a Bun executable. Source: `@github/copilot-darwin-arm64@1.0.89` package metadata (`platform/package/package.json:21-26`), `file`, `./copilot --version`, and `strings` output described above.
- **Docs-only** — `COPILOT_HOME` relocates config/state and defaults to `$HOME/.copilot`; `--config-dir` is deprecated. Source: CLI config-dir reference and `copilot help environment` from v1.0.89.
- **Verified** — `copilot help environment` in v1.0.89 documents `HTTP_PROXY`/`HTTPS_PROXY` (case-insensitive) and `NO_PROXY` for outgoing HTTP(S), and `copilot help config` documents `proxyUrl`, overridden by proxy env vars.
- **Docs-only** — Copilot reads certificates from the OS trust store and from the file named by Node's `NODE_EXTRA_CA_CERTS`; Copilot supports transparent proxies and custom MITM CAs. Source: GitHub Copilot network settings docs.
- **Not found** — I found no Copilot CLI-specific documentation for `SSL_CERT_FILE`. It may affect lower-level OpenSSL/Node behavior in some builds, but that was not verified for this binary.
- **Not found** — I found no evidence of Copilot CLI certificate pinning in docs or readable bundle output. The documented OS/`NODE_EXTRA_CA_CERTS` custom-CA support argues against pinning for normal Copilot traffic, but that is not a direct proof for every internal request path.

## 2. Auth methods and precedence

- **Docs-only** — Copilot CLI auth precedence is exactly: `COPILOT_GITHUB_TOKEN`, `GH_TOKEN`, `GITHUB_TOKEN`, OAuth token from the system keychain, then `gh auth token` fallback. Source: auth docs and `copilot login --help`.
- **Docs-only** — `COPILOT_GITHUB_TOKEN`, `GH_TOKEN`, and `GITHUB_TOKEN` env vars are the recommended non-interactive/container/CI path; env vars silently override stored OAuth, except the auto-injected Codespaces `GITHUB_TOKEN` does not override a `/login` account unless explicitly exported. Source: auth docs and `copilot help environment`.
- **Docs-only** — Interactive auth is `/login` or `copilot login`. Local desktop defaults to browser/loopback web flow; SSH/headless/Codespaces/dev containers/CI default to OAuth device-code flow. `copilot login` supports `--device-code`, `--web-flow`, `--host`, and `--with-token`. Source: auth docs and command reference.
- **Not found** — The OAuth client ID used by GitHub Copilot CLI's own interactive OAuth flow was not documented in the docs I found, and was not recoverable from readable strings in the pinned binary.
- **Docs-only** — Supported token types are: OAuth tokens (`gho_`), fine-grained PATs (`github_pat_`) with the **Copilot Requests** account permission and personal-account ownership, and GitHub App user-to-server tokens (`ghu_`) via env var. Classic PATs (`ghp_`) are not supported. Source: auth docs and command reference.
- **Docs-only** — OAuth tokens from the GitHub CLI (`gh`) app are explicitly supported; the `gh` fallback is the lowest-priority auth method. Source: auth docs and command reference.
- **Inferred** — A laptop `gh auth token` value of the usual GitHub CLI OAuth-app form (`gho_`, default gh scopes such as `repo`, `read:org`, `gist`) should power Copilot CLI when supplied as `GH_TOKEN` if the account has Copilot entitlement, because the docs explicitly support OAuth tokens from the GitHub CLI app and place `GH_TOKEN` above `gh` fallback. I did not live-test the user's token, and I found no docs saying the default `gh` scopes are insufficient.
- **Not found** — I found no official statement that classic PAT `ghp_` can be made to work by adding scopes; docs say classic PATs are not supported.

## 3. Storage

- **Docs-only** — Interactive OAuth tokens are stored in the OS keychain under service name `copilot-cli`: macOS Keychain Access, Windows Credential Manager, or Linux libsecret. Source: auth docs.
- **Docs-only** — If the system credential store is unavailable (example: headless Linux without `libsecret`), Copilot CLI prompts to store the token in plaintext `~/.copilot/config.json`; `COPILOT_HOME` changes the base directory. Source: auth docs and command reference.
- **Docs-only** — The config directory contains `config.json` (automatically managed app state including authentication), `settings.json`, `permissions-config.json`, `providers.json`, `mcp-*`, logs, session state, skills, hooks, and other state. Source: config-dir reference.
- **Not found** — I found no docs or readable code saying that a token provided through `COPILOT_GITHUB_TOKEN`, `GH_TOKEN`, or `GITHUB_TOKEN` is written to disk. The docs present env-var auth as an automatic per-command credential source, and storage text is tied to OAuth login.
- **Implication** — For Hibernaut, prefer env-var placeholders over `copilot login --with-token` inside machines, because `login` may persist a real or placeholder token under `COPILOT_HOME` while env-var use has no documented persistence path.

## 4. Network behavior

- **Docs-only** — GitHub's Copilot allowlist lists these relevant required domains: `github.com/login/*`, `github.githubassets.com`, `avatars.githubusercontent.com`, `github.com/copilot/*`, `api.github.com/user`, `api.github.com/copilot_internal/*`, `collector.github.com/*`, `copilot-telemetry.githubusercontent.com/telemetry`, `default.exp-tas.com`, `copilot-proxy.githubusercontent.com`, `origin-tracker.githubusercontent.com`, `*.githubcopilot.com/*`, `*.individual.githubcopilot.com`, `*.business.githubcopilot.com`, and `*.enterprise.githubcopilot.com`. Source: allowlist reference.
- **Docs-only** — The same allowlist says not to allowlist wildcard Copilot model domains if using subscription-based network routing; use plan-specific routing controls instead. Source: allowlist reference.
- **Docs-only** — GitHub Actions examples run Copilot CLI directly with `GITHUB_TOKEN` in the environment and require `copilot-requests: write`; this demonstrates env-token auth is intended for Copilot model requests in automation. Source: Copilot CLI in Actions docs.
- **Inferred** — Because `api.github.com/copilot_internal/*` is explicitly required for Copilot user management, Copilot clients likely exchange or validate GitHub-user credentials there before/while calling model hosts. This matches historical Copilot client architecture, but the exact Copilot CLI request and response were not verified from readable bundled code.
- **Not found** — For the official Copilot CLI, I did not verify the exact endpoint path (`/copilot_internal/v2/token` or otherwise), request headers, response fields, derived-token lifetime, refresh timing, or cache location. The binary was not plaintext JS, and docs do not spell this out.
- **Not found** — I found no CLI-specific enumeration of all headers sent to the model API. For OpenCode's provider, source is readable and listed in §7.
- **Not found** — I found no evidence that the official CLI caches a short-lived Copilot token on disk, and no evidence that it does not.
- **Docs-only** — Telemetry/experimentation hosts include `collector.github.com`, `copilot-telemetry.githubusercontent.com`, and `default.exp-tas.com`; offline mode disables GitHub authentication, telemetry, web tools, GitHub MCP, and auto-update but requires a local BYOK provider. Sources: allowlist reference and auth docs.

## 5. Headless use

- **Verified** — v1.0.89 `copilot --help` documents `-p, --prompt <text>` for non-interactive mode, `-s, --silent`, `--output-format text|json`, `--model`, `--reasoning-effort`, `--context`, `--auto-tier`, and `--no-ask-user`.
- **Verified** — v1.0.89 `copilot --help` says `--allow-all-tools` allows all tools automatically and is required for non-interactive mode; `--allow-all` and `--yolo` are equivalent to `--allow-all-tools --allow-all-paths --allow-all-urls`. `copilot help permissions` confirms denial rules still take precedence.
- **Verified** — `copilot help environment` says `COPILOT_ALLOW_ALL=true` both auto-approves tools and trusts the working directory without prompting; other truthy spellings (`1`, `yes`, `on`, `y`) auto-approve tools but do not also trust cwd hooks/skills/plugins/MCP servers.
- **Verified** — `copilot help config` lists `trustedFolders` as folders where permission to read or execute files has been granted, and `defaultPermissionMode` can be `manual`, `assisted`, or `allow-all` unless policy disables bypass.
- **Verified** — `copilot --help` documents `--secret-env-vars` for stripping named env values from shell and MCP environments and redacting output.
- **Docs-only** — Non-interactive workflow runs exist as `copilot workflow run NAME`, with `--args`, `--silent`, `--output-format`, and `--result-file`; it cannot be combined with `--prompt`. Source: command reference.
- **Verified** — v1.0.89 `copilot help config` listed model settings and `--model` options; model availability is account/plan/policy-dependent, so bake no Hibernaut assumption that a particular model is always available.
- **Recommended smoke test** — **Inferred** unattended command for Hibernaut machines:

```bash
COPILOT_GITHUB_TOKEN="$GH_TOKEN" COPILOT_AUTO_UPDATE=false COPILOT_ALLOW_ALL=true copilot -p 'Reply with exactly OK.' -s --no-ask-user --model auto
```

This uses the GitHub placeholder token under the highest-precedence Copilot env var and avoids interactive permission prompts. In a repository where trusting hooks/skills is undesirable, use explicit CLI flags instead of `COPILOT_ALLOW_ALL=true`, e.g. `--allow-all-tools --allow-all-paths --allow-all-urls`, and disable repo customizations as needed.

## 6. Policy and billing/accounting

- **Docs-only** — An active Copilot subscription is a prerequisite, and org/enterprise admins can disable Copilot CLI. Source: install docs.
- **Docs-only** — For GitHub Actions, PAT auth bills AI credits to the PAT user's Copilot seat; `GITHUB_TOKEN` authenticates as an installation. In personal repos it bills the repo owner's seat; in org repos it meters directly to the organization and requires the org policy **Allow use of Copilot CLI billed to the organization**. Source: Copilot CLI in Actions docs.
- **Docs-only** — GitHub recommends `GITHUB_TOKEN` for org-owned automation and says each workflow run receives a short-lived scoped token, avoiding long-lived stored credentials. Source: Copilot CLI in Actions docs.
- **Docs-only** — Directly invoking Copilot CLI in workflows gives broad access to the workflow environment; GitHub recommends GitHub Agentic Workflows for most automation. Source: use-in-Actions docs.
- **Not found** — I found no GitHub policy text prohibiting Copilot CLI use from remote VMs, sandboxes, or through proxies. The docs explicitly cover SSH/headless environments, containers, CI, proxies, and custom certificates.
- **Docs-only** — Usage is now described in AI credits; legacy accounts may see premium requests. Source: Copilot CLI help/billing docs surfaced by `copilot help billing` in the agent notes and GitHub billing docs.

## 7. OpenCode + Copilot

- **Docs-only** — GitHub says it officially supports using Copilot Pro, Pro+, Business, or Enterprise subscriptions with OpenCode; setup is `/connect`, choose **GitHub Copilot**, complete the GitHub device login flow, then use OpenCode powered by Copilot. Source: GitHub changelog, 2026-01-16.
- **Verified** — In pinned OpenCode source, `CLIENT_ID = "Ov23li8tweQw6odWQebz"`; device and token URLs are `https://${domain}/login/device/code` and `https://${domain}/login/oauth/access_token`; the base model host is `https://api.githubcopilot.com` or `https://copilot-api.${enterpriseDomain}`. Source: [`copilot.ts#L9-L28`](https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/plugin/github-copilot/copilot.ts#L9-L28).
- **Verified** — OpenCode requests device flow with JSON body `{ client_id: CLIENT_ID, scope: "read:user" }`, then polls `ACCESS_TOKEN_URL` with `grant_type: urn:ietf:params:oauth:grant-type:device_code`. Source: [`copilot.ts#L234-L244`](https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/plugin/github-copilot/copilot.ts#L234-L244) and [`copilot.ts#L264-L275`](https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/plugin/github-copilot/copilot.ts#L264-L275).
- **Verified** — On success, OpenCode stores `refresh: data.access_token`, `access: data.access_token`, and `expires: 0`; there is no separate refresh token in that code path. Source: [`copilot.ts#L286-L305`](https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/plugin/github-copilot/copilot.ts#L286-L305).
- **Verified** — OpenCode storage path is `path.join(Global.Path.data, "auth.json")`, and writes use mode `0o600`; `OPENCODE_AUTH_CONTENT` can override file contents. Source: [`auth/index.ts#L10`](https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/auth/index.ts#L10), [`auth/index.ts#L58-L66`](https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/auth/index.ts#L58-L66), and [`auth/index.ts#L73-L80`](https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/auth/index.ts#L73-L80).
- **Verified** — OpenCode model listing calls `${baseURL}/models` with headers including `Authorization`, `User-Agent: opencode/<version>`, and `X-GitHub-Api-Version: 2026-06-01`. Source: [`copilot.ts#L71-L77`](https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/plugin/github-copilot/copilot.ts#L71-L77) and [`models.ts#L217-L228`](https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/plugin/github-copilot/models.ts#L217-L228).
- **Verified** — OpenCode request wrapping sends `Authorization`, `User-Agent`, `Openai-Intent: conversation-edits`, and `x-initiator`, optionally `Copilot-Vision-Request`, then deletes lower-case `x-api-key`/`authorization`. Source: [`copilot.ts#L160-L178`](https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/plugin/github-copilot/copilot.ts#L160-L178).
- **Verified** — At this pinned commit, I found no `api.github.com/copilot_internal/v2/token` exchange in the OpenCode Copilot provider source; it passes the stored OAuth token directly to the Copilot API host.
- **Inferred** — The hardcoded/non-exchange OpenCode flow may be less robust for Business/Enterprise or plan-specific routing than the official CLI, because GitHub's allowlist includes plan-specific `*.business.githubcopilot.com` and `*.enterprise.githubcopilot.com`, while this pinned OpenCode code defaults to `api.githubcopilot.com` unless an enterprise URL is configured.

## 8. Implications for Hibernaut

### (a) Can one `gho_` token power git, `gh`, and Copilot CLI?

- **Docs-only / Inferred** — Yes for v1, if the account has Copilot entitlement. `GH_TOKEN=<placeholder>` already powers `gh`; git over HTTPS is powered by `gh auth setup-git`; Copilot CLI docs explicitly put `GH_TOKEN` second in precedence and support OAuth tokens from the GitHub CLI app. I did not live-test a particular laptop `gho_` token, and fine-grained PATs for Copilot have different requirements.
- **Operational caveat** — The safest Copilot env var is `COPILOT_GITHUB_TOKEN=$GH_TOKEN` so Copilot CLI uses the placeholder at top precedence while `gh` keeps using `GH_TOKEN`.

### (b) Which hosts should Hibernaut bind for swaps?

- **Docs-only** — Minimum GitHub/Copilot bind set for v1 should include `github.com`, `api.github.com`, `github.githubassets.com`, `avatars.githubusercontent.com`, `collector.github.com`, `copilot-telemetry.githubusercontent.com`, `default.exp-tas.com`, `copilot-proxy.githubusercontent.com`, `origin-tracker.githubusercontent.com`, `api.githubcopilot.com`, and plan-specific `*.individual.githubcopilot.com`, `*.business.githubcopilot.com`, `*.enterprise.githubcopilot.com` (or the concrete hosts observed in logs). Source: Copilot allowlist reference.
- **Inferred** — For secret swapping, the critical bound hosts are `github.com` and `api.github.com` for OAuth/user/token operations, plus whichever Copilot model host the CLI actually uses. Telemetry/experimentation can be pass-through or blocked depending on Hibernaut policy; they should not need the user's GitHub token except where the CLI chooses to attach one.
- **Not found** — Exact official CLI host use per command was not captured packet-by-packet here.

### (c) Can the proxy replace a derived short-lived Copilot token with a placeholder?

- **Inferred** — If the official CLI receives a short-lived Copilot bearer from `api.github.com/copilot_internal/*`, Hibernaut's response scrubber could replace that real token with a look-alike placeholder in the exchange response, store the real derived token in the host vault keyed to the machine/session, and swap placeholder → real on Copilot model hosts.
- **Could break** — The CLI may validate token shape, expiry, signature/JWT fields, or endpoint metadata locally; the placeholder must preserve whatever syntax the CLI parses. If the exchange response includes host routing metadata, Hibernaut must not corrupt it. If the derived token is used for WebSocket/SSE or non-JSON bodies, the proxy must handle streaming headers without buffering large model streams. If the CLI caches the derived token on disk, the placeholder cache is fine, but Hibernaut must expire the host-side real token on the original lifetime. If the token appears in debug logs or telemetry bodies, response/body scrubbing must catch it. If the model host varies by plan (`business`/`enterprise`), all relevant hosts must be bound.
- **Not found** — The official CLI's exchange response format, derived-token lifetime, refresh threshold, and disk cache behavior were not verified, so this design needs an e2e MITM trace before implementation.

### (d) Recommended v1 wiring

- **Verified/Docs-only** — Install the official Copilot CLI with npm in the image (`npm install -g @github/copilot`) if Node 22+ is present, or install the standalone binary/cask. Pin an exact version during image builds for reproducibility.
- **Recommended config/env** — Bake these into machines:

```bash
export GH_TOKEN='<github-placeholder>'
export COPILOT_GITHUB_TOKEN="$GH_TOKEN"
export COPILOT_HOME="$HOME/.copilot"
export COPILOT_AUTO_UPDATE=false
export NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/hibernaut-ca.crt
export SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
```

- **Recommended files** — Create `$COPILOT_HOME/settings.json` with conservative defaults, for example:

```jsonc
{
  "autoUpdate": false,
  "banner": "never",
  "defaultPermissionMode": "manual",
  "trustedFolders": []
}
```

For fully unattended agent runs, pass explicit flags per invocation rather than globally setting `defaultPermissionMode: "allow-all"` in a golden image.
- **Recommended e2e smoke test** — Run inside a fresh machine after `gh auth setup-git` and CA install:

```bash
COPILOT_GITHUB_TOKEN="$GH_TOKEN" COPILOT_AUTO_UPDATE=false COPILOT_ALLOW_ALL=true copilot -p 'Reply with exactly OK.' -s --no-ask-user --model auto
```

Expected output should be exactly or visibly `OK`; any prompt, login URL, cert error, or immediate silent exit should fail the smoke test.

## 9. Live trace (2026-10-04)

- **Setup** — Copilot CLI 1.0.91 on the laptop (macOS) ran
  `copilot -p "Reply with exactly OK." -s --allow-all-tools --no-ask-user` through mitmproxy, with
  `HTTPS_PROXY`, `NODE_EXTRA_CA_CERTS` set to the mitm CA, a temp `COPILOT_HOME` and
  `COPILOT_AUTO_UPDATE=false`. The addon printed only hosts, methods, paths without queries,
  statuses, header names and token shapes. It wrote no flow file, and the temp files were deleted.
- **Verified live** — There is no token exchange. No request went to `copilot_internal/v2/token`
  or any other exchange endpoint. The `gho_` token from `GH_TOKEN` went, in the `Authorization`
  header, straight to:
  - `api.github.com`: `/copilot_internal/user` and `/copilot/mcp_registry`.
  - `api.enterprise.githubcopilot.com`: `/models`, `/mcp/readonly` and `/v1/messages`. The API
    host depends on the plan. This account's plan is enterprise.
- **Verified live** — A placeholder works. With `GH_TOKEN` set to a placeholder made by
  Hibernaut's `vault.Placeholder`, and the addon swapping it for the real token on `api.github.com`
  and `*.githubcopilot.com`, the run printed OK.
- **Verified live** — `COPILOT_HOME` held no token after the run.
- **Implication** — Copilot needs no token endpoint in the proxy. Binding `*.githubcopilot.com`
  to the GitHub secret, next to `github.com`, `api.github.com` and `uploads.github.com`, is enough.

## Gaps

- **Not found** — Copilot CLI OAuth client ID.
- **Answered (§9)** — The official CLI does no `copilot_internal` exchange in 1.0.91. It sends the
  GitHub token to the Copilot API host directly.
- **Answered (§9)** — An env-var token was not written to `COPILOT_HOME` in the traced run.
- **Not found** — CLI-specific support for `SSL_CERT_FILE`.
- **Not found** — Certificate pinning evidence. Custom-CA docs suggest normal trust-store behavior but do not prove every path. The traced run trusted the mitm CA from `NODE_EXTRA_CA_CERTS`.
- **Answered (§9)** — A real `gho_` token from gh works as `GH_TOKEN`, and so does a placeholder
  swapped for it.
