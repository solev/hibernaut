# How users start cloud-agent tasks, for Hibernaut

**Date checked:** 2026-10-27.

**Summary**

- **HTTPS task kickoff is a real product surface.** Copilot, Cursor, Jules and Devin document
  public task creation. Claude Code documents a narrower saved-task trigger. **Docs**, §§A to F.
- **A vendor CLI need not use its public REST API.** `gh agent-task create` uses Copilot's jobs
  backend, not the public agent tasks preview. **Code**, §A2.
- **Codex cloud has a CLI without a verified public creation API.** Its source calls ChatGPT's
  backend with account credentials. That is not an OpenAI Platform API contract.
  **Code** and **Unverified**, §C2.
- **Phones are clients, not execution hosts.** GitHub Mobile, Cursor for iOS, ChatGPT for iOS
  and Claude's mobile apps can start remote work. Their exact HTTP paths remain unverified.
  **Docs** and **Unverified**, §§A to F.
- **The initiating identity matters more than the channel.** Human tokens, service accounts
  and linked chat identities have different permissions and usage attribution.
  **Docs** (inference), §§A3, B3, C3, E3 and F3.
- **Claude Code and Managed Agents are separate contracts.** A routine token starts one saved
  Claude Code task. A Console API key starts developer-defined Managed Agents sessions.
  **Docs**, §F3.

**Docs.** This extends [agent task APIs][apis] §§D to I and [ingress and triggers][ingress]
§§M to Q. Those notes own resource shapes and GitHub App triggers. This adds surfaces and auth.

## Sources

**Tags.** **Docs** means a first-party document. **Code** means inspected source at an immutable
commit, linked to its file and line range. **Unverified** means the checked sources do not
establish the claim. It does not mean the feature cannot exist. Conclusions say "(inference)".
Only public documentation and source were read. No authenticated API or agent task was started.

