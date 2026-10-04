# M7 GitHub device flow

Status: **in progress** · Spec: §4.3

**Goal:** `hib connect github` gives the host its own GitHub token through a browser approval. No
copying from the laptop, and it can be revoked on its own.

## Design

The M7 code is built against this section. Code that has to differ updates it in the same commit.

**What GitHub gives us** (GitHub's docs, checked 2026-10-04):

- `POST https://github.com/login/device/code` with `client_id` and `scope`, space-separated,
  returns `device_code`, which is secret, `user_code` like `WDJB-MJHT`, `verification_uri`
  (`https://github.com/login/device`), `expires_in` (900) and `interval` (5). There is no client
  secret, and no `verification_uri_complete`.
- `POST https://github.com/login/oauth/access_token` with `client_id`, `device_code` and
  `grant_type=urn:ietf:params:oauth:grant-type:device_code` returns `access_token`, `token_type`
  and `scope`, comma-separated. Errors are `{"error": ...}`, on HTTP 200 or 400.
- The error codes are `authorization_pending`, `slow_down` (5 s more, with the new `interval`),
  `expired_token`, `unsupported_grant_type`, `incorrect_client_credentials`,
  `incorrect_device_code`, `access_denied` and `device_flow_disabled`.
- An app may ask for 50 codes an hour. GitHub keeps at most 10 tokens per user, app and scope set,
  and revokes the oldest. Expiring tokens, which last 8 hours and come with a refresh token, are
  off unless the app turns them on.
- An org that restricts OAuth Apps hides its repos from the token until an owner approves the
  app.

**The flow:**

1. `hib connect github`, with no flag, runs `sudo hibd connect github --source device --json` over
   SSH. It holds hibd's stdin open and sends nothing on it.
2. hibd takes the GitHub login lock, `github-login.lock` in its state directory, without waiting.
   Every source takes it, so two connects can't race to store. A busy lock fails with "another
   hib connect github is running: finish it, or stop it, then try again".
3. hibd checks that the vault can take the token, as for `--from-gh` and `--token`, then asks
   GitHub for a code for the OAuth App `github.ClientID`, with the scopes `repo`, `read:org` and
   `workflow`.
4. hibd prints `hibapi.GitHubDevicePrompt` to stderr, then "Waiting for GitHub. Press Ctrl-C to
   cancel." hib streams hibd's stderr as always. When it sees a whole line that
   `hibapi.IsGitHubDevicePrompt` matches, it opens `hibapi.GitHubDeviceURL` in a browser.
5. hibd polls until the user approves or denies the code, the code expires, or hibd's stdin ends.
   It checks an approved token with `GET /user` and stores it as it stores a pasted one, with the
   source `device`. hib prints what it prints for the other sources.

**Talking to GitHub** (`github.DeviceFlow`):

- The device code stays private. No exported type holds it, and neither it nor the token is ever
  in an error or a log.
- GitHub's answer is checked before use. `user_code` must match
  `^[A-Za-z0-9]{1,16}(-[A-Za-z0-9]{1,16}){0,3}$`, `verification_uri` must be exactly
  `hibapi.GitHubDeviceURL`, and `device_code` must match `^[A-Za-z0-9_-]{1,255}$`. `expires_in`
  must be 1 to 3600 and `interval` 0 to 900, checked as integers. An interval of 0 means 5 s.
- The deadline is fixed when the code arrives, at that time plus `expires_in`. Each round waits
  the interval, cut short at the deadline, then checks the deadline again before it polls.
- `authorization_pending` keeps the interval. `slow_down` makes it the larger of the interval
  plus 5 s and GitHub's new `interval`. GitHub's interval is never capped.
- A transport error, a timeout, a 5xx or a 429 is retried until the deadline. The wait doubles,
  from the interval up to a minute, and drops back to the interval after any other answer. If the
  code expires after a failed poll, the error says why the poll failed.
- Every other answer is final. `expired_token` and the deadline are `ErrCodeExpired`.
  `access_denied` is `ErrAccessDenied`. `device_flow_disabled` and
  `incorrect_client_credentials` are `ErrAppRefused`. Any other code is "GitHub refused the
  device login (CODE)", with CODE shown only if it matches `^[a-z_]{1,64}$`. `error_description`
  is never shown.
- An error in a reply wins over a token in it. A redirect, or a 4xx without an error, is final.
  The code request maps errors the same way, and isn't retried.
- The token must pass `CheckToken`. A reply with `refresh_token` or `expires_in` is
  `ErrExpiringToken`, because hibd can't refresh tokens.
- Requests send `Accept: application/json` and `User-Agent: hibernaut-hibd/VERSION`, read at most
  1 MiB, and never follow a redirect, even with an injected client.
- An empty client ID is `ErrNoClientID`. `github.ClientID` is empty until the OAuth App exists.
  `App.GitHubDeviceFlow` replaces the flow in tests.

**Cancelling:**

- hibd stops polling when its stdin ends. hib holds stdin open with a pipe that closes when hib
  returns or dies, and ssh passes the end on, even over a shared connection. So Ctrl-C in hib, or
  a dead hib, stops hibd.
- hib runs ssh with `-o StdinNull=no`, so a config that sets it can't end hibd's stdin early.
- A network partition that SSH doesn't notice leaves hibd polling until the code expires, at
  most 15 minutes. Meanwhile the lock tells anyone who tries again.
- hibd stores a token GitHub has granted even if hib has gone. hib can't tell whether that
  happened, so on Ctrl-C it says "run hib status to see whether GitHub is connected".
- Run by hand, `sudo hibd connect github --source device` stops at Ctrl-C or Ctrl-D.

**Messages** (hibd's, which hib shows after its own prefix):

- Expired: "the GitHub code expired. Run hib connect github again for a new code".
- Denied: "the GitHub login was denied. Nothing was stored".
- Cancelled: "the GitHub login was cancelled. Nothing was stored".
- App refused: "GitHub refused the Hibernaut OAuth App. Its owner must turn on Device Flow at
  github.com/settings/developers. Until then, run hib connect github --from-gh or --token".
- No client ID: "this hibd has no GitHub OAuth App to log in to. Run hib connect github --from-gh
  or --token".
- Expiring token: "GitHub gave a token that expires, and hibd can't refresh tokens. Turn off
  expiring tokens for the Hibernaut OAuth App, then run hib connect github again".

**hib:**

- `hib connect github [--from-gh | --token]`. No flag runs the device flow. Both flags give
  "choose at most one of --from-gh and --token".
- It waits up to 17 minutes, two more than a code lasts.
- It opens the browser only when stderr is a terminal: `open` on macOS, and `xdg-open` on Linux
  when `DISPLAY` or `WAYLAND_DISPLAY` is set. The opener gets no stdin, stdout or stderr, and
  10 s. It runs at most once, only for `hibapi.GitHubDeviceURL`. If it fails, hib says so in a
  note and keeps waiting.

**Later:** a cancelled `hib connect codex` leaves the codex login polling on the host until its
code expires, holding its lock. The same stdin watch would stop it.

## Tasks

- [ ] Create the "Hibernaut" GitHub OAuth App by hand, with Device Flow enabled. Its client ID
      goes into `hibd`, as `github.ClientID`. It isn't secret, and the device flow needs no
      client secret.
- [ ] `hibd` runs the flow, and `hib` shows the code and opens `github.com/login/device`. The
      token goes straight into the vault and never touches the laptop.
- [ ] Scopes `repo`, `read:org`, `workflow`.
- [ ] Handle every response: `authorization_pending` (keep polling), `slow_down` (+5 s to the
      interval), `expired_token` (codes last 15 minutes: start over), `access_denied`,
      `incorrect_device_code`, `device_flow_disabled`.
- [ ] `hib connect github` defaults to the device flow. `--from-gh` and `--token` stay.
- [ ] Ctrl-C in `hib` stops `hibd`, and one GitHub login runs at a time, for every source.
- [ ] README: how to revoke (github.com/settings/applications), and org approval.

## Tests

Unit (fake clock):

- [ ] The polling loop for every response above, including interval changes.
- [ ] Bad answers from GitHub, redirects, retries up to the deadline, and errors that never
      quote the device code or the token.
- [ ] `hibd`: the lock, the end of stdin, each message, and the token stored with the source
      `device`.
- [ ] `hib`: the flags, the held stdin, `StdinNull`, and the browser opening once.

Integration:

- [ ] `hib connect github` end to end against a fake GitHub OAuth server.

End-to-end:

- [ ] `TestDeviceFlow` (manual: you approve the code in the browser), then `TestGitHubFullFlow`
      passes on the new token.

## Done when

- [ ] `make lint test`, `make test-host-remote`, a clean `make generate` and `make e2e` are green,
      plus one manual `TestDeviceFlow` run. CI is parked, so `make lint test` stands in for it.
- [ ] Spec: §4.3 matches the code.
- [ ] README: connecting GitHub with the device flow.

## Changes

- 2026-10-04: After a plan review: the device code never leaves `github.DeviceFlow`, GitHub's
  answers are checked before use, transient poll errors are retried until the code expires, hib
  holds hibd's stdin open so Ctrl-C stops the login, one GitHub login runs at a time, and hib
  opens only the one device URL. The client ID lives in `hibd`, not `hib`, since `hibd` runs the
  flow.
