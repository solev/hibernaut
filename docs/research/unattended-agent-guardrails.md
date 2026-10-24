# How cloud agents are kept safe when nobody watches, for Hibernaut

**Date checked:** 2026-10-24.

**Summary**

This note is a research input to Hibernaut's cloud-agent design. It examines who may start work,
which credentials and tools that work receives and what stops an unattended mistake. The survey
in `cloud-agents.md` covers the landscape. `cloud-agent-internals.md` covers execution and
`always-on-agents.md` covers persistent assistants, channels and their trust models. This note
adds enforcement details, source checks and first-party incident evidence.

- **An authorized trigger does not make its attached content trusted.** Comment authors,
  event actors and repository owners can be different principals (§A1 to §A8, §J9).
- **Credential isolation is stronger than transcript redaction.** Current Codex and Claude
  environments can inject credentials outside the VM. Cursor Runtime Secrets remain raw
  process inputs even when their displayed values are redacted (§D1 to §D6).
- **Branch prefixes are not interchangeable security boundaries.** Copilot has a scoped
  writable branch. Claude's `claude/` convention and Cursor's default branch choice are not
  equivalent restrictions (§B1 to §B4).
- **An egress policy must name its exceptions and enforcement point.** Setup, MCP, hosted
  search, GitHub proxies and secret-bound destinations can follow other paths (§C1 to §C7).
- **Unattended approval policies differ sharply.** Claude scheduled tasks use auto mode,
  Jules API plans are auto-approved by default and the OpenHands SDK defaults to no
  confirmation (§E1 to §E6).
- **Separate write jobs reduce authority but need strict contracts.** gh-aw projects requested
  writes through trusted jobs. Its advisories show why output fields, artifacts and credential
  sidecars remain security boundaries (§G1 to §G4, §J8 to §J10).
- **A cost cap, a stop button and credential revocation solve different problems.** Some caps
  are soft, some timeouts background commands and archived environments can retain live
  secret access (§D3, §H1 to §H7).

## Sources

- **GitHub Copilot.** [Risk controls][gh-risk], [firewall][gh-fw], [secrets][gh-secrets],
  [automations][gh-auto], [CLI tools][gh-tools], [limits][gh-limits],
  [sessions][gh-manage], [data][gh-data] and [runtime cap][gh-overview].
- **OpenAI Codex.** [Environments][oa-env], [legacy internet][oa-net], [GitHub][oa-gh],
  [security][oa-security] and [Agent Security][oa-policy]. [openai/codex][oa-code],
  `970111f6cb62cc02fe36a82ea7d71b37a2889c97`. Advisory: GHSA-w5fx-fh39-j5rw.
- **Claude Code.** [Web][an-web], [environments][an-env], [scheduled tasks][an-routines],
  [modes][an-modes], [permissions][an-perms], [hooks][an-hooks] and [CLI][an-cli].
  [anthropics/claude-code][an-code],
  `318b7e6ac4e726e88a4ad326214866a93c373b1b`. Advisories:
  GHSA-ff64-7w26-62rf, GHSA-jh7p-qr78-84p7 and GHSA-5j29-h97v-84ch.
- **Claude Code Action.** [Security][act-security], [source][act-code],
  `925afb53776cf1a2b738c6bdbed37b4d4000ef13`. Advisory: GHSA-8q5r-mmjf-575q.
- **Anthropic sandbox runtime.** [Source][srt-code],
  `c66fb77b28fdbea8b9788aca72377238ae67ca20`. The experimental repository redirects here.
  Advisory: GHSA-9gqj-5w7c-vx47.
- **Cursor.** [Security][cu-security], [network][cu-net], [settings][cu-settings],
  [GitHub][cu-gh], [Slack][cu-slack] and [API][cu-api]. Advisories:
  GHSA-4cxx-hrm3-49rm and GHSA-whx2-4gvm-m3r3.
- **Devin.** [Profiles][de-profiles], [secrets][de-secrets],
  [GitHub][de-gh], [Slack][de-slack], [Linear][de-linear], [automations][de-auto],
  [session creation API][de-api], [usage][de-usage] and [enterprise limits][de-limits].
- **Jules.** [Tasks][ju-tasks], [FAQ][ju-faq],
  [limits][ju-limits], [API][ju-api], [changelog][ju-changes] and
  [scheduled tasks][ju-schedules].
- **OpenHands.** [Settings][oh-settings], [secrets][oh-secrets] and [automations][oh-auto].
  [OpenHands/OpenHands][oh-code],
  `fee10e3d2909ebbf9950578575b1ee011492171e` and
  [OpenHands/software-agent-sdk][oh-sdk],
  `acd96e6fa424c02432304395b9208f99be24b54d`. Advisory: GHSA-7h8w-hj9j-8rjw.
- **GitHub Agentic Workflows.** [Architecture][aw-arch], [safe outputs][aw-outputs],
  [permissions][aw-perms], [triggers][aw-triggers], [network][aw-net] and [settings][aw-front].
  [github/gh-aw][aw-code],
  `86f50bf961338a263af17182498c4dc360afbf8c`. `githubnext/gh-aw` redirects here.
  Advisories: GHSA-jxrq-hq57-gwwm, GHSA-r8gh-v7wv-8g7h, GHSA-8h78-hpm7-29gg,
  GHSA-x78f-wrrj-4h24 and GHSA-9jxf-w4wc-5g93.
- **Supply-chain incidents.** AWS [AWS-2025-015][aws-q], CVE-2025-8217 and Nx's
  [s1ngularity postmortem][nx-post], corroborated by GHSA-cxm3-wv7p-598c.

**Tags.** **Docs**: read on the vendor's docs, changelog or blog on the date above. **Code**: read
in source at the named commit. **Unverified**: only the named secondary source says so. News and
forums served only as leads. Quotes are verbatim. "(paraphrase)" marks spots where a quote would
break this note's prose rules. "(inference)" marks my own deductions. An untagged claim takes the
tag of its sentence's citation.

