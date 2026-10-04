# M5 AI tools

Status: **in progress** · Spec: §4.2 (Token endpoints), §4.4 · Spikes: S9 (first), S6

**Goal:** `codex` and `copilot` work in every machine with no login, and no real ChatGPT, GitHub
or Copilot token ever enters a machine.

## Tasks

**Spike first (S9)**

- [ ] Trace one `copilot -p` run through an intercepting proxy (mitmproxy on the laptop is enough):
      hosts, which token goes where, any token exchange (endpoint, response fields, lifetime), and
      what gets written under `~/.copilot`. The capture holds real tokens: keep it in a temp dir
      and delete it afterwards.
- [ ] Same for one `codex exec` run on a ChatGPT login: confirm the token only goes to
      `chatgpt.com` and `auth.openai.com`.
- [ ] Write the findings into `docs/research/copilot-auth.md` and the spec (S9, bound sites).

**Image**

- [ ] Codex and the Copilot CLI installed at pinned versions.
- [ ] `~/.codex/config.toml`: `approval_policy = "never"`, `sandbox_mode = "danger-full-access"`,
      `check_for_update_on_startup = false`, analytics off.
- [ ] `COPILOT_AUTO_UPDATE=false`, and Copilot's settings without the banner.

**Codex (ChatGPT plan)**

- [ ] Codex installed on the host (pinned), used only for the device login.
- [ ] `hib connect codex`: `hibd` runs `codex login --device-auth` with a throwaway `CODEX_HOME`,
      and `hib` shows the code and link. The tokens go into the vault, then the file is deleted.
      A clear hint if device login isn't turned on in ChatGPT's security settings.
- [ ] Placeholder login file: the real JWT header and claims, far-future `exp`, fake signature,
      and a placeholder refresh token. `hibd` writes it to `~/.codex/auth.json` (mode 0600, owned
      by `user`) whenever it writes the `/etc/environment` block.
- [ ] Access and ID token placeholders bound to `chatgpt.com` and `auth.openai.com`, always
      swapped for the current real value.
- [ ] Refresher: `hibd` refreshes the real token before it expires, one refresh at a time. The
      new refresh token is saved to the vault before anything uses it.
- [ ] Token endpoint: a `POST auth.openai.com/oauth/token` carrying a placeholder is answered by
      the proxy itself with the same placeholders, once the real token is fresh. Never forwarded.
- [ ] `hib status`: Codex login health. A used-up or expired refresh token says
      "run `hib connect codex` again".

**Copilot CLI**

- [ ] Uses the GitHub secret's `GH_TOKEN` placeholder; bind Copilot's hosts to it, per the S9
      trace.
- [ ] If S9 shows a token exchange: capture it in the proxy. The real Copilot token stays on the
      host until it expires, the machine gets a look-alike placeholder, and the proxy swaps it back
      on Copilot's hosts.

## Tests

Unit (fake clock):

- [ ] Placeholder JWTs decode the way Codex decodes them (payload only), with the real claims and
      a far-future `exp`.
- [ ] Refresher: rotates and saves; many concurrent requests cause one refresh; a reused or
      expired refresh token makes `hib status` say reconnect.

Integration (fake `auth.openai.com`, `chatgpt.com` and Copilot servers, signed by the test CA):

- [ ] Real `codex exec` through the proxy: the fake backend sees the real access token, the
      client only placeholders.
- [ ] The fake backend answers 401 → Codex refreshes → the proxy answers locally → the retry
      succeeds. The fake auth server sees exactly one refresh, from `hibd`.
- [ ] Ten machines refreshing at once cause one upstream refresh.
- [ ] If S9 shows an exchange: real `copilot -p` through the proxy. The exchange response the
      client gets holds a placeholder; the fake model host sees the real Copilot token.

End-to-end:

- [ ] `TestCodex`: fresh machine → `codex exec --skip-git-repo-check "Reply with exactly OK."`
      prints OK with no login prompt.
- [ ] `TestCodexRefresh`: `hibd` forces a real refresh → Codex still works in an existing machine
      and in a new one.
- [ ] `TestCopilot`: fresh machine →
      `copilot -p "Reply with exactly OK." -s --allow-all-tools --no-ask-user` prints OK.
- [ ] Leak scan after all three finds no real ChatGPT, GitHub or Copilot token.

## Done when

- [ ] `make lint test`, CI and `make e2e` are green.
- [ ] Spec: S6 and S9 marked answered; bound sites updated.
- [ ] README: `hib connect codex`, and Copilot working through the GitHub connection.

## Changes

- 2026-09-30: `hib connect codex --access-token` moved to Later. Our ChatGPT account is Plus, which
  can't create Codex access tokens, so there's nothing to test it against.
