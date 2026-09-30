# M4 GitHub

Status: **not started** · Spec: §4.3, §8 (end-to-end details)

**Goal:** a fresh machine does the whole GitHub loop (clone, commit, push, open a PR) with no
login, and the token never enters it.

## Tasks

- [ ] `hib connect github --from-gh`: reads `gh auth token` and `gh api user` on the laptop.
      Stores secret `github` (`GH_TOKEN`, bound to `github.com`, `api.github.com`,
      `uploads.github.com`) and your name, ID and login.
- [ ] Scope check from the `X-OAuth-Scopes` response header. Without `workflow`, warn and suggest
      `gh auth refresh -s workflow`.
- [ ] `hib connect github --token github_pat_…` for fine-grained tokens: same path, checked with
      `GET /user`.
- [ ] `dev` image: `gh auth setup-git` for `user`; `git@github.com:` and `ssh://git@github.com/`
      rewritten to `https://github.com/`.
- [ ] `hibd` writes git `user.name` and the noreply `user.email` into each machine at create, and
      again after reconnecting.
- [ ] `hib status`: host, E2B and each secret's health. GitHub is checked with `GET /user`; a 401
      says "revoked: run `hib connect github --from-gh` again".
- [ ] Fixture repo `solev/hibernaut-e2e`: private, with a README and a workflow file.

## Tests

Unit:

- [ ] Scope parsing and warnings; the noreply address format.

Integration (fake GitHub signed by the test CA):

- [ ] Real `git` clones and pushes through the proxy to a local `git http-backend` posing as
      `github.com`. The backend sees the real token, the client only the placeholder.
- [ ] `gh api user` through the proxy against a fake API.
- [ ] `hib status` reports a revoked token (the fake answers 401).

End-to-end:

- [ ] `TestGitHubFullFlow`, on a fresh machine:
  - clone `solev/hibernaut-e2e` over HTTPS and again as `git@github.com:…`;
  - commit (author is your noreply address) and push a branch;
  - `gh pr create`, then `gh pr close --delete-branch`;
  - leak scan clean;
  - cleanup runs even when the test fails.
- [ ] `TestWorkflowPush`: push a change under `.github/workflows/` to a branch (needs `workflow`),
      then delete the branch.

## Done when

- [ ] `make lint test`, CI and `make e2e` are green.
- [ ] README: `hib connect github --from-gh`, the `workflow` scope, `hib status`.

## Changes

(none yet)