[gh-risk]:
https://docs.github.com/en/copilot/concepts/security-governance-and-network-settings/risks-and-mitigations
[gh-fw]:
https://docs.github.com/en/copilot/how-tos/copilot-on-github/customize-copilot/customize-the-firewall
[gh-secrets]:
https://docs.github.com/en/copilot/how-tos/copilot-on-github/customize-copilot/customize-cloud-agent/configure-secrets-and-variables
[gh-auto]:
https://docs.github.com/en/copilot/concepts/agent-capabilities/agent-powered-workflows-and-operations/about-automations
[gh-tools]: https://docs.github.com/en/copilot/how-tos/copilot-cli/use-copilot-cli/allowing-tools
[gh-limits]:
https://docs.github.com/en/copilot/how-tos/copilot-cli/use-copilot-cli/set-session-limit
[gh-manage]:
https://docs.github.com/en/copilot/how-tos/copilot-on-github/use-copilot-agents/manage-and-track-agents
[gh-data]:
https://docs.github.com/en/copilot/concepts/security-governance-and-network-settings/session-data
[gh-overview]:
https://docs.github.com/en/copilot/concepts/copilot-surfaces/copilot-on-github
[oa-env]: https://learn.chatgpt.com/docs/environments/cloud-environments
[oa-net]: https://learn.chatgpt.com/docs/cloud/internet-access
[oa-gh]: https://learn.chatgpt.com/docs/third-party/github
[oa-security]: https://learn.chatgpt.com/docs/agent-approvals-security
[oa-policy]: https://learn.chatgpt.com/docs/enterprise/agent-security
[oa-code]: https://github.com/openai/codex/tree/970111f6cb62cc02fe36a82ea7d71b37a2889c97
[an-web]: https://code.claude.com/docs/en/claude-code-on-the-web
[an-env]: https://code.claude.com/docs/en/cloud-environments
[an-routines]: https://code.claude.com/docs/en/routines
[an-modes]: https://code.claude.com/docs/en/permission-modes
[an-perms]: https://code.claude.com/docs/en/permissions
[an-hooks]: https://code.claude.com/docs/en/hooks
[an-cli]: https://code.claude.com/docs/en/cli-reference
[an-code]: https://github.com/anthropics/claude-code/tree/318b7e6ac4e726e88a4ad326214866a93c373b1b
[act-code]:
https://github.com/anthropics/claude-code-action/tree/925afb53776cf1a2b738c6bdbed37b4d4000ef13
[act-security]:
https://github.com/anthropics/claude-code-action/blob/925afb53776cf1a2b738c6bdbed37b4d4000ef13/docs/security.md
[srt-code]:
https://github.com/anthropics/sandbox-runtime/tree/c66fb77b28fdbea8b9788aca72377238ae67ca20
[cu-security]: https://cursor.com/docs/cloud-agent/security
[cu-net]: https://cursor.com/docs/cloud-agent/security-network
[cu-settings]: https://cursor.com/docs/cloud-agent/settings
[cu-gh]: https://cursor.com/docs/integrations/github
[cu-slack]: https://cursor.com/docs/integrations/slack
[cu-api]: https://cursor.com/docs/cloud-agent/api/endpoints
[de-profiles]: https://docs.devin.ai/product-guides/security-profiles
[de-secrets]: https://docs.devin.ai/product-guides/secrets
[de-gh]: https://docs.devin.ai/integrations/gh
[de-slack]: https://docs.devin.ai/integrations/slack
[de-linear]: https://docs.devin.ai/integrations/linear
[de-auto]: https://docs.devin.ai/product-guides/automations
[de-api]: https://docs.devin.ai/api-reference/v3/sessions/post-organizations-sessions
[de-usage]: https://docs.devin.ai/admin/billing/usage
[de-limits]: https://docs.devin.ai/enterprise/features/usage-policies
[ju-tasks]: https://jules.google/docs/tasks-repos/
[ju-faq]: https://jules.google/docs/faq/
[ju-limits]: https://jules.google/docs/usage-limits/
[ju-api]: https://developers.google.com/jules/api
[ju-changes]: https://jules.google/docs/changelog/
[ju-schedules]: https://jules.google/docs/scheduled-tasks/
[oh-settings]: https://docs.openhands.dev/openhands/usage/settings/application-settings
[oh-secrets]: https://docs.openhands.dev/openhands/usage/settings/secrets-settings
[oh-auto]: https://docs.openhands.dev/openhands/usage/automations/event-automations
[oh-code]: https://github.com/OpenHands/OpenHands/tree/fee10e3d2909ebbf9950578575b1ee011492171e
[oh-sdk]:
https://github.com/OpenHands/software-agent-sdk/tree/acd96e6fa424c02432304395b9208f99be24b54d
[aw-arch]: https://github.github.io/gh-aw/introduction/architecture/
[aw-outputs]: https://github.github.io/gh-aw/reference/safe-outputs/
[aw-perms]: https://github.github.io/gh-aw/reference/permissions/
[aw-triggers]: https://github.github.io/gh-aw/reference/triggers/
[aw-net]: https://github.github.io/gh-aw/reference/network/
[aw-front]: https://github.github.io/gh-aw/reference/frontmatter/
[aw-code]: https://github.com/github/gh-aw/tree/86f50bf961338a263af17182498c4dc360afbf8c
[aws-q]: https://aws.amazon.com/security/security-bulletins/AWS-2025-015/
[nx-post]: https://nx.dev/blog/s1ngularity-postmortem
[ad-action]:
https://github.com/anthropics/claude-code-action/security/advisories/GHSA-8q5r-mmjf-575q
[ad-settings]: https://github.com/anthropics/claude-code/security/advisories/GHSA-ff64-7w26-62rf
[ad-baseurl]: https://github.com/anthropics/claude-code/security/advisories/GHSA-jh7p-qr78-84p7
[ad-symlink]: https://github.com/anthropics/claude-code/security/advisories/GHSA-5j29-h97v-84ch
[ad-codex]: https://github.com/openai/codex/security/advisories/GHSA-w5fx-fh39-j5rw
[ad-srt]: https://github.com/anthropics/sandbox-runtime/security/advisories/GHSA-9gqj-5w7c-vx47
[ad-cursor-config]:
https://github.com/cursor/cursor/security/advisories/GHSA-4cxx-hrm3-49rm
[ad-cursor-cloud]:
https://github.com/cursor/cursor/security/advisories/GHSA-whx2-4gvm-m3r3
[ad-openhands]: https://github.com/OpenHands/OpenHands/security/advisories/GHSA-7h8w-hj9j-8rjw
[ad-aw-fields]: https://github.com/github/gh-aw/security/advisories/GHSA-jxrq-hq57-gwwm
[ad-aw-trigger]: https://github.com/github/gh-aw/security/advisories/GHSA-r8gh-v7wv-8g7h
[ad-aw-artifact]: https://github.com/github/gh-aw/security/advisories/GHSA-8h78-hpm7-29gg
[ad-aw-sni]: https://github.com/github/gh-aw/security/advisories/GHSA-x78f-wrrj-4h24
[ad-aw-sidecar]: https://github.com/github/gh-aw/security/advisories/GHSA-9jxf-w4wc-5g93
[ad-nx]: https://github.com/nrwl/nx/security/advisories/GHSA-cxm3-wv7p-598c

## Comparison table

Cells summarize the cited sections. An undocumented control is not evidence that no control exists.

| Product | Who can trigger | Write scope | Egress control | Secrets exposure | Unattended approvals | Budgets and timeouts |
| --- | --- | --- | --- | --- | --- | --- |
| Copilot cloud agent | Writers, automation exceptions opt-in (§A1) | One branch, no merge (§B1) | Default Bash firewall, setup and MCP excluded (§C1) | Agent env vars, MCP-only prefix (§D1) | Autonomous tools within cloud restrictions (§E1) | 59 minutes, explicit stop (§H1) |
| Codex current cloud | Account and environment RBAC (§A2) | Task environment, GitHub operation-specific rules (§B2) | Package managers, custom or unrestricted (§C2) | Raw vars or proxy-injected network secrets (§D2) | Cloud default not established (§E2) | Published overall task cap not found (§H2) |
| Codex legacy cloud | Connected GitHub account, review configuration rights (§A2) | Can update an authorized PR branch (§B2) | Agent internet off by default, domains and methods (§C2) | Secrets setup-only (§D2) | Do not infer from CLI defaults (§E2) | Do not transfer current or CLI limits (§H2) |
| Claude web and scheduled tasks | Account capability, task token or GitHub subscription (§A3) | Branch protections, no prefix-only enforcement (§B3) | Trusted default, separate proxy exceptions (§C3) | Raw vars or host-bound network secrets (§D3) | Scheduled runs auto mode when available (§E3) | Shared usage, trigger caps, command timeouts (§H3) |
| claude-code-action | Writer gate, explicit bot and non-writer exceptions (§A4) | Workflow token and configured tools (§B3) | Runner policy, not web-session policy (§C3) | Workflow secrets, best-effort child scrub (§D4) | Tool allowlists and CLI arguments (§E4) | CLI limits plus Actions deadline (§H3) |
| Cursor Cloud Agents | User Git access, linked integrations, follow-up policy (§A5) | New branch default, current-branch opt-in (§B4) | Internet on, restrict and Enterprise lock (§C4) | Raw runtime vars with optional redaction (§D5) | Terminal commands auto-run (§E5) | Cancel API, retention differs by data type (§H4) |
| Devin | GitHub checks or configured automation identity (§A6) | Full git or read-only profile, can merge (§B5) | Machine-wide on managed VMs, operator on Outposts (§C5) | Scoped raw tool and shell access (§D6) | Security profiles, not per-command human review (§E5) | API ACU cap and enterprise usage limits (§H5) |
| Jules | Authorized repos or account API key (§A7) | Plans and PRs, authorship configurable (§B6) | Internet available, customer allowlist not verified (§C6) | MCP API keys, runtime handling not established (§D7) | Web plan gate, API auto-approval default (§E6) | Daily tasks and concurrent-task quotas (§H6) |
| OpenHands SDK and app | Deployment and automation routing (§A8) | Runtime and token scope (§B7) | Workspace/deployment boundary (§C7) | Raw runtime env, optional state encryption (§D7) | SDK NeverConfirm default (§E6) | SDK 500 iterations, app USD budget (§H6) |
| gh-aw | Trusted roles default, bot allowlist, fork gates (§A8) | Read-only agent, separate configured writes (§G) | AWF, hosted-web policy separate (§C7) | Credential sidecars and write jobs (§G3) | Configured writes can apply automatically (§G2) | Default 500 turns, 1000 credits, layered deadlines (§H7) |
| Anthropic sandbox runtime | Caller owns admission (§F1) | OS filesystem rules (§F1) | OS restriction plus filtering proxy (§F1) | Caller configuration, not inherently secret-free (§F1) | Callback or strict allowlist, not product approval (§F1) | No cloud task budget established (§H7) |
| Codex CLI sandbox | Local caller and managed policy (§E2) | Read-only, workspace or full access (§F2) | OS network mode and optional proxy (§F2) | Process credentials need separate scoping (§D2) | Never keeps sandbox, yolo removes it (§E2) | Outer task budget needed (§H2) |

## A. Who may trigger work

### A1. Copilot checks writers, but automations have an explicit exception

**Docs**, [risk controls][gh-risk]: ordinary cloud tasks require repository write access.
Comments by non-writers are not presented. [Automations][gh-auto] are limited to private and
internal repositories. Writers create them. Non-writer events are ignored by default but can
be enabled. `always-on-agents.md §D` covers the earlier trigger inventory.

