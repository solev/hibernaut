# M7 GitHub device flow

Status: **not started** · Spec: §4.3

**Goal:** `hib connect github` gives the host its own GitHub token through a browser approval. No
copying from the laptop, and it can be revoked on its own.

## Tasks

- [ ] Create the "Hibernaut" GitHub OAuth App by hand, with Device Flow enabled. Its client ID
      goes into `hib`. It isn't secret, and the device flow needs no client secret.
- [ ] `hibd` runs the flow; `hib` shows the code and opens `github.com/login/device`. The token
      goes straight into the vault and never touches the laptop.
- [ ] Scopes `repo`, `read:org`, `workflow`.
- [ ] Handle every response: `authorization_pending` (keep polling), `slow_down` (+5 s to the
      interval), `expired_token` (codes last 15 minutes: start over), `access_denied`,
      `incorrect_device_code`, `device_flow_disabled`.
- [ ] `hib connect github` defaults to the device flow; `--from-gh` and `--token` stay.
- [ ] README: how to revoke (github.com/settings/applications).

## Tests

Unit (fake clock):

- [ ] The polling loop for every response above, including interval changes.

Integration:

- [ ] `hib connect github` end to end against a fake GitHub OAuth server.

End-to-end:

- [ ] `TestDeviceFlow` (manual; you approve the code in the browser), then `TestGitHubFullFlow`
      passes on the new token.

## Done when

- [ ] `make lint test`, CI and `make e2e` are green, plus one manual `TestDeviceFlow` run.
- [ ] README: connecting GitHub with the device flow.

## Changes

(none yet)