[apis]: agent-task-apis.md
[ingress]: ingress-and-triggers.md
[hib-spec]: ../spec.md#6-decisions
[gh-start]:
https://docs.github.com/en/copilot/how-tos/use-copilot-agents/cloud-agent/start-copilot-sessions
[gh-cli]:
https://docs.github.com/en/copilot/how-tos/use-copilot-agents/cloud-agent/use-cloud-agent-from-cli
[gh-api]: https://docs.github.com/en/rest/agent-tasks/agent-tasks
[gh-assign]:
https://docs.github.com/en/copilot/how-tos/use-copilot-agents/cloud-agent/use-cloud-agent-via-the-api
[gh-mobile]:
https://docs.github.com/en/copilot/how-tos/use-copilot-agents/cloud-agent/use-cloud-agent-on-mobile
[gh-delegate]:
https://docs.github.com/en/copilot/how-tos/copilot-cli/use-copilot-cli/delegate-tasks-to-cca
[gh-mcp]:
https://docs.github.com/en/copilot/how-tos/use-copilot-agents/cloud-agent/use-cloud-agent-with-mcp
[gh-auth]: https://cli.github.com/manual/gh_auth_login
[gh-rate]: https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api
[gh-about]: https://docs.github.com/en/copilot/concepts/copilot-surfaces/copilot-on-github
[gh-discovery]:
https://github.com/cli/cli/blob/9be5608274eca527fbdf54020ead268ff605bad3/pkg/cmd/agent-task/shared/capi.go#L21-L68
[gh-job]:
https://github.com/cli/cli/blob/9be5608274eca527fbdf54020ead268ff605bad3/pkg/cmd/agent-task/capi/job.go#L55-L94
[gh-transport]:
https://github.com/cli/cli/blob/9be5608274eca527fbdf54020ead268ff605bad3/pkg/cmd/agent-task/capi/client.go#L74-L90
[gh-mcp-code]:
https://github.com/github/github-mcp-server/blob/f38526264abb14961b32cea386e38ec6d3cf674f/pkg/github/copilot.go#L155-L205
[gh-mcp-mutation]:
https://github.com/github/github-mcp-server/blob/f38526264abb14961b32cea386e38ec6d3cf674f/pkg/github/copilot.go#L346-L373
[cursor]: https://cursor.com/docs/cloud-agent
[cursor-cli]: https://cursor.com/docs/cli/overview
[cursor-cli-auth]: https://cursor.com/docs/cli/reference/authentication
[cursor-api]: https://cursor.com/docs/api
[cursor-sdk]: https://cursor.com/docs/sdk/typescript
[cursor-python]: https://cursor.com/docs/sdk/python
[cursor-service]: https://cursor.com/docs/account/enterprise/service-accounts
[cursor-slack]: https://cursor.com/docs/integrations/slack
[cursor-linear]: https://cursor.com/docs/integrations/linear
[cursor-auto]: https://cursor.com/docs/cloud-agent/automations
[cursor-mcp]: https://cursor.com/docs/cloud-agent/capabilities#cursor-cloud-mcp
[codex-cloud]: https://learn.chatgpt.com/docs/cloud.md
[codex-cli]: https://learn.chatgpt.com/docs/developer-commands?surface=cli
[codex-mobile]: https://learn.chatgpt.com/docs/mobile.md
[codex-auth]: https://learn.chatgpt.com/docs/auth.md
[codex-tokens]: https://learn.chatgpt.com/docs/enterprise/access-tokens.md
[codex-service]: https://learn.chatgpt.com/docs/enterprise/service-accounts.md
[codex-slack]: https://learn.chatgpt.com/docs/third-party/slack.md
[codex-linear]: https://learn.chatgpt.com/docs/third-party/linear.md
[codex-schedule]: https://learn.chatgpt.com/docs/automations.md
[codex-pricing]: https://learn.chatgpt.com/docs/pricing.md
[codex-sdk]: https://learn.chatgpt.com/docs/codex-sdk
[codex-commands]:
https://github.com/openai/codex/blob/5c6ef3d1a6769a3ea78080792bd67a2c42affdcc/codex-rs/cloud-tasks/src/cli.rs#L15-L60
[codex-init]:
https://github.com/openai/codex/blob/5c6ef3d1a6769a3ea78080792bd67a2c42affdcc/codex-rs/cloud-tasks/src/lib.rs#L54-L116
[codex-paths]:
https://github.com/openai/codex/blob/5c6ef3d1a6769a3ea78080792bd67a2c42affdcc/codex-rs/backend-client/src/client.rs#L475-L524
[codex-create]:
https://github.com/openai/codex/blob/5c6ef3d1a6769a3ea78080792bd67a2c42affdcc/codex-rs/backend-client/src/client.rs#L607-L618
[codex-auth-code]:
https://github.com/openai/codex/blob/5c6ef3d1a6769a3ea78080792bd67a2c42affdcc/codex-rs/model-provider/src/auth.rs#L386-L403
[codex-bearer]:
https://github.com/openai/codex/blob/5c6ef3d1a6769a3ea78080792bd67a2c42affdcc/codex-rs/model-provider/src/bearer_auth_provider.rs#L34-L45
[jules-api]: https://developers.google.com/jules/api
[jules-cli]: https://jules.google/docs/cli/reference.md
[jules-auth]: https://jules.google/docs/api/reference/authentication.md
[jules-limits]: https://jules.google/docs/usage-limits.md
[jules-schedule]: https://jules.google/docs/scheduled-tasks.md
[jules-client]:
https://github.com/google-labs-code/jules-sdk/blob/19fc117ac5cb961f393257b99d0b939848e75b52/packages/core/src/client.ts#L112-L128
[jules-create]:
https://github.com/google-labs-code/jules-sdk/blob/19fc117ac5cb961f393257b99d0b939848e75b52/packages/core/src/client.ts#L641-L648
[jules-header]:
https://github.com/google-labs-code/jules-sdk/blob/19fc117ac5cb961f393257b99d0b939848e75b52/packages/core/src/api.ts#L72-L102
[jules-mcp]:
https://github.com/google-labs-code/jules-sdk/blob/19fc117ac5cb961f393257b99d0b939848e75b52/packages/mcp/src/tools/create-session.tool.ts#L1-L54
[jules-mcp-call]:
https://github.com/google-labs-code/jules-sdk/blob/19fc117ac5cb961f393257b99d0b939848e75b52/packages/mcp/src/functions/create-session.ts#L11-L30
[devin-auth]: https://docs.devin.ai/api-reference/authentication
[devin-create]: https://docs.devin.ai/api-reference/v3/sessions/post-organizations-sessions.md
[devin-cloud]: https://docs.devin.ai/cli/cloud.md
[devin-handoff]: https://docs.devin.ai/work-with-devin/devin-handoff.md
[devin-script]:
https://github.com/club-cog/devin-handoff/blob/24a60e0b3030a162bedf709f0922d4b73f440004/.agents/skills/devin-handoff/scripts/devin-handoff.sh#L88-L104
[devin-script-create]:
https://github.com/club-cog/devin-handoff/blob/24a60e0b3030a162bedf709f0922d4b73f440004/.agents/skills/devin-handoff/scripts/devin-handoff.sh#L198-L212
[devin-slack]: https://docs.devin.ai/integrations/slack.md
[devin-teams]: https://docs.devin.ai/integrations/microsoft-teams.md
[devin-linear]: https://docs.devin.ai/integrations/linear.md
[devin-auto]: https://docs.devin.ai/product-guides/automations.md
[devin-mcp]: https://docs.devin.ai/work-with-devin/devin-mcp.md
[devin-pricing]: https://devin.ai/pricing
[devin-quota]: https://docs.devin.ai/admin/billing/self-serve.md
[claude-cloud]: https://code.claude.com/docs/en/claude-code-on-the-web
[claude-mobile]: https://code.claude.com/docs/en/mobile.md
[claude-slack]: https://code.claude.com/docs/en/slack.md
[claude-tag]: https://claude.com/docs/claude-tag/overview
[claude-routines]: https://code.claude.com/docs/en/routines.md
[claude-fire]: https://platform.claude.com/docs/en/api/claude-code/routines-fire
[claude-managed]: https://platform.claude.com/docs/en/managed-agents/overview