**Docs**, [automations][gh-auto]: runs use the creator's attribution and selected tools.
Automation configuration is private to the creator, even against repository administrators.
Sessions and logs are repository-visible. Configuration is not stored in Git.

### A2. Codex separates account access from environment administration

**Docs**, [current environments][oa-env]: Enterprise RBAC separates using Codex in cloud from
managing environments. Tasks select environments available to the requesting account.
Slack requires both service-account and requester access. Another participant does not
automatically gain the right to continue someone else's task.

**Docs**, [GitHub integration][oa-gh]: configuring repository code review requires push or
admin rights. `@codex review` requests review. Other legacy comments can start cloud work,
but no universal comment-sender rule is documented. Legacy GitHub, Linear and Code Review
remain available and are planned for deprecation (**Docs**, [oa-env]).
`cloud-agent-internals.md §B4` covers these trigger surfaces.

### A3. Claude scheduled tasks bind authority to an account, not the event author

**Docs**, [scheduled tasks][an-routines]: the feature formerly called routines belongs to an
individual account and uses its connected identities. Owners can disable it organization-wide.
Enterprise custom roles can govern access. The old routines URL and API names remain.

**Docs**, same page: a task-scoped fire token admits run-specific text. It arrives in a
`routine-fire-payload` block marked untrusted. The saved prompt must opt into acting on it.
Token rotation or deletion is immediate.

**Docs**, same page: GitHub subscriptions require the Claude App and can filter PR fields.
No universal event-author writer check is promised. A maintainer-applied label does not
authenticate the PR body (inference). Events normally start independent sessions.
`cloud-agent-internals.md §C4` covers the earlier inventory.

### A4. claude-code-action has two distinct authorization gates

**Code**, anthropics/claude-code-action@925afb5
`src/github/validation/permissions.ts:28-71,89-161`: entity-event actors must have admin or
write access unless explicitly excepted. `workflow_run` also checks the upstream actor.
API failures normally fail closed.

**Code**, same repository, `src/github/validation/actor.ts:11-87`: a separate check resolves
the account type. `allowed_bots` defaults empty and accepts names or `*`, not human exceptions.
Tag mode calls it in `src/modes/tag/index.ts:46-48`. The writer gate precedes execution in
`src/entrypoints/prepare.ts:40-54`.

**Docs**, [action security][act-security]: schedule and dispatch rely on GitHub's event
authorization. `allowed_non_write_users` accepts names or `*` with an explicit `github_token`.
Use an expiring job token, not a static PAT. The lower-level `claude-code-base-action` lacks
these actor checks and trusted-config restoration.

### A5. Cursor checks repository access and separately controls session steering

**Docs**, [security overview][cu-security]: an administrator installs the Git provider App,
then each user links an account. Repository scope cannot exceed the user's Git access.
Protected Git Scopes can bind Git and Cursor organizations. Slack requires linking but reads
the whole thread (**Docs**, [cu-slack]). Linkage does not authenticate every quoted message
(inference).

**Docs**, [settings][cu-settings]: team follow-ups require same-team membership and the
teammate's repository access. Options are **Disabled**, **Service accounts only** and **All**.
Disabled permits only the creator to steer. The docs warn that follow-ups can operate with
another user's secrets. `cloud-agent-internals.md §A7` covers initial identity.

### A6. Devin's direct GitHub command is narrower than its automation system

**Docs**, [GitHub integration][de-gh]: a new `/devin` command must start the comment on an
open PR. The commenter needs write/admin, a linked account and authorized Devin organization
membership with session permission. Issues and closed PRs do not start this flow.
Later forwarding has an Enterprise-enforceable **Require session access for PR comments** gate.

**Docs**, [automations][de-auto]: configured GitHub issue-comment events can start work too.
They default to private repositories. Admins can expand **Automation scope** to public installed
repositories. The docs explicitly warn about public-comment and PR injection.

**Docs**, [Linear][de-linear] and [Slack][de-slack]: personal account linking maps users to
Devin. Enterprise Linear team mapping selects the organization. This is not a documented
unauthorized-sender rejection policy for every event.

**Docs**, [session API][de-api]: creation requires `org.devins.use`. Impersonating a user
requires `ImpersonateOrgSessions`, membership and the target's session permission.

### A7. Jules exposes account and repository authorization, not a universal chat gate

**Docs**, [task management][ju-tasks] and [FAQ][ju-faq]: Jules can access repositories explicitly
authorized through its GitHub installation. Users can narrow this in GitHub.

**Docs**, [API][ju-api]: callers authenticate using `X-Goog-Api-Key`. An account may create
up to three API keys. Custom Slack or Linear bridges must authenticate senders independently
(inference). A native GitHub mention or label sender rule was not established.

### A8. OpenHands and gh-aw put admission rules in different layers

**Docs**, [OpenHands automations][oh-auto]: a GitHub organization routes to a claimed OpenHands
team. Claims require OpenHands Owner and GitHub organization admin. Unclaimed events drop
silently. JMESPath filters can select actors. No universal writer-only default is stated.

**Docs**, same page: custom webhook authentication uses configured HMAC headers and secrets.
Provider signature formats still require compatibility checks.

**Docs**, [gh-aw triggers][aw-triggers]: default admitted roles are admin, maintainer and write.
Matching is exact against standard roles. Failed checks cancel with a warning. Fork PRs are
blocked by default. Bots require an allowlist, with special App-dispatch handling.

**Docs**, same page: strict mode rejects `pull_request_target` combined with roles `all` or
`read`. The confused-deputy advisory in §J9 shows why actor and content author differ.

## B. What the agent may change

### B1. Copilot enforces branch scope and a separate human merge boundary

**Docs**, [risk controls][gh-risk]: ordinary tasks create a `copilot/` branch and can push only
to it. An `@copilot` task on an existing PR instead gets that PR's branch.

**Docs**, same page: Copilot cannot approve or merge PRs or mark its draft ready for review.
Branch protections and required checks remain applicable. The requester cannot approve the PR.
Unattributed Copilot PRs need an extra approval if approvals are already required.
Rulesets enable this by default but allow opting out. Branch protections always apply it.

**Docs**, same page: Actions workflows on agent PRs require a writer's approval by default.
Repositories can opt into automatic workflow runs. Code review and CI approval are distinct.

**Docs**, [session management][gh-manage]: commits are authored by Copilot, co-authored by the
requester and signed. Commit messages link session logs.
`cloud-agent-internals.md §D3` covers the write and audit overview.

### B2. Codex has no verified cross-product branch-prefix rule

**Docs**, [GitHub integration][oa-gh]: legacy follow-up work can push a fix to the PR branch.
No universal enforced branch prefix or unconditional human-merge gate was established.
Prepared filesystems and environment sharing do not themselves define branch authorization
(inference, **Docs**, [current environments][oa-env]).

### B3. Claude's branch convention is not a proxy authorization rule

**Docs**, [scheduled tasks][an-routines]: Claude normally pushes to `claude/` branches but a
prompt can choose another. GitHub rules constrain the connected identity.
If that identity can bypass a rule, the task can too.

**Docs**, [cloud environments][an-env]: the hosted GitHub proxy rejects branch deletion and
non-branch pushes such as tags, not other branch names. Attached repositories limit API scope.
GraphQL is blocked.

**Docs**, [web sessions][an-web]: commits include a `Claude-Session` trailer and PR bodies link
to the session. `attribution.sessionUrl=false` removes that trace.
Scheduled tasks use the owner's linked identity.

**Docs**, [action security][act-security]: the default mention workflow commits its work and
offers a PR-creation link. This is not a hard prohibition on other token-authorized workflows
creating or merging PRs.

### B4. Cursor can write the starting branch when explicitly requested

**Docs**, [API][cu-api]: `workOnCurrentBranch` defaults false and creates a new `cursor/`
branch. True pushes to the starting ref or supplied PR head. `autoCreatePR` controls PR creation.
A prefix default is not a push authorization boundary (inference).

**Docs**, [security overview][cu-security]: the documented handoff is a draft PR for human
review. It says nothing merges until a person reviews but publishes no independent token-level
merge restriction. Do not substitute that narrative for branch rules (inference).

**Docs**, [secrets and network][cu-net]: GitHub commits use
`Cursor Agent <cursoragent@cursor.com>`. The triggering user is co-author when resolved.
Service accounts omit that trailer. HSM-backed Ed25519 signing supplies Verified commits.
The App includes repository, PR, issue, check and workflow capabilities.

### B5. Devin can merge unless another policy prevents it

**Docs**, [GitHub integration][de-gh]: Devin uses organization integration permissions, not
the initiating user's individual scope. Admins select repositories.
Devin can merge and the docs recommend branch protection.

**Docs**, [security profiles][de-profiles]: **Read-only** git access prevents pushes and PR
creation. **Full** permits clone, fetch, push and PR operations. Removing `gh` is separate.

