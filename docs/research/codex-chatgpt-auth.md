# Codex ChatGPT-plan auth — S6 research notes

**Date checked:** 2026-09-30. **Live checks:** 2026-10-04 with codex-cli 0.160.0, the version
Hibernaut pins (§11). Where §11 and the source notes disagree, §11 wins for that version.
**Primary code source:** `openai/codex` release tag `rust-v0.159.2`, annotated tag object `8b9fa496bbf2c47aebd62e85a080b9a522a455b5`, commit `ff6aec96948b70d94983af2641a6b67c94faeff5`. All Codex source links below are pinned to that commit. **Code wins over docs where they disagree.**
**Docs checked:** `developers.openai.com/codex/*` / `learn.chatgpt.com/docs/*`, OpenAI Help, and OpenAI policy pages where fetchable.

## Summary

- **Verified:** `CODEX_ACCESS_TOKEN` is now the cleanest S6 path *if* the user is in a ChatGPT Business/Enterprise workspace that allows Codex access-token creation. Access tokens are documented ChatGPT workspace credentials for trusted automation, created in the ChatGPT admin console, scoped to Codex, and used directly by Codex CLI via env var or `codex login --with-access-token`.
- **Verified:** ChatGPT `auth.json` sessions use rotating refresh tokens. Codex classifies `refresh_token_reused` as an exhausted/reused token, rewrites `auth.json` after successful refresh, and has a 401 refresh-and-retry path. Sharing one real `auth.json` across concurrent machines is unsafe.
- **Verified:** Codex decodes ChatGPT JWT payloads locally without signature verification for claims and expiry. That makes placeholder JWTs parseable, but it does **not** make them usable against `chatgpt.com`; the first real request/refresh still needs a real token.
- **Verified:** Official docs explicitly support copying `~/.codex/auth.json` to headless machines and trusted CI as an advanced workflow, but warn to treat it like a password and use one serialized runner stream per file.
- **Recommendation:** v1 should support `CODEX_ACCESS_TOKEN` first for eligible workspaces; otherwise use a host-owned, single-refresh-stream real `auth.json` and inject a placeholder `auth.json` into machines only if the proxy can also rewrite refresh requests and serialize refreshes. Do not let multiple machines refresh the same token family.

## Source abbreviations

- `codex@ff6` = `https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/...`
- Codex auth docs: https://developers.openai.com/codex/auth (redirects to `learn.chatgpt.com/docs/auth`).
- Codex env docs: https://learn.chatgpt.com/docs/config-file/environment-variables.
- Codex non-interactive docs: https://developers.openai.com/codex/noninteractive.
- Codex CI auth docs: https://learn.chatgpt.com/docs/auth/ci-cd-auth.
- Codex access token docs: https://learn.chatgpt.com/docs/enterprise/access-tokens.

## 1. `CODEX_ACCESS_TOKEN`, `at-` tokens and agent identity