## Comparison table

**Docs**, **Code** and **Unverified** labels distinguish evidence within each row.
GitHub trigger details remain in [ingress and triggers][ingress] §§M to Q.

| Product | Human kickoff | Scripts and agent clients | Chat and trackers | Schedules | All surfaces use one public API? |
|---|---|---|---|---|---|
| A. Copilot | **Docs** web, IDEs, `gh`, `/delegate` and GitHub Mobile | **Docs** REST preview, issue APIs and remote MCP | **Docs** GitHub, Slack, Teams and Linear | **Docs** Automations | **Code** no for `gh` versus REST. **Unverified** web/mobile paths. §A |
| B. Cursor | **Docs** IDE, web, CLI `&`, iOS and Android PWA | **Docs** Cloud API and TypeScript/Python SDKs | **Docs** GitHub, Slack and Linear | **Docs** Automations | **Unverified** own CLI/web/mobile wire paths. §B |
| C. Codex | **Docs** ChatGPT web, desktop, iOS and `codex cloud` | **Code** account-backed CLI. **Docs** SDKs are local | **Docs** GitHub, Slack and Linear | **Docs** scheduled tasks, with runtime caveats | **Code** CLI uses ChatGPT backend. **Unverified** public cloud creation API. §C |
| D. Jules | **Docs** web and Jules Tools CLI | **Docs** REST. **Code** SDK and MCP | **Docs** GitHub label. **Unverified** native Slack/Teams/Linear kickoff | **Docs** scheduled tasks | **Code** SDK/MCP use public API. **Unverified** CLI/web paths. §D |
| E. Devin | **Docs** web, desktop and cloud CLI | **Docs** REST and MCP. **Code** handoff script | **Docs** GitHub, Slack, Teams and Linear | **Docs** Automations | **Code** handoff uses public API. **Unverified** own app paths. §E |
| F. Claude Code | **Docs** web, desktop, CLI and iOS/Android | **Docs** saved-task `/fire`, not Managed Agents | **Docs** GitHub and Slack, with identity caveats | **Docs** cloud scheduled tasks | **Unverified** app wire paths. **Docs** Code and Platform contracts differ. §F |

## A. GitHub Copilot coding agent

### A1. GitHub offers many kickoff clients, including its phone app

**Docs.** The [surface index][gh-start] lists web, IDEs, CLI, REST, MCP, Slack, Teams and Linear.
It also lists Automations. [GitHub Mobile][gh-mobile] can submit a repository and prompt
or assign an issue to Copilot. GitHub triggers remain in [ingress and triggers][ingress] §M.

**Docs.** [`gh agent-task create`][gh-cli] starts remote work and supports `--repo`, `--base`
and `--follow`. Copilot CLI's [`/delegate PROMPT`][gh-delegate], also spelled `& PROMPT`,
hands work to cloud agent. Local autopilot is a different execution mode.