**Docs**, [GitHub integration][de-gh]: PR authorship can be Devin, the user with fallback or
the user only. Some service-account paths fall back. Commit authorship is configurable.
Signing requires configured keys and matching committer identity.

### B6. Jules separates plan approval, PR creation and commit attribution

**Docs**, [API][ju-api]: `requirePlanApproval: true` enables a plan gate.
`automationMode: "AUTO_CREATE_PR"` requests automatic PR creation. Without that mode the API
does not automatically create the PR. Neither documents a merge permission boundary.

**Docs**, [changelog][ju-changes]: default commits are authored by Jules. Users can instead
choose co-authorship or user-only authorship. CI Fixer reads failed checks on Jules PRs,
fixes code and resubmits without intervention.

### B7. OpenHands write scope follows the runtime and supplied credentials

**Docs**, [application settings][oh-settings]: default git identity is `openhands` with
`openhands@all-hands.dev`. Custom authorship keeps OpenHands as co-author.

**Docs**, [secrets][oh-secrets]: a configured GitHub token becomes runtime `GITHUB_TOKEN`.
The docs describe supplying a lower-scope GitLab token to the agent separately from the
application integration token.

## C. Network egress

### C1. Copilot's default firewall is not a machine-wide firewall

**Docs**, [firewall][gh-fw]: the firewall is on by default. Required GitHub hosts are always
allowed. A recommended list is also on and covers packages, container registries,
certificate services and browser downloads.

**Docs**, same page: filtering applies to processes launched through the agent's Bash tool.
Setup and MCP-server processes are excluded. Bash filtering remains while MCP is used.
GitHub warns sophisticated bypasses remain possible. This is the Actions appliance,
not a guaranteed boundary on arbitrary external compute.

**Docs**, same page: organizations can require Enabled, Disabled or let repositories decide.
They can disable the recommended list or repository rules. Organization and repository custom
rules form a union. Repositories cannot remove organization entries.

**Docs**, same page: a domain grants that domain and subdomains. A URL rule grants its scheme,
host, path and descendant paths. Blocks appear in PR warnings.
`cloud-agent-internals.md §D3` covers the overview.

### C2. Codex current and legacy internet settings are different products

**Docs**, [legacy internet access][oa-net]: agent-phase internet is off by default.
Setup always has internet. Agent internet supports presets **None**, **Common dependencies**
and **All**, a domain allowlist and allowed methods such as GET, HEAD and OPTIONS.

**Docs**, same page: internet access creates prompt-injection, exfiltration, malware,
dependency and licensing risks. GET can still carry data in URLs or headers (inference).

**Docs**, [current environments][oa-env]: current network choices are **Package managers**,
**Custom domains only** and **All**. Package presets include registry, download and GitHub
source hosts. Root domains also allow `www`, not all subdomains.
Environment-owned network secrets add their destinations.

**Docs**, [Agent Security][oa-policy]: Enterprise requirements and defaults are distinct.
Global sets approvals and tool controls. Codex Cloud overrides supported execution settings,
including sandboxing and command networking. Environment internet restrictions apply separately.
Command allowlists do not disable hosted search, MCP or approved full sandbox escalation.

**Docs**, [environments][oa-env]: egress IPs are shared across customers. Shared environments
share a Tailscale identity. `cloud-agent-internals.md §B5` recaps the version split.

### C3. Claude's None level has explicit bypass paths

**Docs**, [cloud environments][an-env]: levels are **None**, **Trusted**, **Full** and
**Custom**. Trusted is default. HTTP/HTTPS uses a security proxy. GitHub's dedicated proxy,
Anthropic API, MCP connectors and network-secret hosts follow separate paths.

**Docs**, same page: a network-secret host can remain reachable even when the level would
otherwise deny it. None does not mean no outbound communication.
The web-session page warns that Anthropic API communication can let data leave.

**Docs**, same page: network edits reach running hosted sessions within about one minute.
Owners control shared environments. No universal personal-environment organization allowlist
is documented.

**Docs**, [web sessions][an-web] and [action security][act-security]: self-hosted sessions
and Actions workflows use operator boundaries, not hosted web settings.
`cloud-agent-internals.md §C6` covers the proxy layout.

### C4. Cursor allows internet by default, with an enforceable Enterprise lock

**Docs**, [secrets and network][cu-net]: modes are **Allow all network access**,
**Default + allowlist** and **Allowlist only**. Internet is on by default. Cursor services and
source-control providers remain reachable even in allowlist-only mode.

**Docs**, same page: environment settings can override user settings, which override team
defaults. Enterprise **Lock Network Access Policy** forces team policy.

**Docs**, same page: avoid broad regional S3 wildcards for artifact uploads. The documented
artifact destination is `cloud-agent-artifacts.s3.us-east-1.amazonaws.com`.
The git egress proxy does not govern arbitrary `gh` or API traffic.
Self-hosted machines need operator enforcement.
`cloud-agent-internals.md §A9` covers the network and secret overview.

### C5. Devin publishes a whole-machine policy, but Outposts need operator enforcement

**Docs**, [security profiles][de-profiles]: managed VM network policies apply to shell,
browser, installs and scripts. Rules support hostname patterns and IP CIDRs.
Required git-proxy destinations are automatically allowed.

**Docs**, same page: MCP allowlisting is separate. An allowed MCP server need not also appear
in the VM list. Recommended profiles are overridable. Mandatory restrictions intersect
down the hierarchy and cannot be loosened.

**Docs**, same page: the hierarchy includes Enterprise, organization, automation defaults,
automation and session. Children inherit it. Resolution occurs on boot or wake,
not automatically on edits during a running session.

**Docs**, same page: Outposts retain cloud-side git, MCP and token restrictions but do not get
a host firewall. Operators enforce queued `spec.network_policy`.
`cloud-agent-internals.md §E5` recaps execution.

### C6. Jules documents internet access but not a customer egress policy

**Docs**, [FAQ][ju-faq]: each task has a fresh VM with internet access. Google asks users to
review code and non-code components, avoid secrets and distrust third-party commands.
Customer domain rules, method restrictions and network-off were not established.

### C7. OpenHands deployment policy and gh-aw AWF are separate layers

**Docs**, [OpenHands application settings][oh-settings]: conversations can be grouped in one
sandbox with shared filesystem, credentials and compute. No universal egress policy was
established across SDK, hosted app and remote deployments.

**Docs**, [gh-aw architecture][aw-arch]: AWF separates agent and proxy networks, redirects
HTTP/HTTPS through Squid and drops capabilities. Docker is default.
Eligible hosted runners can use a VM path.

**Docs**, [gh-aw network][aw-net]: omitted `network` selects infrastructure defaults.
Explicit domains and ecosystems narrow or expand that set. Blocked entries win.
GitHub destinations are normally allowed separately. Provider-hosted search and fetch need a
distinct `hosted-web` policy outside the agent network.

**Docs**, same page: installing the engine happens outside the agent firewall. MCP gateways
can launch registry-backed servers outside it. Agent egress is not every service's egress
(inference).

## D. Secrets and credential exposure

### D1. Copilot has migrated beyond the copilot Actions environment

**Docs**, [agent secrets][gh-secrets]: current storage is the dedicated **Agents** secret and
variable type at organization and repository scope. The old `copilot` Actions environment
secrets migrate automatically to repository Agents secrets.

**Docs**, same page: ordinary agent secrets become environment variables throughout
development, including setup. Logs mask values. `COPILOT_MCP_` values serve only MCP servers.
Ordinary Actions, Codespaces and Dependabot secrets are not inherited.

**Docs**, same page: organization secrets can cover all, private/internal or selected
repositories. Lower scopes win. Masking does not deny process access (inference).

### D2. Codex distinguishes setup-only secrets from brokered network secrets

**Docs**, [legacy internet access][oa-net] and [current environments][oa-env]: legacy secrets
are setup-only and removed before agent execution. Raw variables are different.
Removal does not erase files that setup may have written using a secret (inference).

**Docs**, [current environments][oa-env]: current network secrets give programs a placeholder.
A proxy replaces it with the real credential for allowed HTTPS destinations on port 443.
This works during setup and tasks without a raw local credential.

**Docs**, same page: environment-owned network secrets add destinations to restricted policy.
Personal and direct values do not. Personal vaults supply requested keys to selected environments.
Environment sharing does not share participants' personal credentials.

**Docs**, [CLI security][oa-security]: local command sandboxing and network proxies are
separate mechanisms. The CLI does not automatically inherit hosted credential brokering.

### D3. Claude has a git broker and a different network-secret broker

**Docs**, [cloud environments][an-env]: hosted GitHub credentials and signing keys remain
outside the VM. The proxy replaces a scoped credential. `GH_TOKEN` and `GITHUB_TOKEN` default
to `proxy-injected`. Supplying a raw replacement instead exposes that value inside the VM.