- **Verified:** Codex reads `CODEX_ACCESS_TOKEN` after `CODEX_API_KEY` and ephemeral external auth, but before persisted auth storage. In `load_auth`, the env var is classified and loaded before `auth.json`/keyring credentials. Source: `codex-rs/login/src/auth/manager.rs` `load_auth`, `CODEX_ACCESS_TOKEN_ENV_VAR` (`codex@ff6`: [`manager.rs#L955`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/auth/manager.rs#L955), [`manager.rs#L1488-L1596`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/auth/manager.rs#L1488-L1596)).
- **Verified:** A `CODEX_ACCESS_TOKEN` value beginning with `at-` is treated as a personal access token; all other values are treated as Agent Identity JWTs. Source: `CodexAccessToken::try_from`, `PERSONAL_ACCESS_TOKEN_PREFIX = "at-"` ([`access_token.rs#L1-L16`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/auth/access_token.rs#L1-L16)).
- **Verified:** The `at-` personal-token path calls OpenAI's `whoami` endpoint under `https://auth.openai.com/api/accounts/v1/user-auth-credential/whoami` and returns the user's email, ChatGPT user/account ids, plan type, and FedRAMP flag. Source: [`personal_access_token.rs#L12-L107`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/auth/personal_access_token.rs#L12-L107).
- **Verified:** Agent Identity JWTs are not just blindly trusted when used for env/login auth: Codex fetches OpenAI JWKS and verifies issuer/audience/signature in `verified_record_from_jwt`. Source: [`agent_identity.rs#L253-L277`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/auth/agent_identity.rs#L253-L277), [`agent-identity/src/lib.rs#L266-L290`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/agent-identity/src/lib.rs#L266-L290).
- **Docs-only:** Codex access tokens are ChatGPT workspace credentials for trusted non-interactive local workflows, currently supported for ChatGPT Business and Enterprise workspaces. They are created in ChatGPT admin console at **Access tokens**, belong to the creator/workspace, can be scoped to Codex, and can have finite validity windows; scoped tokens must expire, while an older Codex-only dialog may offer no expiration. Source: Codex access token docs.
- **Docs-only:** Access-token usage is associated with the ChatGPT workspace user who created the token and workspace governance data; for general OpenAI API calls, docs say to keep using Platform API keys. Source: Codex access token docs.
- **Docs-only:** API-key auth bills through the OpenAI Platform at API rates; ChatGPT sign-in follows ChatGPT workspace permissions and entitlements. Source: Codex auth docs.
- **Implication:** Hibernaut design 1 works only for users/workspaces that can create Codex access tokens. For a solo ChatGPT Plus/Pro account, **Not found:** an official UI or CLI path to create `at-...` access tokens.

## 2. Device-code login

- **Verified:** `codex login --device-auth` uses OpenAI-specific device endpoints, not RFC 8628's standard paths: `POST {issuer}/api/accounts/deviceauth/usercode`, then polling `POST {issuer}/api/accounts/deviceauth/token`, then a PKCE token exchange using a returned authorization code and verifier. The user visits `{issuer}/codex/device`. Source: [`device_code_auth.rs#L70-L233`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/device_code_auth.rs#L70-L233).
- **Verified:** The OAuth client id is `app_EMoamEEZ73f0CkXaXp7hrann`, overridable by `CODEX_APP_SERVER_LOGIN_CLIENT_ID`. Source: [`manager.rs#L216`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/auth/manager.rs#L216), [`manager.rs#L1717-L1724`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/auth/manager.rs#L1717-L1724).
- **Docs-only:** Official docs call device-code login beta, say to enable device-code login in ChatGPT security settings or workspace permissions, and recommend it for remote/headless environments. Source: Codex auth docs, “Login on headless devices”.
- **Verified:** Device login works without a browser in the Linux guest because the CLI prints the user code/verification URL and polls. It still requires an out-of-band browser to approve the code. Source: device-code source above and docs.
- **Implication:** The host can run the official Codex binary's device login and create a token family dedicated to `hibd`, separate from the laptop login, if the user completes the browser approval. This avoids copying the laptop's refresh token.

## 3. Refresh tokens

- **Verified:** Refresh goes to `https://auth.openai.com/oauth/token`, overridable by `CODEX_REFRESH_TOKEN_URL_OVERRIDE`. Source: [`manager.rs#L212-L214`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/auth/manager.rs#L212-L214).
- **Verified:** The refresh request uses `grant_type=refresh_token`, the current `refresh_token`, and the Codex OAuth `client_id`; the parsed response fields are optional `id_token`, `access_token`, and `refresh_token`. Source: [`manager.rs#L1624-L1714`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/auth/manager.rs#L1624-L1714).
- **Verified:** Refresh tokens are rotating/single-use from Codex's perspective: server error code `refresh_token_reused` maps to `RefreshTokenFailedReason::Exhausted`, with the fixture message “Your refresh token has already been used to generate a new access token.” Source: [`manager.rs#L1650-L1707`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/auth/manager.rs#L1650-L1707), [`util.rs#L26-L33`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/auth/util.rs#L26-L33).
- **Verified:** After a successful refresh, Codex updates access/refresh/id tokens as returned, sets `last_refresh = Utc::now()`, and persists the auth store. Source: [`manager.rs#L1598-L1622`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/auth/manager.rs#L1598-L1622).
- **Verified:** Codex proactively refreshes if the access-token JWT `exp` is within 5 minutes; if `exp` is unparseable, it uses `last_refresh` older than 8 days as a staleness fallback. Source: constants and `should_refresh_proactively` ([`manager.rs#L203-L204`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/auth/manager.rs#L203-L204), [`manager.rs#L3001-L3026`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/auth/manager.rs#L3001-L3026)).
- **Docs-only:** CI docs repeat the 8-day stale-session behavior and say Codex writes refreshed tokens and `last_refresh` back to `auth.json`; they require one `auth.json` per runner or serialized workflow stream. Source: Codex CI auth docs.
- **Implication:** If the laptop and host share one refresh token, a host refresh can rotate it out from under the laptop. If two machines refresh copied `auth.json` concurrently, one can trigger reuse/exhaustion.

## 4. `auth.json` schema, JWT claims, signature checks and writes

- **Verified:** At this release, `auth.json` deserializes to:

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

Source: [`storage.rs#L39-L64`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/auth/storage.rs#L39-L64).

- **Verified:** `tokens` has `id_token`, `access_token`, `refresh_token`, and optional `account_id`. `IdTokenInfo` exposes `email`, `chatgpt_plan_type`, `chatgpt_user_id`, `chatgpt_account_id`, `chatgpt_account_is_fedramp`, and `raw_jwt`. Source: [`token_data.rs#L11-L40`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/token_data.rs#L11-L40).
- **Verified:** Codex reads custom claims under `https://api.openai.com/profile` and `https://api.openai.com/auth`; the important Hibernaut claims are account id, user id, plan type, FedRAMP flag and email. Source: [`token_data.rs#L70-L96`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/token_data.rs#L70-L96).
- **Verified:** ChatGPT JWT parsing is payload-only: `decode_jwt_payload` splits `header.payload.signature`, base64url-decodes the payload, and JSON-parses it. It does not verify the signature. Source: [`token_data.rs#L127-L140`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/token_data.rs#L127-L140).
- **Verified:** File storage writes pretty JSON to `auth.json` and uses mode `0600` on Unix. Source: [`storage.rs#L206-L225`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/auth/storage.rs#L206-L225).
- **Verified:** Codex writes `auth.json` on login/access-token persistence and on refresh. It may also delete/migrate file fallback depending on `cli_auth_credentials_store`. Source: storage save and refresh persistence links above.
- **Docs-only:** `cli_auth_credentials_store = "file" | "keyring" | "auto" | "ephemeral"`; file mode stores credentials under `CODEX_HOME/auth.json`, default `~/.codex/auth.json`. Source: Codex auth docs.

## 5. Failure paths and startup validation

- **Verified:** On 401, `UnauthorizedRecovery` first reloads cached auth from disk, then tries a forced refresh from the authority; if that fails, it returns the auth error. Source: [`manager.rs#L1849-L2028`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/auth/manager.rs#L1849-L2028), [`manager.rs#L2848-L2921`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/auth/manager.rs#L2848-L2921).
- **Verified:** The interactive TUI's first-login gate is mostly an existence/presence check: it delays login UI if `CODEX_ACCESS_TOKEN`, workload identity env, `auth.json`, `config.toml`, or `environments.toml` exists under `CODEX_HOME`; it does not validate token cryptography there. Source: [`startup_preflight.rs#L20-L70`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/tui/src/startup_preflight.rs#L20-L70).
- **Verified:** Device-code polling treats 403/404 as “not approved yet” until timeout, not as token validation for an existing auth file. Source: [`device_code_auth.rs#L112-L137`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/device_code_auth.rs#L112-L137).
- **Not found:** A mandatory `/backend-api/me` validation call on ordinary startup before the first model/backend request. The verified startup preflight is local presence-only.
- **Docs-only:** CI docs say 401 also has built-in refresh-and-retry behavior. Source: Codex CI auth docs.

## 6. Transport and ChatGPT-mode hosts

- **Verified:** The ChatGPT Codex base URL constant is `https://chatgpt.com/backend-api/codex`. Source: [`model-provider-info/src/lib.rs#L77`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/model-provider-info/src/lib.rs#L77).
- **Verified:** The normal streamed Responses transport uses Server-Sent Events/EventSource parsing (`eventsource_stream`) in `codex-api/src/sse/responses.rs`. Source: [`responses.rs#L18`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/codex-api/src/sse/responses.rs#L18), [`responses.rs#L528-L626`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/codex-api/src/sse/responses.rs#L528-L626).
- **Verified:** WebSocket support exists for Responses telemetry/transport paths, and Codex docs say custom CA applies to secure WebSocket connections. Source: `codex-api/src/telemetry.rs` WebSocket trait ([`telemetry.rs#L1-L80`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/codex-api/src/telemetry.rs#L1-L80)), Codex auth docs custom CA section.
- **Verified:** Credentials are carried as `Authorization: Bearer <token>` and `ChatGPT-Account-ID` when an account id is available. Source: [`bearer_auth_provider.rs#L29-L41`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/model-provider/src/bearer_auth_provider.rs#L29-L41).
- **Verified:** Other ChatGPT-backend endpoints in code include `/wham/config/bundle`, token usage profile URLs, `/backend-api/files`, `/backend-api/codex/models`, cloud-task headers, and analytics events under `/codex/analytics-events/events`. Sources: backend client config/usage ([`backend-client/src/client.rs#L410-L541`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/backend-client/src/client.rs#L410-L541)), files header tests ([`codex-api/src/files.rs`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/codex-api/src/files.rs)), analytics URL construction ([`analytics/src/client.rs#L142`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/analytics/src/client.rs#L142)).
- **Verified:** Login/refresh/revoke/device endpoints use `auth.openai.com`. Source links in sections 2 and 3.
- **Verified:** Codex explicitly supports custom CAs; `CODEX_CA_CERTIFICATE` takes precedence over `SSL_CERT_FILE`, and network-proxy code also recognizes common CA env vars. Source: [`http-client/src/custom_ca.rs#L1-L80`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/http-client/src/custom_ca.rs#L1-L80), [`network-proxy/src/certs.rs#L1-L80`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/network-proxy/src/certs.rs#L1-L80), Codex env docs.
- **Verified:** No certificate pinning was found; the code path is built around custom CA/root loading and route-aware clients. Source: custom CA source and `create_client_for_route_async` transport construction ([`default_client.rs#L230-L322`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/login/src/auth/default_client.rs#L230-L322)).
- **Verified:** Codex can respect proxy policy through reqwest/system route-aware clients; docs list `CODEX_CA_CERTIFICATE`/`SSL_CERT_FILE` but do not list `HTTPS_PROXY` on the stable env page. Source: default client link above. **Inferred:** reqwest/system proxy handling means standard `HTTPS_PROXY` should work outside sandbox-specific routing, but Hibernaut should verify empirically with its installed binary.
- **Not found:** Statsig/Sentry/`ab.chatgpt.com` calls in the local Codex CLI source path at this tag. Analytics to ChatGPT backend was found.

## 7. Unattended use in a fresh Hibernaut machine

- **Docs-only:** Non-interactive runs use `codex exec`; by default `codex exec` is read-only. For automation, set an explicit sandbox: `--sandbox workspace-write` for edits or `--sandbox danger-full-access` for controlled isolated environments. Use `--skip-git-repo-check` outside a Git repository. Source: Codex non-interactive docs.
- **Docs-only:** `CODEX_HOME` stores config/auth/logs and must already exist if overridden. Source: Codex env docs.
- **Docs-only:** `approval_policy = "never"` means Codex does not ask for approval; `sandbox_mode = "danger-full-access"` is a supported enum. Source: config-reference schema and config docs.
- **Verified:** Linux sandboxing in this release uses bubblewrap for filesystem restrictions and seccomp for network isolation; Landlock helpers are legacy/backup. Source: [`linux-sandbox/src/landlock.rs#L1-L4`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/linux-sandbox/src/landlock.rs#L1-L4), [`landlock.rs#L35-L80`](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/linux-sandbox/src/landlock.rs#L35-L80).
- **Inferred:** Because Hibernaut machines are already isolated Firecracker microVMs and may have user-namespace/bubblewrap surprises, v1 should disable Codex's own sandbox for smoke tests and unattended coding (`danger-full-access`), then revisit a narrower mode later.

Recommended image/user config:

```toml
# /home/user/.codex/config.toml
approval_policy = "never"
sandbox_mode = "danger-full-access"
check_for_update_on_startup = false

[analytics]
enabled = false

# Optional for a known workspace checkout root; generate per machine if needed.
[projects."/home/user/work"]
trust_level = "trusted"
```

Recommended environment baked into `/etc/environment` (placeholder values where applicable):

```sh
CODEX_HOME=/home/user/.codex
CODEX_CA_CERTIFICATE=/etc/ssl/certs/ca-certificates.crt
SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
# Either:
CODEX_ACCESS_TOKEN=at_hib_PLACEHOLDER...
# Or no env token, with /home/user/.codex/auth.json present.
```

One-line smoke test:

```sh
codex exec --skip-git-repo-check --sandbox danger-full-access "Reply with exactly OK."
```

## 8. Official guidance and policy constraints

- **Docs-only:** Official headless guidance prefers device-code login; fallbacks are copying `~/.codex/auth.json` or SSH port-forwarding the localhost callback. Source: Codex auth docs.
- **Docs-only:** Official CI/CD guidance says API keys are the recommended default, and ChatGPT-managed `auth.json` automation is an advanced workflow only for trusted private automation. It explicitly says not to use it for public/open-source repos, to preserve refreshed `auth.json`, and to use one file per runner/serialized workflow stream. Source: Codex CI auth docs.
- **Docs-only:** Access-token docs say tokens should be stored in a secret manager, kept out of logs, rotated, and used only on trusted runners; public/forked/shared machines are called out as risks. Source: Codex access-token docs.
- **Docs-only:** OpenAI Help account-sharing policy says an OpenAI account is meant for the individual who created it and other people should have their own account. Source: https://help.openai.com/en/articles/10471989-openai-account-sharing-policy (raw page was fetchable but article text was not cleanly extracted by the tooling).
- **Not found:** A terms clause specifically banning a same-user, private, trusted proxy that keeps real Codex credentials on the user's own host. **Inferred:** hosted/multi-user Hibernaut should get legal review before relaying ChatGPT-plan credentials for customers; single-user private Hibernaut is much closer to OpenAI's documented headless/CI patterns.

## 9. OpenCode's ChatGPT Plus/Pro login

- **Verified:** Current OpenCode (`anomalyco/opencode` release `v1.18.33`, commit `2fa3363c924c5c3e367b84a87ae478296a0ed59b`) includes an OpenAI Codex OAuth plugin in-tree. It uses the same client id `app_EMoamEEZ73f0CkXaXp7hrann`, issuer `https://auth.openai.com`, callback port `1455`, scope `openid profile email offline_access`, and ChatGPT Codex endpoint `https://chatgpt.com/backend-api/codex/responses`. Source: [`packages/opencode/src/plugin/openai/codex.ts#L10-L14`](https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/plugin/openai/codex.ts#L10-L14), [`codex.ts#L88-L101`](https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/plugin/openai/codex.ts#L88-L101), [`codex.ts#L117-L148`](https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/plugin/openai/codex.ts#L117-L148).
- **Verified:** OpenCode's plugin parses JWT claims locally and stores OAuth credentials in OpenCode's auth store (`~/.local/share/opencode/auth.json`, mode 0600 per earlier OpenCode research). Source: [`codex.ts#L49-L77`](https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/plugin/openai/codex.ts#L49-L77), [`auth/index.ts#L10-L31`](https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/auth/index.ts#L10-L31), [`auth/index.ts#L76-L88`](https://github.com/anomalyco/opencode/blob/2fa3363c924c5c3e367b84a87ae478296a0ed59b/packages/opencode/src/auth/index.ts#L76-L88).
- **Inferred:** This is official to OpenCode because it is in the canonical OpenCode repo/release, but it is **not** official OpenAI guidance. It reuses OpenAI's public OAuth endpoints/client id but is not documented by OpenAI as an OpenCode integration.

## 10. Implications for Hibernaut S6 designs

### Design 1: `CODEX_ACCESS_TOKEN`

- **Feasibility:** **High for Business/Enterprise, Not found for Plus/Pro.** Source and docs agree that `CODEX_ACCESS_TOKEN` is first-class, env-based, non-interactive, and takes precedence over stored `auth.json`.
- **Proxy fit:** **High.** A placeholder `at-...` can be delivered via env and swapped in `Authorization: Bearer` for `auth.openai.com` whoami and `chatgpt.com/backend-api/codex` requests. It avoids refresh-token rotation inside machines.
- **Failure modes:** Ineligible workspace/user cannot create one; expired/revoked token fails at run start; placeholder must preserve enough shape for Codex's `at-` classifier.
- **Recommendation:** Implement first. UX should say “available for ChatGPT Business/Enterprise access tokens; Plus/Pro may need device-login/auth.json fallback.”

### Design 2: placeholder `auth.json`

- **Feasibility:** **Medium, but only if the proxy also swaps refresh requests and `hibd` serializes host-side refresh.** Codex will parse unsigned placeholder JWTs and skip the first-login gate, but real API calls and refreshes still go to OpenAI and require a real access/refresh token.
- **Good parts:** Real credentials can stay on the host; placeholder JWT claims can mirror plan/account/email and far-future `exp`; `CODEX_CA_CERTIFICATE` supports Hibernaut MITM.
- **Hard parts:** Codex writes `auth.json` after refresh. If the machine sends a placeholder refresh token to `auth.openai.com`, Hibernaut must swap it to the current real refresh token, capture/scrub the rotated real refresh token in the response, update the host vault, and return placeholder tokens to the machine. Because refresh tokens are single-use, this must be one serialized token family per Hibernaut user/account.
- **Failure modes:** Missed response rewrite leaks real tokens or breaks future refresh; two machines refreshing at once can exhaust the token; 401 recovery may force unexpected refresh mid-run.
- **Recommendation:** Viable as Hibernaut's “no real token in machine” ChatGPT-plan path after an end-to-end proxy rewrite test, but not as a pure fake-auth bypass.

### Design 3: copy real `auth.json`

- **Feasibility:** **High and officially documented.** Codex docs explicitly recommend copying `auth.json` to headless machines and trusted CI when needed.
- **Security:** **Worst.** Real access and refresh tokens sit in the machine, violating Hibernaut's credential design.
- **Operational constraints:** Use only one runner/machine per token family or serialize use; persist refreshed file; reseed on refresh failure.
- **Recommendation:** Keep as emergency/manual fallback only, with a clear warning and no golden-image capture of the real file.

## 11. Live checks (2026-10-04, codex-cli 0.160.0)

- **Setup** — codex-cli 0.160.0 from npm, in a temp prefix on the laptop (macOS), ran
  `codex exec --skip-git-repo-check "Reply with exactly OK."` with a temp `CODEX_HOME`,
  `HTTPS_PROXY` set to mitmproxy and `CODEX_CA_CERTIFICATE` set to a bundle with the mitm CA. The
  addon printed only hosts, methods, paths, statuses, header names and token shapes, and held the
  real tokens in memory only. It answered `auth.openai.com` itself and never forwarded it, so the
  laptop's own refresh token was never used. The temp files were deleted.
- **Verified live (S6)** — A placeholder login file works. Its access and ID tokens were made by
  Hibernaut's `vault.Placeholder` (the real header and claims, `exp` 2100, a random signature),
  with a random `rt.` refresh token. With only the `Authorization` bearer swapped for the real
  access token on `chatgpt.com`, the run printed OK. Codex didn't refresh and didn't rewrite
  `auth.json`.
- **Verified live** — Hosts. Every request with a token went to `chatgpt.com`, under
  `/backend-api/`: `codex/models`, `codex/responses`, `codex/analytics-events/events`,
  `plugins/featured`, `ps/plugins/…`, `ps/mcp` and `wham/…`. Requests without a token went to
  `ab.chatgpt.com/otlp/v1/metrics`, to `*.oaiusercontent.com` for files, and to the MCP connectors
  of the ChatGPT account, on their own hosts.
- **Verified live** — The model stream is a WebSocket: `GET …/codex/responses` upgraded over
  HTTP/1.1, with an HTTPS POST as the fallback. §6 found Server-Sent Events in the rust-v0.159.2
  source. Both exist. 0.160.0 tries the WebSocket first.
- **Verified live** — Tokens appear only in `Authorization`. No real or placeholder token was in
  any other header, any request or response body, or any WebSocket frame in either direction. The
  ID token and the refresh token were never sent in normal use.
- **Verified live** — Refresh behaviour, driven by 401s from the addon:
  - One 401 on the WebSocket connect: Codex reconnects without refreshing.
  - Two 401s: Codex sends `POST auth.openai.com/oauth/token` with `Content-Type:
    application/json` and `{client_id, grant_type: "refresh_token", refresh_token}`, with no
    `scope`.
  - A 200 answer with the same three placeholders: Codex retries and succeeds. It rewrites only
    `last_refresh` in `auth.json`. The tokens and `account_id` stay the same.
  - A 401 on every model request: Codex refreshes about every third attempt, falls back to HTTPS
    after five WebSocket reconnects, then exits 1 with "Could not parse your authentication token.
    Please try signing in again."
  - A refresh answered 401 with `{"error":{"code":"refresh_token_reused"}}`: exactly one refresh
    call. Codex caches the failure for the rest of the process, prints its canned "refresh token
    was already used" message, and leaves `auth.json` alone.
  - A refresh answered 503: not cached. One run sent 13 refresh calls.
- **Seen in M5's integration test** — the real codex 0.160.0 against Hibernaut's fake OpenAI:
  - At startup Codex calls `GET chatgpt.com/backend-api/wham/accounts/check`. The answer is an
    `accounts` list. Each account has an `id`, with `workspace_backend_origin` and
    `account_routing_override` set to `NO_CONSTRAINT`. A 401 there makes Codex refresh.
  - The first WebSocket frame Codex sends is about 60 KB, so anything that reads its frames needs
    a large read limit.
  - The bare binary in a release's `codex-<target>.tar.gz` warns that `codex-code-mode-host` is
    missing. The release's `codex-package-<target>.tar.gz` has it, with `bin/codex`,
    `codex-package.json`, `codex-path/rg`, `codex-resources/bwrap` and `codex-resources/zsh`.
    Codex canonicalizes its own path to find the package, so a symlink to `bin/codex` works.
    OpenAI's install.sh installs it that way.
- **Implications for Hibernaut:**
  - Install Codex's package, not the bare binary, so code mode works.
  - Bind the access token to `chatgpt.com` only. Intercept `auth.openai.com` only to answer the
    refresh. Never swap the refresh or ID token: neither is sent in normal use.
  - The token endpoint must not pass every refresh call on to OpenAI: a 503 brings a dozen more.
  - Codex keeps `account_id` from the file, so a new account needs a new file, not just a refresh
    answer.
  - The proxy doesn't scrub WebSocket frames. This check found no token in them for 0.160.0, so
    re-run it whenever the pin moves.

## Minimal v1 bake-in

- **Files:**
  - `/usr/local/share/ca-certificates/hibernaut.crt`, then run `update-ca-certificates`.
  - `/home/user/.codex/config.toml` as shown above.
  - Optional `/home/user/.codex/auth.json` containing placeholders (design 2) or real fallback (design 3 only).
- **Env:** `CODEX_HOME`, `CODEX_CA_CERTIFICATE`, `SSL_CERT_FILE`, and either `CODEX_ACCESS_TOKEN=<placeholder>` or no env token with `auth.json` present.
- **Bound hosts:** at least `auth.openai.com` and `chatgpt.com`; likely include `gov.chatgpt.com` only for FedRAMP accounts. Leave unbound telemetry/update hosts untouched unless a credential appears.
- **Smoke test:** `codex exec --skip-git-repo-check --sandbox danger-full-access "Reply with exactly OK."`

## Gaps

- **Not found:** `at-...` access tokens for ChatGPT Plus/Pro personal accounts. Docs found Business/Enterprise workspace support.
- **Not found:** Exact access-token server-side lifetime beyond docs saying finite windows, shortest custom window one day, scoped tokens must expire, older Codex-only dialog may allow no expiration.
- **Not found:** A complete production host allowlist for every optional Codex feature. Verified core: `auth.openai.com`, `chatgpt.com/backend-api/codex`, backend config/usage/files/analytics paths under ChatGPT backend.
- **Not found:** Clean plaintext extraction of OpenAI Terms/Service Terms pages with this tooling; policy/legal conclusions beyond official Codex docs and Help account-sharing page need manual legal review.
- **Answered (§11):** codex-cli 0.160.0 honours `HTTPS_PROXY`. In machines the proxy is
  transparent anyway, so Codex needs no proxy setting there.
