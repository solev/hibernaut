# M5 AI tools

Status: **in progress** · Spec: §4.1, §4.2 (Token endpoints), §4.3, §4.4 · Spikes: S6 and S9,
both answered

**Goal:** `codex` and `copilot` work in every machine with no login, and no real ChatGPT or GitHub
token ever enters a machine.

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

- [ ] `images/dev/versions` pins Codex 0.160.0 and the Copilot CLI 1.0.91 by version and SHA-256.
      The host reads the same file.
- [ ] Both installed from their release tarballs into `/usr/local/bin`.
- [ ] `~/.codex/config.toml`: `approval_policy = "never"`, `sandbox_mode = "danger-full-access"`,
      `check_for_update_on_startup = false`, analytics off, and
      `cli_auth_credentials_store = "file"`.
- [ ] `COPILOT_AUTO_UPDATE=false`, and Copilot's settings without the banner.

**Proxy**

- [ ] Scrub-only secrets: their sites are intercepted and responses are scrubbed of them, but they
      are never swapped. Codex's refresh and ID tokens are scrub-only.
- [ ] Token endpoints: the proxy answers a few requests itself. It reads their bodies, capped, and
      audits them. Codex's refresh is the only one.
- [ ] A site's 401 to a swapped token is reported, so a refresh happens only when it's needed.

**Codex (ChatGPT plan)**

- [ ] The pinned Codex installed on the host, used only for the device login.
- [ ] `hib connect codex`: `hibd` runs `codex login --device-auth` with a throwaway `CODEX_HOME`
      and a clean environment, and `hib` shows its output (the code and link) as it comes. The
      tokens go into the vault as `codex-access`, `codex-refresh` and `codex-id`, then the
      directory is deleted. Connecting again replaces the login. A clear hint if device login
      isn't turned on in ChatGPT's security settings.
- [ ] Placeholder login file: the real claims, a far-future `exp` and fake signatures, and a
      placeholder refresh token. `hibd` writes it to `~/.codex/auth.json` (mode 0600, owned by
      `user`) on create, on resume, and when the placeholders change.
- [ ] `codex-access` is swapped on `chatgpt.com`. `codex-refresh` and `codex-id` are scrub-only,
      bound to `auth.openai.com`.
- [ ] Refresher in `hibd daemon`: at half the token's lifetime, one refresh at a time across
      processes. The new tokens are in the vault before anything uses them, and old values stay in
      the leak scan. A refresh with an unknown outcome is never retried by itself.
- [ ] Token endpoint: the proxy answers `POST auth.openai.com/oauth/token` itself, as §4.4 says.
      It never goes to OpenAI.
- [ ] `hibd codex refresh [-force]`, so tests can make a real refresh.
- [ ] `hib status`: the account, the plan, the expiry, and whether the login is dead or stuck,
      with "run `hib connect codex`".
- [ ] `hib secret` refuses names that start with `codex-`.

**Copilot CLI**

- [ ] `*.githubcopilot.com` joins the GitHub secret's sites.
- [ ] `hibd daemon` adds it to a GitHub connection made before M5.

## Tests

Unit (fake clock):

- [ ] The placeholder login file has the layout Codex writes. Its JWTs decode the way Codex
      decodes them (payload only), with the real claims and a far-future `exp`.
- [ ] Refresher: rotates and saves, keeps placeholders that still fit, and retires old values.
      Many concurrent refreshes cause one upstream refresh. Each outcome (not sent, 200, 401, 400
      `invalid_grant`, 429, 5xx, no answer, a malformed answer, a crash mid-refresh) leaves the
      right health and retry time.
- [ ] Token endpoint: each answer in §4.4, audited first, scrubbed, and never forwarded.
- [ ] Scrub-only secrets are scrubbed but never swapped.

Integration (fake `chatgpt.com` and `auth.openai.com`, signed by the test CA, and the pinned
`codex`. CI installs it. Locally these skip without it.):

- [ ] Real `codex exec` through the proxy: the fake backend sees the real access token, the
      client only placeholders.
- [ ] The fake backend answers 401. Codex refreshes, the proxy answers locally, and the retry
      succeeds. The fake auth server sees exactly one refresh, from `hibd`.
- [ ] Ten clients refreshing at once cause one upstream refresh.

End-to-end:

- [ ] `TestCodex`: in a fresh machine, `codex exec --skip-git-repo-check "Reply with exactly OK."`
      prints OK with no login prompt.
- [ ] `TestCodexRefresh`: `sudo hibd codex refresh -force` makes a real refresh. Codex still works
      in an existing machine and in a new one.
- [ ] `TestCopilot`: in a fresh machine,
      `copilot -p "Reply with exactly OK." -s --allow-all-tools --no-ask-user` prints OK.
- [ ] The leak scan after all three covers `github`, `codex-access`, `codex-refresh` and
      `codex-id`, old values included, and finds nothing.

## Done when

- [ ] `make lint test`, `make test-host-remote`, CI and `make e2e` are green.
- [x] Spec: S6 and S9 marked answered, and bound sites updated.
- [ ] README: `hib connect codex`, and Copilot working through the GitHub connection.

## Changes

- 2026-09-30: `hib connect codex --access-token` moved to Later. Our ChatGPT account is Plus, which
  can't create Codex access tokens, so there's nothing to test it against.
- 2026-10-04: S9 traced live: Copilot sends the GitHub token itself, with no exchange. So the
  exchange tasks and tests are gone, and `*.githubcopilot.com` joins the GitHub secret's sites.
  S6 checked live with codex-cli 0.160.0, which is now the pinned version. After a plan review:
  three managed Codex secrets (two of them scrub-only), refreshes at half the lifetime, refreshes
  with an unknown outcome never retried by themselves, the token endpoint's answers, and
  `hibd codex refresh` for tests.