**Docs**, same page: environment network secrets attach to matching requests after those
requests leave. These hosted-only secrets serve every session using the environment,
not just the user who saved them.

**Docs**, same page: network secrets do not attach to setup-script requests. The agent proxy
connects after setup. GitHub uses separate authentication. Anthropic API, public registries
and telemetry are excluded from this injection path.

**Docs**, same page: archiving an environment prevents new sessions but does not stop current
sessions or detach secrets. Delete secrets before archiving to revoke them.
Raw variables are readable by environment users and refresh on restore or rebuild.

### D4. claude-code-action limits subprocess exposure without promising perfect isolation

**Docs**, [action security][act-security]: child-process secret scrubbing is best effort.
Linux PID namespaces apply when bubblewrap is available. `CLAUDE_CODE_SUBPROCESS_ENV_SCRUB: 0`
disables scrubbing. `CLAUDE_CODE_SCRIPT_CAPS` limits named helper-script calls.

**Code**, anthropics/claude-code-action@925afb5
`base-action/src/parse-sdk-options.ts:278-294`: SDK environment construction removes
`ACTIONS_ID_TOKEN_REQUEST_URL` and `ACTIONS_ID_TOKEN_REQUEST_TOKEN`.
This removes that environment path to minting new tokens.

**Code**, same repository, `action.yml:156-159`: `show_full_output` defaults false and warns
that full JSON output can contain tool secrets. Static tokens may still be partly recovered
across runs (**Docs**, [action security][act-security]).

### D5. Cursor Runtime Secret is redacted, not absent

**Docs**, [secrets and network][cu-net]: Environment Variables are agent-visible runtime
values. Runtime Secrets, formerly Redacted Secrets, are also raw variables. `[REDACTED]`
replaces values in tool output, transcripts, commits and messages. Terminal users see raw values.

**Docs**, same page: Build Secrets use Docker build secret mounts and are not runtime
values. OIDC supplies short-lived cloud federation.

**Docs**, [settings][cu-settings]: a teammate's follow-up can steer an agent holding the
creator's secrets. Runtime code can still authenticate or transform variables (inference).

### D6. Devin scopes raw secrets to users, repositories and sessions

**Docs**, [secrets][de-secrets]: organization secrets can serve all users' sessions, while
personal secrets serve their creator. Repository secrets are encrypted and excluded from
blueprints and snapshots but available during setup and tasks. Session secrets are not reused.

**Docs**, same page: the relevant command or tool receives the value when needed.
Shell bindings need not persist. Browsers can type passwords, cookies and TOTP.
Dedicated accounts are recommended. No credential-never-visible proxy is documented.

**Docs**, [security profiles][de-profiles]: retaining `gh` requires Full git access and an
allowed GitHub API destination under network policy. Token removal is independent.

### D7. Jules and OpenHands have different documented secret surfaces

**Docs**, [Jules changelog][ju-changes]: selected MCP integrations accept service API keys.
Google audits their flow and permissions. Runtime key visibility, log redaction and setup-only
secrets were not established.

**Docs**, [OpenHands secrets][oh-secrets]: custom secrets become raw runtime environment
variables. `request_secret` avoids putting the submitted value in chat, events or tool
arguments, not runtime memory. Mid-session requests require the local backend.

**Code**, OpenHands/software-agent-sdk@acd96e6
`openhands-sdk/openhands/sdk/conversation/state.py:490-495`: a supplied cipher encrypts
saved secrets. Without it, serialization redacts and loses them.

## E. Approvals when nobody is present

### E1. Copilot cloud autonomy is not the same as CLI allow-all

**Docs**, [risk controls][gh-risk]: cloud runs act within the repository, branch and tool
restrictions above. PR review and default CI gates do not require approving every command.
Automations select tools.

**Docs**, [CLI permissions][gh-tools]: the CLI auto-allows reads, search and read-only shell
operations. Others normally prompt. `--allow-tool` grants and `--deny-tool` denies.
Deny wins over allow, saved permissions and broad grants.

**Docs**, same page: `--available-tools` limits the model-visible tool set.
`--excluded-tools` removes tools. With both, the available list wins.
`--allow-all` and `--yolo` grant all tools, paths and URLs. GitHub recommends isolation.

**Docs**, same page: the example `--allow-tool='shell(git:*)'` can be narrowed with
`--deny-tool='shell(git push)'`. Denying `write` removes file-write tools.
It does not necessarily stop separately allowed shell writes (inference).

### E2. Codex never means no prompts, not no sandbox

**Docs**, [approvals and security][oa-security]: sandbox mode and approval policy are
independent. `workspace-write` plus `on-request` permits in-workspace work and asks for
escalation. `read-only` plus `never` is documented for non-interactive use.
`never` preserves the sandbox while suppressing prompts.

**Docs**, same page: `danger-full-access` grants broad access. The
`--dangerously-bypass-approvals-and-sandbox` flag, also called `--yolo`, removes both layers.
Explicit `approval_policy = "untrusted"` is retired. Untrusted project classification is separate.

**Docs**, same page: `approvals_reviewer = "auto_review"` delegates eligible approval
to a reviewer agent. It checks only actions needing approval. Critical risk is denied.
Parse and review failures fail closed. It adds model calls, not OS isolation.

**Docs**, [Agent Security][oa-policy]: Enterprise Global requirements can restrict approval
policies and reviewers. Defaults are overridable starting values, not enforced requirements.

The reviewed cloud environment pages do not name a single equivalent approval-policy default
for hosted tasks. Do not transfer local CLI recommendations into cloud guarantees.

### E3. Claude modes and cloud scheduled defaults have changed

**Docs**, [permission modes][an-modes]: modes include `default`, `acceptEdits`, `plan`,
`auto`, `dontAsk` and `bypassPermissions`. `default` means Manual.
Interactive terminal and VS Code default to auto from v2.1.283, subject to policy and availability.

**Docs**, same page: `claude -p` without another mode starts Manual in the documented current
behavior. Allow rules, hooks or callbacks can approve otherwise denied calls.
`dontAsk` denies otherwise prompted tools. Bypass still honors explicit denies.

**Docs**, same page: web cloud sessions offer Accept edits, Plan and Auto. The Accept edits
means `default` with cloud file edits pre-approved. Bypass is unavailable.
Cloud settings cannot enable `dontAsk` or bypass.

**Docs**, [scheduled tasks][an-routines]: new hosted runs start auto mode when available.
A classifier allows or blocks actions. Ask rules and repeated blocks can still pause the run
for a person. Existing threads keep their mode.

### E4. Claude hooks can enforce policy, but ordinary hook failures can fail open

**Docs**, [permissions][an-perms]: deny, ask and allow rules are processed in that order.
Managed settings can disable bypass or auto mode. Instructions in `CLAUDE.md` are not
authorization. Bash patterns do not cover every alternate way to perform an operation.

**Docs**, [hooks][an-hooks]: `PreToolUse` can block before execution. Exit code 2 blocks on
that event. A typical exit code 1, missing script or invalid JSON is normally a non-blocking
error. `onFailure: "block"` makes supported hooks fail closed from v2.1.295.

**Docs**, same page: managed hooks cannot be disabled by a lower-scope `disableAllHooks`.
Repo hooks are executable policy. Protect their implementation (§J2, inference).

**Code**, anthropics/claude-code-action@925afb5
`base-action/src/parse-sdk-options.ts:314-344`: the action passes tool allow and deny lists,
optional max turns and CLI arguments into SDK options. Settings sources default to user,
project and local. Workflow arguments determine behavior, not web-session defaults.

### E5. Cursor and Devin expect autonomous execution inside outer restrictions

**Docs**, [Cursor security][cu-security]: terminal commands auto-run in cloud agents.
Containment uses VM isolation, egress, secrets and human PR review.

**Docs**, [Devin security profiles][de-profiles]: profiles restrict network, git, tokens and
MCP capabilities. Built-in Devin MCP can be read-only.
Profiles do not document human approval of every shell command.

### E6. Jules API and OpenHands SDK default to fewer human gates than their UIs suggest

**Docs**, [Jules API][ju-api] and [task management][ju-tasks]: the web plan flow asks a person
to approve before changes. API sessions auto-approve plans unless
`requirePlanApproval: true` is set. Planning Critic separately reviews non-human-approved plans
(**Docs**, [changelog][ju-changes]).

**Code**, OpenHands/software-agent-sdk@acd96e6
`openhands-sdk/openhands/sdk/conversation/state.py:123-127`: the SDK state defaults to
`NeverConfirm()` with no analyzer. This is not a verified hosted-app default.