### A2. The open-source gh client does not call the public task endpoint

**Code.** At `cli/cli@9be5608`, [discovery][gh-discovery] queries GraphQL
`viewer.copilotEndpoints.api`. [`CreateJob`][gh-job] POSTs to that base plus
`/agents/swe/v1/jobs/{owner}/{repo}`. This differs from the public
`POST /agents/repos/{owner}/{repo}/tasks` in [APIs][apis] §I.

**Code.** The [transport][gh-transport] resolves the active GitHub credential per request and
sends `Authorization: Bearer <GitHub token>`. Requests to the Copilot backend also carry
`Copilot-Integration-Id: copilot-4-cli` and `X-GitHub-Api-Version: 2026-01-09`.
**Docs.** [`gh auth login`][gh-auth] offers browser/device-code login and token input.
`GH_TOKEN` supports headless callers. This does not make an installation token valid for tasks.

### A3. Public task creation runs with a user's authority

**Docs.** The [REST preview][gh-api] requires Business or Enterprise and repository
`Agent tasks` read/write permission. It accepts fine-grained PATs and GitHub App user tokens.
The [how-to][gh-assign] also names OAuth app tokens and classic PATs. Installation tokens are
explicitly unsupported. This is delegated user access, not an independent service principal.

**Docs.** Issue assignment has REST and GraphQL routes. The how-to requires classic `repo` or
fine-grained metadata read plus Actions, contents, issues and pull requests read/write.
GraphQL uses `GraphQL-Features` flags. See [APIs][apis] §I2 for the mutations and REST variants.

### A4. MCP exists, but its launch tools are not all the same implementation

**Docs.** The remote server offers [`create_pull_request_with_copilot`][gh-mcp].
**Code.** The public server at `github/github-mcp-server@f385262` separately exposes
[`assign_copilot_to_issue`][gh-mcp-code] with `repo` scope. It uses an
[`updateIssue` GraphQL mutation][gh-mcp-mutation] with the assignment feature header.
Neither establishes that web or Mobile uses the public task REST preview. **Unverified**.
**Unverified.** Dedicated task-creation SDK coverage was not established.

**Docs.** General [REST limits][gh-rate] normally allow 5,000 authenticated requests/hour.
That is not a promise of 5,000 task starts. Sessions have a [59-minute execution limit][gh-about].
**Unverified.** A separate numeric creation rate or universal task concurrency cap was not found.

## B. Cursor Cloud Agents

### B1. The CLI can hand off work, while web and mobile can start it directly

**Docs.** [Cloud Agents][cursor] supports the editor, web, iOS, Android PWA, API, GitHub,
Slack and Linear. The [CLI][cursor-cli], now documented as `agent`, can send an interactive
message beginning with `&` to a Cloud Agent. Web or mobile can pick it up afterward.
The checked docs do not establish terminal attachment to that cloud run. **Unverified**.

**Docs.** [Slack][cursor-slack] starts work through `@cursor`. [Linear][cursor-linear] supports
delegation and mentions. Both require account linking. [Automations][cursor-auto] add schedules,
event triggers and authenticated incoming webhook URLs. GitHub mechanics remain in
[ingress and triggers][ingress] §N.

### B2. Scripts have REST and official SDKs, but app wire equivalence is unproven

**Docs.** [APIs][apis] §D covers the Cloud API. The official [`@cursor/sdk`][cursor-sdk] and
[Python SDK][cursor-python] offer local and cloud runtimes. TypeScript uses
`Agent.create({cloud: ...})`, then `agent.send(prompt)`.
**Unverified.** CLI, web and mobile use of the same public REST endpoints was not shown.

**Docs.** [Cursor Cloud MCP][cursor-mcp] is a built-in diagnostics server for existing runs.
**Unverified.** No official external MCP task-creation tool was established in the checked docs.
Consuming MCP tools inside an agent is not the same capability.

### B3. User keys and enterprise service accounts support automation

**Docs.** The [API overview][cursor-api] documents Basic auth with an empty password or bearer
auth. The SDK accepts user keys and service-account keys, not Team Admin keys.
The [CLI][cursor-cli-auth] supports browser login or `CURSOR_API_KEY`.
[Service accounts][cursor-service] are Enterprise identities with team-level repository access,
key rotation and optional spend limits. They consume the team's pool rather than a human seat.
SDK user-key usage bills to the user. Repository-scoped keys cannot start no-repository agents.