**Code**, same repository,
`openhands-sdk/openhands/sdk/security/confirmation_policy.py:27-61`: alternatives are
`AlwaysConfirm` and `ConfirmRisky`. The latter defaults to High and confirms Unknown risks.
The LLM analyzer returns the action's model-supplied risk field in
`openhands-sdk/openhands/sdk/security/llm_analyzer.py:10-29`.
A model risk label is not OS access control (inference).

## F. Open-source sandbox enforcement

### F1. Anthropic sandbox runtime couples filesystem isolation to proxy routing

**Code**, anthropics/sandbox-runtime@c66fb77 `README.md:107-128`: macOS uses Seatbelt through
`sandbox-exec`. Linux uses bubblewrap, a network namespace and a Unix-socket proxy.
Both filesystem and network restrictions are needed.

**Code**, same repository, `src/sandbox/linux-sandbox-utils.ts:3698-3701,3778-3779`:
the wrapper adds `--unshare-net`. Without proxy sockets there is no network path.
Capabilities are removed at
`src/sandbox/linux-sandbox-utils.ts:768-811`.

**Code**, same repository, `src/sandbox/macos-sandbox-utils.ts:1082-1083,1325-1327`:
the generated profile starts deny-default and can allow the proxy port.
Disabling restriction permits unrestricted networking at `:1257-1259`.

**Code**, same repository, `src/sandbox/sandbox-manager.ts:767-802`: denied domains win,
then configured allows are checked. `strictAllowlist` prevents per-command grants and
callbacks from widening the list.

**Code**, same repository, `README.md:203-215`: network rule changes affect subsequent
connections. Filesystem rules are baked into each command.
Policy edits do not recall existing connections or replace running-command rules (inference).
The embedding still chooses roots, sockets and strictness.

### F2. Codex Linux now defaults to bubblewrap, not Landlock

**Docs**, [approvals and security][oa-security]: current Linux sandboxing uses bubblewrap and
seccomp. macOS uses Seatbelt. Landlock-only descriptions are outdated.

**Code**, openai/codex@970111f
`codex-rs/linux-sandbox/src/linux_run_main.rs:57-94`: the hidden legacy-Landlock option is
false by default. Bubblewrap precedes inner seccomp. Landlock is a legacy alternative.

**Code**, same repository, `codex-rs/linux-sandbox/src/bwrap.rs:106-123,289-292`:
network modes are FullAccess, Isolated and ProxyOnly. The latter two unshare the network
namespace. ProxyOnly uses a bridge. FullAccess retains host networking.

**Code**, same repository, `codex-rs/linux-sandbox/src/seccomp.rs:132-136,192-239`:
the implementation sets `no_new_privs`, restricts process-inspection calls and applies
network-specific socket rules. No-network permits Unix sockets but denies other families.

**Code**, same repository, `codex-rs/sandboxing/src/seatbelt.rs:58-62`:
macOS fixes `/usr/bin/sandbox-exec` rather than trusting PATH.
`codex-rs/sandboxing/src/seatbelt_base_policy.sbpl:7-10` starts deny-default and states that
children inherit policy.

### F3. Codex's command proxy is an additional, optional boundary

**Docs**, [approvals and security][oa-security]: network proxy is off by default.
Enabling it does not grant network. Network on without it permits direct outbound access.
With it, configured destinations are enforced.

**Docs**, same page: exact hosts, `*.example.com` and `**.example.com` have different apex
matching. Deny wins. Empty rules permit no external destinations.
Local/private addresses need explicit grants. DNS checks fail closed but remain best effort.
Supported cloud proxy paths have a distinct local-binding default (**Docs**, [oa-policy]).

**Docs**, same page: full sandbox escalation can bypass the proxy. Managed command policy
does not automatically disable MCP, apps, browser activity or hosted web search.
Those surfaces need their own requirements.

## G. Read-only agents with separate writes

### G1. gh-aw compiles an authority split into separate Actions jobs

**Docs**, [architecture][aw-arch] and [safe outputs][aw-outputs]: the agent requests structured
operations rather than receiving repository write permission. Trusted jobs validate and
apply them. This is a privilege split, not an instruction to avoid `git push`.

**Code**, github/gh-aw@86f50bf
`.github/workflows/daily-safe-outputs-conformance.lock.yml:74-75,477-486`:
the example has no workflow-wide permissions. Agent grants are `contents: read` and `issues: read`.

**Code**, same file, `:1294-1305,2539-2551`: the agent's output is uploaded as an artifact.
The separate `safe_outputs` job depends on the agent and successful detection, then receives
`discussions: write` and `issues: write`. Other workflows have different scope.

**Code**, github/gh-aw@86f50bf `pkg/workflow/compiler_safe_output_jobs.go:13-17,120-124`:
the compiler creates separate jobs. Asset publishing needs distinct git setup and
`contents: write`.

### G2. The output contract must bound fields as well as operation names

**Docs**, [safe outputs][aw-outputs]: output configuration can restrict maximum counts,
targets, labels, title prefixes and repositories. Staged output supports review.
Configured outputs may merge automatically. With no non-system output, create-issue is
auto-enabled at max one unless explicitly disabled. Safe does not mean no automatic writes.

**Code**, github/gh-aw@86f50bf
`actions/setup/js/safe_output_type_validator.cjs:777-795`: known types build a fresh
object from declared fields, not raw agent fields. Unknown types are left to the caller.
This helper alone does not admit or reject unknown operations.

**Code**, same repository, `actions/setup/js/safe_output_processor.cjs:108-158`:
the processor loads configured allowed values, maximum counts and target selection.
`actions/setup/js/safe_output_validator.cjs:120-141` filters label patterns, deduplicates them
and limits counts. Handler-specific authorization remains necessary.
`actions/setup/js/create_issue.cjs:734-737,867-873,1121-1130` additionally checks target
repositories and the processed-issue count before permitting creation.

**Docs**, [permissions][aw-perms]: read-only repository permissions are the default.
`id-token: write` is possible and can still authorize cloud access. Repository-read-only
does not mean no external write authority (inference).

### G3. Secrets can belong only to the trusted applier, but sidecars still need containment

**Docs**, [safe outputs][aw-outputs]: Linear or Jira output credentials can be supplied only
to the privileged job, absent from the agent. Custom jobs are trusted code requiring review.

**Docs**, [architecture][aw-arch]: read-only MCP reads and buffered output artifacts separate
analysis from writes. Credential sidecars create another trusted interface (§J10).

**Docs**, [frontmatter][aw-front]: secret-derived environment names are excluded from the
agent container. `env-exclude` adds names the compiler cannot infer.

### G4. Brokered authentication is related, but not equivalent

Credential proxies separate possession from authenticated operations (§D2 and §D3).
gh-aw also separates repository mutation from model execution (§G1 and §G2).
An allowed operation can still be harmful. Neither pattern eliminates injection
(inference, §J8 and §J10).

## H. Budgets, stopping and audit

### H1. Copilot has a hard cloud deadline and a soft CLI spending limit

**Docs**, [GitHub.com overview][gh-overview]: cloud sessions have a maximum execution time of
59 minutes, not extensible. `copilot-setup-steps.yml` can shorten it with `timeout-minutes`.

**Docs**, [session management][gh-manage]: **Stop session** ends the Actions run while
preserving commits. Logs show tools, progress, tokens and duration.
Cloud sessions are shared by default and can be archived, not deleted.

**Docs**, [CLI limits][gh-limits]: `--max-ai-credits` limits a non-interactive run.
Minimum is 30 credits. It is soft because the current response finishes.
Interactive `/limits set max-ai-credits` applies across messages and can be reset.
These are CLI limits, not verified cloud settings.

**Docs**, [session data][gh-data]: the cloud VM is destroyed after the session, while its log
remains. A fixed public cloud-log retention period was not established.

### H2. Codex published settings do not supply one universal cloud task budget

**Docs**, [current environments][oa-env]: saved VM state can be recovered for seven days.
This is not a runtime limit. Overall deadlines, per-task spend caps and concurrency were not
established from the reviewed pages.

**Docs**, [CLI security][oa-security]: `never` changes approval routing, not spending.
Automatic review adds usage. Sandboxing does not supply an account budget (inference).

### H3. Claude command timeouts can background work instead of killing it

**Docs**, [cloud environments][an-env]: Bash defaults to two minutes in the foreground and
can request ten. Timeout normally backgrounds, not kills. Background gets another 30 minutes.
Increasing `BASH_DEFAULT_TIMEOUT_MS` beyond that also increases the background cap.

**Docs**, same page: SessionStart command hooks default to 600 seconds. Async hooks do not
have the same enforced timeout. Roughly five-minute setup completion controls caching,
not a hard task deadline.

**Docs**, [CLI reference][an-cli]: `--max-turns` has no default limit in print mode and exits
with an error at the cap. Streamed queued messages can get new per-turn limits.
`--max-budget-usd` uses estimates, includes subagents and can overshoot.
Spend restored from earlier runs does not count toward a new invocation's cap.

**Docs**, [scheduled tasks][an-routines]: schedules have an account limit of 100 starts per
hour. API fire and Run now share 30 starts per task per hour. API fires also have a separate
100-per-account hourly limit. GitHub caps are unpublished and excess events drop.
These limit admission, not execution.

**Docs**, same page: Owners can disable scheduled tasks organization-wide and individual
tasks can pause or delete. Shared usage limits apply, with optional metered overage.

### H4. Cursor retains snapshots and transcripts on different schedules

**Docs**, [API][cu-api]: `POST /v1/agents/{id}/runs/{runId}/cancel` terminally cancels a run.
Continuation requires a new run. Usage reports include Task subagents and side chats.
Archive blocks new runs until unarchived. It is not deletion.

**Docs**, [security overview][cu-security]: snapshots expire after 30 days of inactivity.
Starting or resuming refreshes that timer. Conversation state is indefinite by default.
Deleting an agent removes transcript and artifacts but cannot delete its snapshots on demand.

**Docs**, [secrets and network][cu-net]: Enterprise retention options include indefinite and
90-day conversations. Runtime workspace recycling, conversation retention and secret storage
follow different lifecycles.

**Docs**, [security overview][cu-security]: session activity is logged and administrators can
review runs. Enterprise auth/admin events can stream to a SIEM, webhook or S3.
Overall runtime or per-run token caps were not established.

### H5. Devin combines session caps with account limits and sleeping

**Docs**, [session API][de-api]: session creation accepts `max_acu_limit`.
`resumable`, default true, preserves stopped state. The schema does not establish cost
overshoot or child-session aggregation semantics.

**Docs**, [usage policies][de-limits]: Enterprise can enable per-user monthly ACU limits.
Local/cloud usage counts together. Independent organization caps can also block work.
Review is excluded. Additional-usage approval can expand the ceiling, including automatically.

**Docs**, [usage][de-usage]: ordinary idle sessions sleep after 30 minutes by default.
Lite/API sessions sleep after five, except spawned children. Enterprise can request five to
120 minutes. Sleep consumes no usage. Idle timers do not bound active work (inference).

**Docs**, [Linear][de-linear]: its stop signal puts the task to sleep.
This is not permanent deletion. Public audit retention and emergency termination remain gaps.

### H6. Jules quotas and OpenHands iterations are different budget units

**Docs**, [Jules limits][ju-limits]: Free, Pro and Ultra allow 15, 100 and 300 daily tasks
and 3, 15 and 60 concurrent tasks. Daily accounting is a rolling 24-hour window.
Quota blocks new tasks, not management of existing tasks.

**Docs**, [Jules task management][ju-tasks]: users can pause, resume or delete tasks.
The changelog adds editing, pausing and resuming scheduled tasks. The schedule guide still
says editing is unsupported (**Docs**, [ju-changes] and [ju-schedules]).

**Docs**, [OpenHands settings][oh-settings]: a conversation USD budget stops execution at
the cap, but users can continue. It is not an irreversible account budget.

**Code**, OpenHands/software-agent-sdk@acd96e6
`openhands-sdk/openhands/sdk/conversation/state.py:108-113`: `max_iterations` defaults 500
and must be positive. This is per run, not lifetime model spend.

### H7. gh-aw has unusually explicit layered ceilings

**Docs**, [frontmatter][aw-front]: `max-turns` defaults 500 across supported engines.
AWF enforces it at the model proxy. `max-ai-credits` defaults 1000 with warning steering.
Negative disables it. `max-daily-ai-credits` is off by default and can fail open if configured.

**Docs**, same page: agentic execution defaults to 20 minutes after an environment-variable
override. Agent job default is 60 minutes and detection job default ten, also overridable.
Custom reusable-workflow caller jobs do not automatically inherit the execution deadline.

**Docs**, [triggers][aw-triggers]: `stop-after` disables future triggering after a deadline.
`manual-approval` uses GitHub environment protection on activation. Cooldown skips repeat work,
but unreadable history fails open. Rate limiting exempts writers and higher roles by default.
`ignored-roles: []` removes those exemptions.

**Docs**, [architecture][aw-arch] and [frontmatter][aw-front]: Actions jobs, artifacts and
optional OpenTelemetry export provide evidence. Retention is operator policy.
No cloud spending cap was established for standalone sandbox-runtime.

## I. Prompt injection and trusted configuration

### I1. Vendors publish mitigations, not a proof that untrusted text is harmless

**Docs**, [Copilot controls][gh-risk]: hidden text is filtered. CodeQL, secrets and dependency
checks address generated code, malware and High/Critical advisories. Some checks can be disabled.
They do not validate every completed external action (inference).

**Docs**, [Codex][oa-net] and [Cursor][cu-security]: fetched instructions can cause exfiltration.
Network restrictions contain reach rather than authenticate instructions (inference).
Cursor also recommends redaction and human review. `.cursorignore` controls context,
not a verified OS-level shell read prohibition.

**Docs**, [Jules FAQ][ju-faq]: code and non-code files feed the model. Google warns about
credentials, dependencies and third-party commands. No comprehensive injection filter was verified.

### I2. claude-code-action protects selected configuration, not all executable input

**Docs**, [action security][act-security]: the action removes hidden comments, invisible
characters, image alt text and hidden attributes. Actor comment filters give exclusion precedence.

**Docs**, same source: privileged `pull_request_target` and `workflow_run` workflows must
not blindly execute PR head content. Base configuration is restored for `.claude/`, `.mcp.json`,
`CLAUDE.md`, `.gitmodules`, `.ripgreprc` and `.husky/`. PR copies remain under `.claude-pr/`.

**Docs**, same source: package manifests, lockfiles, Makefiles and other runtime configuration
remain untrusted. A trusted base hook invoking a PR-controlled script can still execute it.
Protecting declarations without dependencies is insufficient (inference).

### I3. Social attribution and memory are not security labels

`always-on-agents.md §A3` covers @grok incidents. Its §B7, §B11 and §E2 cover OpenClaw's
tools, trust model and channel credentials. These do not supply coding-agent isolation.
Authorization does not authenticate quoted content (§A3 to §A8 and §J9, inference).
Fresh context also does not erase persisted hooks, dependencies or caches (§I2 and §J2,
inference). The wider operating concerns are in `always-on-agents.md §F`.

## J. Incidents and what failed

### J1. Claude Code Action trusted PR-controlled MCP configuration

**Docs**, [GHSA-8q5r-mmjf-575q][ad-action], published May 20, 2026, CVE-2026-47751:
versions before 1.0.74 could load attacker-controlled `.mcp.json` from a PR head during a
privileged invocation. Settings sources and project MCP crossed a configuration boundary.
Version 1.0.74 fixed it. Dependency scripts still require separate protection (§I2).

### J2. Claude's missing configuration file was not protected

**Docs**, [GHSA-ff64-7w26-62rf][ad-settings], published February 6, 2026, CVE-2026-25725:
a sandbox could create a missing settings file under a writable `.claude` parent.
Its hook could execute with host privileges on restart. Version 2.1.2 fixed absent-path protection.

### J3. Claude configuration could affect credentials before trust

**Docs**, [GHSA-jh7p-qr78-84p7][ad-baseurl], published January 20, 2026, CVE-2026-21852:
repository `ANTHROPIC_BASE_URL` configuration could apply before the trust dialog and expose
an API key. Version 2.0.65 fixed it.
[GHSA-5j29-h97v-84ch][ad-symlink], published October 5, 2026, CVE-2026-103435, describes a
permission-check versus path-resolution symlink race fixed in 2.1.129.
Both crossed configuration or path authorization boundaries.

### J4. Codex let model-selected working directories widen writes

**Docs**, [GHSA-w5fx-fh39-j5rw][ad-codex], published September 19, 2025, CVE-2025-59532:
the CLI could treat a model-chosen working directory as the writable root.
CLI 0.2.0 through 0.38.0 was affected, fixed in 0.39.0. IDE fix: 0.4.12.
Canonical scope was anchored to the user's session root. Disabled-network restrictions held.

### J5. An empty sandbox-runtime allowlist failed to mean deny

**Docs**, [GHSA-9gqj-5w7c-vx47][ad-srt], published December 4, 2025, CVE-2025-66479:
versions before 0.0.16 failed to enforce network sandboxing for an empty allowed-domain list.
Version 0.0.16 fixed it. Empty policies need enforcement testing (inference).

### J6. Cursor had both local configuration and cloud control-channel flaws

**Docs**, [GHSA-4cxx-hrm3-49rm][ad-cursor-config], published August 2, 2025,
CVE-2025-54135: creating a missing MCP settings file lacked the approval required for editing
one. Injection could reach executable configuration. Versions through 1.2.1 were affected,
fixed in 1.3.9. This local flaw is not evidence of cloud escape.