**Docs.** The API overview states a default 20 requests/minute unless overridden, but its Cloud
Agents row only says standard rate limiting. **Unverified.** A more specific launch rate or
numeric cloud concurrency cap was not found. **Docs.** Cloud work also consumes plan usage.

## C. OpenAI Codex cloud

### C1. Cloud work can begin from ChatGPT, a phone or the terminal

**Docs.** [Codex Cloud][codex-cloud] starts from ChatGPT web, desktop and mobile after selecting
an environment. The [iOS flow][codex-mobile] distinguishes Cloud from a connected computer.
[`codex cloud exec --env ENV_ID "PROMPT"`][codex-cli] submits from the terminal.
**Code.** The [CLI enum][codex-commands] also has `list`, `status`, `diff` and `apply`.
`exec` accepts `--branch` and one to four attempts. Attempts are not a concurrency allowance.

**Docs.** [Linear][codex-linear] supports assignment and mentions. Triage automation runs as
the issue creator. [Slack][codex-slack] now documents `@ChatGPT`, replacing new `@Codex`
requests. Coding delegation needs a published shared environment and runs as the requester.
GitHub `@codex` behavior remains in [ingress and triggers][ingress] §O.

### C2. Codex's open-source CLI exposes backend calls, not a public REST promise

**Code.** At `openai/codex@5c6ef3d`, [initialization][codex-init] defaults to
`https://chatgpt.com/backend-api`. [Creation][codex-create] POSTs `/wham/tasks`.
[Listing and inspection][codex-paths] GET `/wham/tasks/list` and `/wham/tasks/{id}`.
The code also supports an alternate `/api/codex/tasks` path style.

**Code.** Initialization requires Codex-backend authentication. For ChatGPT credentials,
the [auth selection][codex-auth-code] uses a [bearer provider][codex-bearer] that sends
`Authorization: Bearer <access token>` and `ChatGPT-Account-Id` when available.
**Docs.** [Authentication][codex-auth] says cloud requires ChatGPT sign-in, not a Platform key.
It documents browser login and `codex login --device-auth` for headless sign-in.

**Unverified.** No public, documented arbitrary Codex-cloud task creation API was found.
CLI source is not a stability promise for scripts calling those endpoints directly (inference).
Neither web/mobile endpoint equivalence nor a public cloud-task OAuth scope was established.

### C3. Developer SDKs and automation identities must not be mistaken for cloud access

**Docs.** The [TypeScript and Python SDKs][codex-sdk] control local Codex execution.
The old `codex mcp-server` was removed in favor of app-server, not a cloud launch server.
OpenAI Platform agent APIs remain separate contracts in [APIs][apis] §H.

**Docs.** [Access tokens][codex-tokens] and [service accounts][codex-service] support trusted
local automation. **Unverified.** Their documented local-access scope does not establish
permission to create arbitrary cloud tasks.

### C4. Usage limits and schedules depend on the execution surface

**Docs.** [Pricing][codex-pricing] shares plan allowance between local work and cloud chats.
Consumption depends on model and task size. Weekly limits can apply. API-key plans exclude
cloud-based features. **Unverified.** No universal cloud-creation RPM or concurrency cap was found.
**Docs.** [Scheduled tasks][codex-schedule] exist on web/mobile and desktop, but desktop jobs
needing local files require the computer online. These are not all `codex cloud` jobs.

## D. Google Jules

### D1. Jules Tools launches cloud sessions, but uses browser account login

**Docs.** [Jules Tools][jules-cli] provides `jules remote new --repo owner/repo --session "PROMPT"`.
It accepts `--parallel`. `jules login` opens Google sign-in.
This differs from the API-key setup for scripts. **Unverified.** The checked CLI docs do not
show its backend endpoint or prove that CLI and web use the public REST API.

**Docs.** Web [scheduled tasks][jules-schedule] launch recurring work. GitHub label kickoff
is covered in [ingress and triggers][ingress] §Q. The [API introduction][jules-api] describes
embedding Jules in Slack or Linear through custom workflows, not native installed launch apps.
**Unverified.** Native mobile, IDE and Teams task-launch clients were not established.