**Docs**, [GHSA-whx2-4gvm-m3r3][ad-cursor-cloud], published July 6, 2026,
CVE-2026-61613: attacker web content in browser-enabled Cloud Agent sessions could reach an
unauthenticated local agent endpoint and execute code inside that session. Session repository
data, environment credentials and GitHub App access could be exposed. Cursor fixed the
control-channel authentication March 31, 2026. Hosted customers needed no action.
This establishes session compromise, not host or cross-tenant compromise.

### J7. OpenHands had a shell-injection route outside normal agent tools

**Docs**, [GHSA-7h8w-hj9j-8rjw][ad-openhands], published March 23, 2026, CVE-2026-33718:
an authenticated git-diff endpoint interpolated a path into a shell command.
Versions through 1.4.0 were affected and 1.5.0 fixed argument handling.
This bypassed agent approvals inside the sandbox, not a documented host escape.

### J8. gh-aw forwarded undeclared fields into privileged writes

**Docs**, [GHSA-jxrq-hq57-gwwm][ad-aw-fields], published August 8, 2026:
the validator copied raw agent items, checked declared fields and forwarded additional fields.
Appliers could consume those fields to change unintended comments, PR targets or files.
Version v0.86.1 is listed as patched. Current code projects declared fields (§G2).

### J9. gh-aw admission confused the trigger actor with the content author

**Docs**, [GHSA-r8gh-v7wv-8g7h][ad-aw-trigger], published August 16, 2026:
the confused-deputy check omitted `pull_request_target`. A trusted maintainer applying a label
passed admission while the privileged agent read an external author's fork PR.
Versions through 0.86.2 were affected, fixed in v0.87.0.

### J10. gh-aw artifacts and credential sidecars exposed authority

**Docs**, [GHSA-8h78-hpm7-29gg][ad-aw-artifact], published August 27, 2026:
versions 0.83.3 through 0.85.3 could save credential-bearing git output into an artifact before
runner masking. Artifact readers could recover a configured CI trigger token.
Version 0.85.4 fixed exposure. Delete artifacts, rotate long-lived tokens and regenerate workflows.

**Docs**, [GHSA-9jxf-w4wc-5g93][ad-aw-sidecar], published October 14, 2026:
caller-controlled sidecar execution inputs and malformed API routing could expose and
exfiltrate a per-job token. The fix rejects caller environment and execution context,
uses a server-owned environment and replaces ambient credentials with a placeholder.
The body names v0.91.12 but patched metadata names v0.91.13.
Use the later floor and recompile (inference).

### J11. gh-aw's HTTPS destination check needed TLS-name validation

**Docs**, [GHSA-x78f-wrrj-4h24][ad-aw-sni], published September 23, 2026:
checking only HTTP CONNECT allowed a different TLS SNI destination behind a shared CDN.
The firewall fix validates ClientHello SNI without decrypting application traffic.
The advisory's fixed-version fields conflict with its body saying no fixed gh-aw release was
then published. Upgrade to a release with the fixed firewall and regenerate lock files.

### J12. Amazon Q's distribution pipeline admitted malicious code

**Docs**, [AWS-2025-015][aws-q], July 23, 2025, updated July 25, CVE-2025-8217:
an inappropriately scoped GitHub token in CodeBuild let an actor commit code that entered the
Amazon Q Developer VS Code extension's automatic release. Version 1.84.0 contained malicious
code and was removed. Version 1.85.0 fixed it and credentials were replaced.
AWS says a syntax error prevented that code from executing or changing customer environments.
Do not repeat claims of successful destructive execution as a vendor-confirmed incident.

### J13. Nx connected untrusted CI text, publishing credentials and local agent tools

**Docs**, [Nx postmortem][nx-post] and [GHSA-cxm3-wv7p-598c][ad-nx]:
on August 26, 2025, unsanitized PR title input in a privileged Actions flow led through a
write-capable token and release workflow to stolen npm publishing credentials.
Malicious packages scanned files and exfiltrated credentials into public GitHub repositories.
The payload also attempted local coding-agent tools. Nx says Nx Cloud was unaffected.
Nx removed malicious packages and adopted Trusted Publishers, manual release approval and
hardened CI controls. A dependency install can compromise credentials without the agent first
choosing a visibly dangerous tool (inference).

## What a host must enforce for unattended agents

1. Authenticate the trigger and record the effective principal. Separately retain the author
   and origin of issue text, PR content, comments and webhook fields. A trusted event actor
   does not promote attached content into instructions (§A1 to §A8, §J9).
2. Authorize follow-ups, retries and resumptions as well as initial creation. Recheck repository,
   session and credential ownership before another user steers a privileged run (§A2, §A5, §D5).
3. Bind writable roots and repositories outside model control. Protect missing sensitive paths,
   canonicalize final targets and enforce branch rules against the actual credential (§B1 to §B5,
   §F2, §J2 to §J4).
4. Distinguish code review, merge authority and permission to execute CI. Keep privileged workflows
   from executing attacker-controlled head scripts merely because an agent opened a PR
   (§B1, §B3, §I2, §J1, §J13).
5. Enforce egress below tool selection and enumerate every bypass path. Include setup, browser,
   MCP, hosted search, loopback control endpoints, proxies and credential-bound destinations
   in the threat model (§C1 to §C7, §F3, §J6, §J11).
6. Scope credentials by destination, operation, repository, user and lifetime. Prefer external
   injection when practical. Treat runtime environment values as exposed to runtime code even
   if transcripts are redacted (§D1 to §D7, §J10).
7. Keep approval policy and OS isolation separate. Unattended denial must fail closed.
   Validate hook failure, reviewer failure, missing executable and policy-refresh behavior
   rather than trusting mode names (§E1 to §E6, §F1 to §F3).
8. Keep policy declarations and their executable dependencies outside untrusted write scope.
   Review setup scripts, package hooks, plugins, caches and persisted configuration as code
   that can act before or after the model loop (§D4, §I2, §I3, §J1 to §J3, §J13).
9. Give an agent only read authority when the job permits it. Put writes in a separately
   privileged applier with a closed field schema, bounded targets and explicit operation limits.
   Treat its artifacts and sidecar interfaces as hostile input (§G1 to §G4, §J8, §J10).
10. Enforce admission rates, model spend, iterations, wall time and concurrency independently.
    Include child work, retries, resumptions and background commands in accounting.
    Document soft ceilings and exemptions (§H1 to §H7).
11. Provide separate operations to stop execution, prevent future starts, revoke credentials
    and delete retained state. Verify what each does to live commands, snapshots, transcripts
    and external side effects (§D3, §H1, §H3 to §H6).
12. Preserve attributable evidence without publishing credentials. Protect artifacts before
    masking, set retention explicitly and test revocation after an incident.
    Upgrade compiled runtimes as well as local tools (§B1, §B4, §H4, §J10, §J11).

## Gaps

- **Hosted approval and authority details.** The reviewed Codex current-cloud pages did not
  establish one approval-policy default, hard task runtime or per-task spend ceiling.
  GitHub integration docs did not establish one permission rule for every non-review trigger.
- **Native integration sender rejection.** Jules GitHub mention or label authorization was
  not established. Devin Linear and automation docs describe linking and routing but not a
  complete unauthorized-sender rejection matrix. OpenHands event docs do not publish a
  universal writer-only default. No live credentialed trigger tests were performed.
- **Jules internals.** Customer egress settings, arbitrary-secret runtime exposure, merge-token
  restrictions, hard task deadlines and log retention were not established from public docs.
  Its changelog and schedule guide disagree about editing scheduled tasks (§H6).
- **Cloud merge and branch enforcement.** Cursor documents human review and a draft PR
  handoff, but a separate token-level merge prohibition was not verified. Codex did not supply
  a universal branch-prefix rule. Claude explicitly relies on connected-identity branch rules.
- **Audit and kill contracts.** Fixed public log-retention periods were not established for
  Copilot cloud, Claude cloud, Devin or Jules. Cursor's runtime cap and Devin's universal
  termination and credential-revocation behavior remain unclear. Idle sleep is not a kill.
- **Implementation versus hosted configuration.** Pinned OSS code proves the inspected
  mechanisms and defaults, not the flags, patches or deployment settings used by every
  vendor-hosted service. OpenHands SDK defaults are not asserted as hosted-app defaults.
- **Advisory version conflicts.** gh-aw's sidecar advisory body and patched metadata differ.
  Its TLS-name advisory also has conflicting release wording. The note reports both rather
  than inventing a clean boundary (§J10 and §J11). Remediation requires regenerated workflows.
- **Scope of assurance.** This was documentation, advisory and source research. No exploit,
  vendor agent CLI, paid account or secret-bearing runtime was exercised. No claim here proves
  complete prompt-injection resistance or an absence of unpublished controls.