### D2. The official SDK and MCP really do call the public API

**Docs.** [API keys][jules-auth] go in `X-Goog-Api-Key`. The [guide][jules-api] permits three
active keys and requires connecting repositories through the web app first.
**Code.** At `google-labs-code/jules-sdk@19fc117`, the [client][jules-client] reads
`JULES_API_KEY` and defaults to `https://jules.googleapis.com/v1alpha`.
Its [creation call][jules-create] POSTs `sessions` and its [transport][jules-header] supplies
`X-Goog-Api-Key`. This is the documented API from [APIs][apis] §F.

**Code.** The official `@google/jules-mcp` [tool][jules-mcp] exposes `create_session`.
Its [implementation][jules-mcp-call] calls the SDK's `session()` or `run()`.
**Unverified.** Granular scopes, service-account credentials and third-party OAuth were not found.
**Docs.** Keys are generated in the user's Jules settings.

### D3. Task quotas are explicit

**Docs.** [Published plan limits][jules-limits] are 15, 100 or 300 tasks per rolling 24 hours
for Jules, Pro or Ultra. Concurrent limits are 3, 15 or 60 respectively.
**Unverified.** A separate numeric HTTP creation rate was not found.

## E. Devin

### E1. Devin has a real cloud CLI as well as chat and tracker clients

**Docs.** [`devin --cloud -p "PROMPT"`][devin-cloud] creates persistent cloud work.
`devin --cloud --resume ID` can reopen sessions started from CLI, web or Desktop.
`/handoff` transfers local conversation and git context to a cloud session.
[Slack][devin-slack] supports mentions and a create-session message shortcut.
[Teams][devin-teams] supports mentions in channels/group chats and direct messages.
[Linear][devin-linear] supports assignment, playbook labels and mentions.

**Docs.** [Automations][devin-auto] add schedules, GitHub/Linear events and incoming webhooks.
GitHub mention behavior remains in [ingress and triggers][ingress] §P.
**Unverified.** A native phone app was not established. Slack and Teams provide phone-accessible
launch channels (inference). Cross-client session visibility does not prove public API reuse.

### E2. A first-party handoff script uses public REST, but own app paths remain unknown

**Docs.** Cognition links its open-source [handoff plugin][devin-handoff] for other coding agents.
**Code.** At `club-cog/devin-handoff@24a60e0`, the [shell script][devin-script] selects
`/v3/organizations/{org_id}` for `cog_` credentials or legacy `/v1` for older credentials.
It sends bearer auth and [POSTs `/sessions`][devin-script-create].
**Unverified.** This proves the plugin's path, not the proprietary CLI/web/Desktop paths.

### E3. Devin separates credential identity from session attribution

**Docs.** [Authentication][devin-auth] distinguishes `cog_` service-user keys from human PATs.
Service-user roles and organization membership apply. Human PATs inherit the user's access.
[Creation][devin-create] requires `UseDevinSessions`, also named `org.devins.use`.
`create_as_user_id` requires `ImpersonateOrgSessions` and an eligible target user.
It changes session attribution and usage ownership, not the service user's audit identity.
This refines the shorter identity recap in [APIs][apis] §E1.

### E4. Official MCP can start tasks and product concurrency is plan-dependent

**Docs.** [Devin MCP][devin-mcp] exposes `devin_session_create` and schedule management at
`https://mcp.devin.ai/mcp`. Current `cog_` keys support session tools.
PATs and enterprise service keys need `X-Org-Id`. Organization service keys resolve it directly.
**Unverified.** A general official task-creation SDK was not found in the checked API index.

**Docs.** [Pricing][devin-pricing] lists up to ten concurrent sessions for Pro and Max,
with unlimited concurrent sessions for Teams and Enterprise.
[Usage quotas][devin-quota] still constrain spending. Pro has daily/weekly allowance and Max
has weekly allowance without a daily cap. **Unverified.** A numeric create RPM was not found.

## F. Claude Code on the web

### F1. The current CLI spelling is --cloud, while teleport brings work back

**Docs.** [Cloud sessions][claude-cloud] start from web, desktop, mobile or
`claude --cloud "PROMPT"`. `--remote` remains a deprecated alias.
`--teleport` pulls a cloud session into the terminal. It is not the new-task command.
Remote Control steers local execution and still needs that machine running.
[Mobile][claude-mobile] supports iOS and Android with the same claude.ai account.
Console API keys do not provide these mobile/cloud sessions.

**Unverified.** The checked docs do not establish one public API behind CLI, web and mobile.

### F2. GitHub and Slack include distinct execution and identity paths

**Docs.** [Cloud scheduled tasks][claude-routines] support schedules, API triggers and GitHub
events. `/schedule` manages them from the CLI. GitHub Actions execution is a different path,
covered in [ingress and triggers][ingress] §L.

**Docs.** The [earlier Slack integration][claude-slack] starts Claude Code cloud sessions under
linked Pro/Max users in workspaces not connected to Claude Tag.
Team/Enterprise [Claude Tag][claude-tag] uses organization-governed shared channel identity.
It can do coding work, but must not be described as the same personal-account launch path.
**Unverified.** Native Teams or Linear kickoff for personal cloud sessions was not found.

### F3. A public saved-task trigger exists, separately from Managed Agents

**Docs.** [`POST /v1/claude_code/routines/{routine_id}/fire`][claude-fire] starts a run of an
existing saved task. Its bearer token is scoped to that task and grants no read access.
Create/rotate it in the web UI. The task runs under its owner's subscription and connected
identities. This is not a general create-any-task endpoint.

**Docs.** The fire reference explicitly says there is no Anthropic SDK support for this trigger.

**Docs.** [Managed Agents][claude-managed] instead uses a Console API key and a beta header for
developer-defined agents. Its SDK/API contract is in [APIs][apis] §G.
**Unverified.** No evidence establishes that it is the public backend for Claude Code's apps.

### F4. The saved-task trigger has narrower quotas than an account-wide task API

**Docs.** The [fire reference][claude-fire] limits each routine to 30 fires/hour, shared with
web **Run now** and one-shot re-arms, plus 100 API fires/hour per account.
429 responses include `Retry-After`. [Scheduled runs][claude-routines] have a separate
100/hour account limit and consume subscription usage.
**Unverified.** A numeric universal cloud-session concurrency cap was not published.

## What this means for Hibernaut

**Docs** (inference). The common pattern is a shared service with adapters, not one proven public
API used by every client. GitHub's CLI is a counterexample. Jules proves public API reuse for
SDK/MCP, but not its web app. Product continuity and public wire compatibility differ.

**Docs** (inference). Interactive login, script keys/PATs and service identities coexist.
Chat integrations map channel identities to humans or shared principals. The submitter,
execution credentials, billing owner and PR author need not be the same identity.

**Docs** (inference). Hibernaut needs one task-submission boundary. A minimum is an authenticated
HTTPS front door on `hibgate` for the laptop, scripts and a phone-friendly form.
GitHub deliveries and schedules can be adapters to the same service. They need not make
loopback HTTP calls merely to share its authorization and submission logic.

**Docs** (inference). Begin with a revocable owner credential for laptop/scripts, a browser
session for the phone and verified GitHub deliveries with an owner/repository allowlist.
Record source and actor even if all tasks use Martin's agent credentials.
A separate service account is unnecessary until another independent principal exists.

**Docs** (inference). Persist work before acknowledging it, then wake the host.
Bound concurrency and spend. Phone/GitHub kickoff cannot depend on that host already answering.
This does not require moving existing machine commands off SSH.
[Spec decision D1][hib-spec] remains the v1 baseline.

## Gaps

- **Unverified.** No browser or mobile network traffic was captured. Shared public endpoints
  cannot be claimed from shared task history, branding or account login.
- **Unverified.** Cursor's cloud handoff wire path, Jules Tools' backend and the proprietary
  Devin/Claude CLI creation paths remain unknown. Code findings are revision-specific.
- **Unverified.** Codex's documented local access tokens and service accounts do not establish
  cloud-creation authorization. ChatGPT Teams support alone does not establish Codex delegation.
- **Unverified.** Numeric launch rates or universal concurrency limits remain missing where
  explicitly noted above. HTTP quotas, spending allowances and active-task caps are different.
- **Unverified.** An official external cloud-launch MCP server was not established for Cursor,
  Codex or personal Claude Code. MCP consumption inside a task is not launch support.
- **Unverified.** Native launch surfaces not listed as supported were not established.
  Factory, Amp and Warp were omitted to keep depth on the six requested products.
