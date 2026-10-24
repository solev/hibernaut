# How open-source cloud-agent platforms are built, for Hibernaut

**Date checked:** 2026-10-24.

**Summary**

This note supplies implementation evidence for the Hibernaut cloud-agent design. It follows
`cloud-agents.md`'s survey, `cloud-agent-internals.md`'s commercial-worker internals and
`always-on-agents.md`'s durable-agent and channel research. It concentrates on public schemas,
execution ownership, sandbox boundaries, follow-ups and recovery. Repository snapshots matter:
several implementations have changed substantially since the earlier survey.

- **OpenHands is now a split control plane, not one monolithic server.**
  Canvas, SDK Agent Server and automation have separate responsibilities and storage (§A1 to §A7).
- **Coder Tasks has been removed, not merely renamed.**
  Its replacement keeps the model loop in the control plane and tools in workspaces (§B1 to §B6).
- **A task, an agent session and an execution process need different identities.**
  Vibe Kanban's schema migration and Terragon's shared-sandbox chats make this concrete (§C1, §G2).
- **A saved transcript does not imply a surviving process.**
  OpenHands recovers interrupted runs as errors, while Sculptor requeues tasks (§A4, §H2).
- **Durable events and live previews are different protocols.**
  OpenHands, Coder and Omnara explicitly separate replayable records from deltas (§A5, §B4, §I3).
- **Pause means different things at different layers.**
  Kubernetes suspension, native session resume and provider sandbox pause are distinct (§A6,
  §C4, §F3 and §G4).
- **Git completion and review feedback are control-plane work.**
  Branch state, credential selection, checkpoints and comment routing outlive individual turns
  (§C5, §D5 and §G5).
- **Open SWE and Sculptor need updated descriptions.**
  Open SWE now delegates execution to Managed Deep Agents. Sculptor is public MIT code (§D1, §H1).

## Sources

All repositories below were public when checked. License and activity entries use first-party
GitHub repository metadata and the repositories' license files. A recent push is not a promise
of support. Code citations use the seven-character prefix of these full commit IDs. Checked-in
documentation is cited at the same pin rather than assuming today's website matches a checkout.
**Docs**, GitHub REST repository and commit metadata:
https://docs.github.com/en/rest/repos/repos
https://docs.github.com/en/rest/commits/commits

- **OpenHands.** Documentation: https://docs.openhands.dev/
  Canvas, `OpenHands/OpenHands`: `fee10e3d2909ebbf9950578575b1ee011492171e`.
  SDK, `OpenHands/software-agent-sdk`: `acd96e6fa424c02432304395b9208f99be24b54d`.
  Automation, `OpenHands/automation`: `2c110703bbe91e4113a57adc1fa64a912ac7f4f8`.
  Archived app server, `OpenHands/legacy`: `ee9e78b7defdfa744e0bbe48c9cafa90b6135ad7`.
- **Coder.** Documentation: https://coder.com/docs/ai-coder
  `coder/coder`: `7eacfe95b28dd776bafbd7d5f04b9b7188ab1930`.
  `coder/agentapi`: `7c468d5b25ec9d3d76c41313cf4e08904ff5968d`.
  `coder/registry`: `50994be1706b05bb700a0cb366637d3a4be510c3`.
  `coder/terraform-provider-coder`: `d5137fb3fc5e493eb46becad2aeac518bc41c861`.
- **Vibe Kanban.** Documentation: https://www.vibekanban.com/docs
  `BloopAI/vibe-kanban`: `d5cbb5380fa0b32e98ef9b8d987f63decce4be3a`.
- **Open SWE.** Documentation: https://github.com/langchain-ai/open-swe/tree/main/docs
  `langchain-ai/open-swe`: `a7bb5302290af16a88ebf3219c35723b0fa82a5b`.
- **container-use.** Documentation: https://container-use.com/
  `dagger/container-use`: `2e43e625e95216b719ec9338f4034fd3a0be2734`.
- **SWE-ReX.** Documentation: https://swe-rex.com/
  `SWE-agent/SWE-ReX`: `5c995c365dfb1fd5bc56fda688be5d8538f9931f`.
- **Terragon.** Released documentation lives in the source snapshot.
  `terragon-labs/terragon-oss`: `83142a17f3970df3e14d1879234a603db6e4f615`.
- **Sculptor.** Documentation: https://github.com/imbue-ai/sculptor/tree/main/docs/help
  `imbue-ai/sculptor`: `5ebd30f0a373b785e7bc97181d2ea1212545bc8b`.
- **Goose.** Documentation: https://block.github.io/goose/
  `aaif-goose/goose`: `c3bfc5f7b0e230180ba3164ec1ee84fd951eaf0d`.
  The `block/goose` repository URL redirects to this owner.
- **Happy.** Documentation: https://happy.engineering/welcome/
  `slopus/happy`: `d6b85a0dda14c9582e1f083539fd11d99924022f`.
- **Omnara.** Documentation: https://docs.omnara.com/
  `omnara-ai/omnara`: `1bcdea77a35d10f5090ffdccf403ee29abb8b8de`.

**Tags.** **Docs**: read on the vendor's docs, changelog or blog on the date above. **Code**: read
in source at the named commit. **Unverified**: only the named secondary source says so. News and
forums served only as leads. Quotes are verbatim. "(paraphrase)" marks spots where a quote would
break this note's prose rules. "(inference)" marks my own deductions. An untagged claim takes the
tag of its sentence's citation.

## Comparison table

The table summarizes the cited sections, not a claim that all products implement the same
abstraction. Dates in the activity column are last-push metadata. Unknown means not established
by the inspected sources, not proof of absence. **Docs** and **Code**, §§A to I.

| Project | License and activity | Top entities | Lifecycle states | Sandbox providers | Loop location and wire | Streaming and replay | Follow-ups | Git and PR flow | Pause and resume |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| OpenHands | MIT current components, Oct 24. Legacy archived | Conversation, event, runtime, automation, run | SDK: idle, running, paused, waiting_for_confirmation, finished, error, stuck, deleting | Docker, remote runtime API, Kubernetes Agent Sandbox | Agent Server at chosen backend, native SDK or ACP subprocess. HTTP tools | WebSocket sequence replay, transient deltas separate | Persist message, run or supersede ACP prompt | Git inspection and provider access. Automation and legacy integrations supply workflow | Remote pause API. Kubernetes retains volume, not RAM |
| Coder | AGPL-3.0 core, Oct 24. AgentAPI MIT and archived | Chat, message, queue item, workspace, child chat | waiting, running, error, requires_action, interrupting | Terraform workspaces | Loop in coderd, tools over private workspace-daemon HTTP | WebSocket DB reconciliation plus ephemeral parts | Durable queue or interrupt | External-auth OAuth and workspace git tools | Workspace start or stop is separate from chat continuation |
| Vibe Kanban | Apache-2.0, Sep 19 | Project, task, workspace, session, execution process | Task: todo, inprogress, inreview, done, cancelled. Process: running, completed, failed, killed | Local processes and git worktrees in inspected path | Native CLI subprocess, agent-specific adapters | WebSocket JSON patches. Raw JSONL files support reconstruction | Native resume or fork. One in-memory queued follow-up per session | Worktree branch, push and provider PR API through gh adapter | Agent session continuation, no verified RAM pause |
| Open SWE | MIT own code, Oct 24. Vendor exclusion | Thread index, runtime thread, run, queue, plan, sandbox, automation | Thread index: idle, busy, interrupted, error | LangSmith default, Daytona, Modal, Runloop, E2B, local | Current loop in Managed Deep Agents. Backend hooks and sandbox tools | Proxied Agent Server event stream and runtime history | Queue injection, steering and enqueued runs | GitHub App git proxy, user OAuth or App PR identity, review and CI feedback | Reconnect same sandbox. Unreachable is not deleted |
| container-use | Apache-2.0, Sep 21 | Environment, config, container ID, branch, git notes | No product task-state enum in inspected model | Dagger containers | External caller owns loop, MCP stdio tools | Tool results and git notes, not client transcript stream | More calls against environment ID | Environment branch, checkpoint, merge or apply | Rebuild container state, no verified process-memory pause |
| SWE-ReX | MIT, Oct 19 | Deployment, runtime, named shell session, action, observation | No product task-state enum | Docker or Podman, Modal, Fargate, Daytona, remote | External loop, FastAPI JSON action requests | Request-response, not durable agent event stream | Named shell commands and interrupts | Caller responsibility | Persistent shell only while runtime survives |
| Terragon | Apache-2.0 snapshot, Feb 10. Hosted service ended Feb 9 | Thread, thread chat, daemon process, sandbox, automation | draft, scheduled, queue variants, booting, working, stopping, checkpointing, complete | E2B, Daytona. Docker development path | CLI loop inside sandbox, Unix socket commands and HTTP callbacks | Database transcript plus PartyKit invalidations | Persisted queuedMessages and native sessionId | Checkpoint, push, PR and mention feedback | Provider hibernate guarded by active-resource tracking |
| Sculptor | MIT, Oct 23. Research preview | Project, workspace, task, saved message, terminal session | QUEUED, RUNNING, FAILED, CANCELLED, DELETED, SUCCEEDED | Local environments, experimental whole-backend container | Backend-owned subprocesses, CLI JSON and PTY adapters | WebSocket initial snapshot plus updates | Message bus and saved native session | gh login, branch push, PR and CI monitoring | Requeue after backend restart, native session continuation |
| Goose | Apache-2.0, Oct 23 | Session, recipe, scheduled job | Scheduled-job flags, multiple session types | No sandbox provider verified in this slice | Goose server or ACP process owns execution | Session database. Server replay contract not traced | Existing sessions and scheduler controls | Not traced | Schedule pause is not process hibernation |
| Happy | MIT, Oct 23 | Account, machine, session, session message | Turn-end: completed, failed, cancelled | No sandbox provisioner in inspected relay | Original CLI wraps local harness, server relays encrypted sync | Stored sequence numbers and bounded history endpoint | Remote steering of original CLI session | Harness responsibility in inspected relay | Does not establish compute survival by itself |
| Omnara | Apache-2.0, Oct 24 | Agent, machine source, ordered event, tool interaction | Tool-result outcomes and separate transient updates | Managed machine pools in docs, provider internals not traced | Managed-agent platform, machine-daemon implementation present | SSE durable sequence plus nonreplayed previews | API input and interaction surfaces | GitHub integration present, mechanics not traced | Not traced |

## A. OpenHands

### A1. The current repositories describe a different boundary

The earlier survey's short runtime overview is background, not today's architecture diagram:
`cloud-agents.md §B8`. `All-Hands-AI/OpenHands` resolves to `OpenHands/OpenHands`. The current
README presents Agent Canvas, an agent-agnostic frontend for local, remote and cloud backends.
Canvas does not execute actions or provision sandboxes itself. Its primary backend is the SDK's
Agent Server. Automation is another service. **Docs**, `OpenHands/OpenHands@fee10e3`
`README.md:5-60`, `docs/architecture.md:3-31`.

The local stack starts Agent Server and automation separately. Self-hosting places them behind
one ingress, with Agent Server on port 18000, automation on 18001 and ingress on 8000. The ingress
routes `/api/*`, `/sockets` and `/api/automation/*`. A deployed backend can keep running after a
laptop disconnects. That is backend placement, not a special browser-session guarantee.
**Docs**, `OpenHands/OpenHands@fee10e3`
`docs/architecture.md:45-56`, `docs/SELF_HOSTING.md:23-63`, `README.md:54-60`.

The separate `OpenHands/legacy` repository is archived. Its license permits MIT use outside
`enterprise/`, which has separate terms. Its remaining app-server code is useful historical
evidence, but it must not be silently mixed with the current Canvas and SDK APIs.
**Docs**, repository metadata and `OpenHands/legacy@ee9e78b LICENSE:1-7`.

### A2. Conversation identity, execution state and event persistence

The SDK conversation owns `id`, `agent`, `workspace`, `persistence_dir`, `max_iterations`,
`stuck_detection` and `confirmation_policy`. Persistence defaults to `workspace/conversations`.
Setting the persistence directory to `None` disables it. The iteration default is 500 and the
default confirmation policy is `NeverConfirm`. **Code**, `OpenHands/software-agent-sdk@acd96e6`
`openhands-sdk/openhands/sdk/conversation/state.py:82-127`.

The exact execution states are `idle`, `running`, `paused`, `waiting_for_confirmation`,
`finished`, `error`, `stuck` and `deleting`. Terminal states are `finished`, `error` and `stuck`.
An idle conversation is not declared terminal. A confirmation wait is distinct from a pause.
**Code**, `OpenHands/software-agent-sdk@acd96e6`
`openhands-sdk/openhands/sdk/conversation/state.py:48-79`.

Persistent state uses `base_state.json` and an `events/` directory. Individual event names follow
`event-{idx:05d}-{event_id}.json`. The event log indexes both ID and position. It uses FileStore
locking with a 30-second lock timeout and warns about `flock` reliability on NFS. This is a
filesystem-backed conversation store, not a central SQL task queue. **Code**,
`OpenHands/software-agent-sdk@acd96e6`
`openhands-sdk/openhands/sdk/conversation/persistence_const.py:4-10`,
`openhands-sdk/openhands/sdk/conversation/event_store.py:22-61`.

Events have `parent_id`, so the history can form a tree with an active branch. Missing parent IDs
in older records fall back to the preceding event. Server conversation metadata separately
records `forked_from_conversation_id` and `forked_from_event_id`. Forking a conversation therefore
does not, by itself, prove a separately isolated filesystem. **Code**,
`OpenHands/software-agent-sdk@acd96e6`
`openhands-sdk/openhands/sdk/conversation/event_store.py:95-130`,
`openhands-agent-server/openhands/agent_server/models.py:117-131` (inference).

### A3. Loop placement and the action-execution boundary

Agent Server executes the SDK conversation with native `arun()` when the agent supports it.
Otherwise it dispatches synchronous `conversation.run` through an executor. Thus the loop lives
at the selected Agent Server backend. In per-conversation Docker mode, that backend runs inside
the conversation container. ACP agents add another subprocess boundary, not another mandatory
cloud control plane. **Code**, `OpenHands/software-agent-sdk@acd96e6`
`openhands-agent-server/openhands/agent_server/event_service.py:1619-1653`.
**Docs**, `OpenHands/OpenHands@fee10e3 README.md:106-121`.

The current action surface is Agent Server HTTP, not an assumption that the old V0 runtime
protocol remains unchanged. Its bash router exposes `/bash/start_bash_command`,
`/bash/execute_bash_command` and bash-event search, plus a stop-command route. These routes are
under the API ingress. Git changes, diffs and commits have separate inspection routes.
**Code**, `OpenHands/software-agent-sdk@acd96e6`
`openhands-agent-server/openhands/agent_server/bash_router.py:31-173`,
`openhands-agent-server/openhands/agent_server/git_router.py:158-218`.

Conversation HTTP operations include create, `/run`, `/pause`, `/interrupt`, delete and
`/respond_to_confirmation`. Creation and run admission can return HTTP 429. Server configuration
defaults to ten concurrent runs across native async and synchronous paths. Initialization also
reserves capacity. **Code**, `OpenHands/software-agent-sdk@acd96e6`
`openhands-agent-server/openhands/agent_server/conversation_router.py:312-443`,
`openhands-agent-server/openhands/agent_server/event_router.py:213-240`,
`openhands-agent-server/openhands/agent_server/config.py:348-355`.

### A4. Follow-ups, interruption and crash recovery

Sending a message first persists it through `conversation.send_message`. With run requested,
native conversation execution is started or a rerun intent is recorded if another run is still
active. For an ACP prompt that is superseded, the server interrupts the prompt and starts the
replacement while retaining its run-capacity permit. These are deliberate adapter semantics,
not a universal append-to-stdin rule. **Code**, `OpenHands/software-agent-sdk@acd96e6`
`openhands-agent-server/openhands/agent_server/event_service.py:888-971`.

A capacity error can mean the message was saved but execution was not admitted. The error tells
the caller to retry `/api/conversations/{id}/run` without resending the message. Otherwise an
ordinary retry could duplicate the user's request. **Code**, `OpenHands/software-agent-sdk@acd96e6`
`openhands-agent-server/openhands/agent_server/event_service.py:957-965`.

Startup loads records that were marked running for recovery. A valid live lease prevents
takeover, but an interrupted persisted run is marked error and receives a recovery event.
The store preserves evidence and restart context. It does not transparently restore a Python
stack or an in-flight model request. **Code**, `OpenHands/software-agent-sdk@acd96e6`
`openhands-agent-server/openhands/agent_server/conversation_service.py:2210-2245`,
`openhands-agent-server/openhands/agent_server/event_service.py:1425-1428` (inference).

### A5. Durable streaming and lossy preview are explicit

The session WebSocket is `/sockets/session/{conversation_id}`. A reconnect supplies `after_seq`.
The server sends a `sync` boundary, durable frames with `seq` and transient frames without a
cursor. `item_started`, `delta` and `item_aborted` describe unfinished generation. Deltas carry
`item_id`, `attempt` and an increasing `order`. A higher attempt replaces the older preview.
**Code**, `OpenHands/software-agent-sdk@acd96e6`
`openhands-agent-server/openhands/agent_server/session_protocol.py:1-164`.

The final durable event ID matches the preview's item identity. Transient progress is not
replayed and must not advance the durable cursor. The provisional maximum frame is 4 MiB.
Outbound queues are byte-bounded. Overflow disconnects the slow client rather than silently
discarding durable events. The publisher is not blocked by a slow browser. **Code**,
`OpenHands/software-agent-sdk@acd96e6`
`openhands-agent-server/openhands/agent_server/session_protocol.py:1-90`,
`openhands-agent-server/openhands/agent_server/session_socket.py:63-178`.

Subscription buffers live arrivals during disk replay, then removes duplicates already covered
by replay. HTTP event search is separate: it accepts `page_id`, `limit`, event filters and sort
parameters, returning `items` and `next_page_id`. Its default and maximum page size are 100.
**Code**, `OpenHands/software-agent-sdk@acd96e6`
`openhands-agent-server/openhands/agent_server/session_socket.py:173-233`,
`openhands-agent-server/openhands/agent_server/event_router.py:68-139`.

### A6. Provisioning, reuse and three meanings of pause

`OH_CONVERSATION_RUNTIME=docker` creates a separate container and Agent Server for each new
conversation. Workspace and conversation-state mounts survive replacement. The documented
shared-host-workspace setup shares files between conversations. It is not automatic per-branch
edit isolation. SDK Docker workspaces accept images, port mappings and volume mounts. Published
ports bind to loopback. **Docs**, `OpenHands/OpenHands@fee10e3 README.md:106-121`.
**Code**, `OpenHands/software-agent-sdk@acd96e6`
`openhands-workspace/openhands/workspace/docker/workspace.py:53-117,218-222`.

`APIRemoteWorkspace` is a runtime-provider client. Its defaults include `init_timeout=300`,
`startup_wait_timeout=300`, `api_timeout=60`, `keep_alive=False` and `pause_on_close=False`.
It can attach through `GET /sessions/{session_id}`. A paused session is resumed rather than
replaced. **Code**, `OpenHands/software-agent-sdk@acd96e6`
`openhands-workspace/openhands/workspace/remote_api/workspace.py:19-90,157-180`.

The remote API starts compute with `POST /start`. Its payload includes `image`, `command`,
`environment`, `session_id`, `run_as_user`, `fs_group` and optional runtime-class or resource
selection. The command starts Agent Server on port 60000. The response supplies a runtime ID,
URL and `session_api_key`. `/pause` and `/resume` take `runtime_id`. Cleanup chooses pause or stop,
unless `keep_alive` suppresses cleanup. **Code**, `OpenHands/software-agent-sdk@acd96e6`
`openhands-workspace/openhands/workspace/remote_api/workspace.py:184-290,389-404`.

The Kubernetes Agent Sandbox workspace claims prewarmed pods through a named `warmpool`.
Its suspension sets `spec.operatingMode` to `Suspended`. The pod terminates while the persistent
volume remains. `Running` creates a new pod and reconnects the port forward on a fresh port.
Deleting the workspace removes the SandboxClaim. This is not a memory snapshot.
**Code**, `OpenHands/software-agent-sdk@acd96e6`
`openhands-workspace/openhands/workspace/agent_sandbox/workspace.py:46-81,165-175,335-387,427-437`.

Conversation `/pause`, remote-provider `/pause` and Kubernetes suspension therefore describe
three different state machines. The remote client's endpoint names alone do not establish RAM
preservation or recovery after provider loss. **Code**, §A3 and this section (inference).

### A7. Automation has its own durable run model

The automation service stores definitions in `automations` and executions in `automation_runs`.
Definitions include owner and organization, prompt, model, agent profile, trigger JSON,
setup or entrypoint data, timeout, `keep_alive`, enabled state and last-trigger timestamps.
Runs link the definition to `conversation_id`, `sandbox_id`, cost, timeout, trigger identity,
event payload, runtime selection and cleanup timing. **Code**, `OpenHands/automation@2c11070`
`openhands/automation/models.py:61-332`.

Definition states are `ACTIVE`, `INACTIVE` and `DRAFT`. Run states are `PENDING`, `RUNNING`,
`COMPLETED`, `FAILED`, `CANCELLED` and `SKIPPED`. A run has `status_detail`, `error_detail` and
`current_phase` in addition to the enum. These should not be conflated with the SDK's execution
status. **Code**, `OpenHands/automation@2c11070 openhands/automation/models.py:42-58,180-230`.

Cron triggers have a cron `schedule` and IANA `timezone`, defaulting to UTC. The scheduler
persists the pending run and trigger advancement in one transaction. PostgreSQL workers use
`FOR UPDATE SKIP LOCKED`. The code includes a SQLite single-process caveat, while the model says
deployed environments use PostgreSQL. The default polling loop is 60 seconds. **Code**,
`OpenHands/automation@2c11070`
`openhands/automation/schemas.py:63-80`, `openhands/automation/scheduler.py:112-278`,
`openhands/automation/models.py:322-328`.

The dispatcher claims pending rows, marks them `RUNNING` with a provisioning deadline and commits
before launching async execution tasks. Once the bash command starts, its timeout becomes the
execution deadline plus cleanup margin. The watchdog verifies actual command status before
timing out a run. It can defer a still-running command within bounded grace, then mark the run
`FAILED` with timeout detail. **Code**, `OpenHands/automation@2c11070`
`openhands/automation/dispatcher.py:950-969,989-1058`,
`openhands/automation/watchdog.py:457-470,505-553`.

Event triggers can choose `dispatch_run` or `continue_conversation`. The latter uses a
`subject_key_expr` to group events and falls back to a new run if no conversation exists.
The schema gives GitHub issue or PR identity and Slack channel plus thread identity as examples.
The live-subject index includes `automation_id`, `subject_key` and unreleased records. It is not
itself a unique constraint. **Code**, `OpenHands/automation@2c11070`
`openhands/automation/schemas.py:240-262`, `openhands/automation/models.py:334-343`.

### A8. Credentials, skills and the older resolver

REST uses `X-Session-API-Key`. WebSockets accept an initial `auth` message containing
`session_api_key`, a deprecated query parameter or a nonbrowser header. Server `secret_key`
encrypts sensitive serialized values. Prior keys support decryption during rotation.
Authentication is not an egress policy. **Code**, `OpenHands/software-agent-sdk@acd96e6`
`openhands-agent-server/openhands/agent_server/sockets.py:1-13`,
`openhands-agent-server/openhands/agent_server/config.py:358-378` (inference).

The current reusable-instruction model is `Skill`, with `name`, `content`, `trigger` and inputs.
It supports repository or knowledge skills and AgentSkills `SKILL.md` packages. The loader
documents `.openhands/skills` and includes `.agents/skills` discovery. These are instruction
packages, not separately provisioned workers or durable tasks. **Code**,
`OpenHands/software-agent-sdk@acd96e6`
`openhands-sdk/openhands/sdk/skills/skill.py:97-110,178-237,785-793,859-950` (inference).

The archived app server separately linked conversation records to `sandbox_id`,
`selected_repository`, `selected_branch` and parent conversations. Its GitHub resolver helper
fetches issue comments and traverses review threads through GraphQL. That demonstrates richer
review context collection, but not a verified current Canvas clone-to-PR transaction.
**Code**, `OpenHands/legacy@ee9e78b`
`openhands/app_server/app_conversation/app_conversation_models.py:110-134`,
`openhands/app_server/integrations/github/service/resolver.py:19-71,73-176`.

The archived resolver's instructions tell the agent to use `GITHUB_TOKEN`, create an
`openhands/` branch, commit, push and call `create_pr`. This is prompt policy, not proof that an
external controller guarantees each git step. **Code**, `OpenHands/legacy@ee9e78b`
`openhands/app_server/integrations/templates/resolver/github/issue_conversation_instructions.j2:32`
`openhands/app_server/integrations/templates/resolver/github/issue_conversation_instructions.j2:34`
`openhands/app_server/integrations/templates/resolver/github/issue_conversation_instructions.j2:37`
(inference).

## B. Coder Tasks and its replacement

### B1. Version boundaries are essential

The original AgentAPI-based overview is in `cloud-agents.md §B7`. Current migration documentation
says Tasks was deprecated in v2.34 and removed, including `/api/v2/tasks`. Chats is a separate
resource, not a compatible new name. The removal migration drops `tasks`, `task_workspace_apps`,
`task_snapshots`, `tasks_with_status` and `task_status`. **Docs**, `coder/coder@7eacfe9`
`docs/ai-coder/agents/tasks-to-chats-migration.md:5-22`.
**Code**, `coder/coder@7eacfe9`
`coderd/database/migrations/000594_drop_task_tables.up.sql:1-11`.

The retained Terraform provider still documents deprecated `coder_ai_task`. Its ESR notice
describes older Premium versions. It does not restore the removed API on current Coder main.
The resource reads `CODER_TASK_ID` and `CODER_TASK_PROMPT`, computes enabled state and requires an
app ID. It describes the task's workspace app, not a provider-independent sandbox allocator.
**Docs**, `coder/terraform-provider-coder@d5137fb docs/resources/ai_task.md:5-41`.
**Code**, `coder/terraform-provider-coder@d5137fb provider/ai_task.go:13-88` (inference).

Historical tasks linked organization, owner, workspace, template version, template parameters
and prompt. `task_workspace_apps` related a task to its workspace build, agent and app.
The historical status enum was `pending`, `initializing`, `active`, `paused`, `error` and
`unknown`. A view derived it from provisioner, workspace-agent and app health, rather than from
model-turn completion. **Code**, `coder/coder@7eacfe9`
`coderd/database/migrations/000366_create_tasks_data_model.up.sql:1-19`,
`coderd/database/migrations/000379_create_tasks_with_status_view.up.sql:27-67`.

### B2. AgentAPI was terminal automation, not an agent-native event log

AgentAPI is MIT, archived and explicitly deprecated. Its documentation recommends Coder Agents
or newer agent modules. It exposed `GET /messages`, `POST /message`, `GET /status` and SSE
`GET /events`. Status was `stable` or `running`. A successful send waited until it detected the
agent processing the message. **Docs**, `coder/agentapi@7c468d5 README.md:4,87-90`.

Its generic adapter drove an in-memory terminal emulator with keystrokes, then compared screen
snapshots and removed TUI fragments. Those normalized messages are not the same evidence as
native tool-call events. The current Claude Code registry module installs and configures the
CLI. It should not be described as an equivalent replacement universal task sidecar.
**Docs**, `coder/agentapi@7c468d5 README.md:152-184`.
**Code**, `coder/registry@50994be registry/coder/modules/claude-code/main.tf:449-473` (inference).

### B3. Chats moves the loop into the control plane

The new architecture puts the model loop in `coderd`. A workspace is ordinary tool compute,
not the process owning model state. The control plane talks to the existing workspace daemon
over private Tailnet HTTP, directly or through DERP. The daemon connects outbound and requires
no new public inbound port. Provider keys stay in the control plane. **Docs**,
`coder/coder@7eacfe9 docs/ai-coder/agents/architecture.md:5-55`.

Chats use database-backed messages and queue state. Workspace selection or provisioning can
wait until the first tool needs compute. Tools expose files, processes and workspace lifecycle.
A child agent can be spawned, messaged, waited on or interrupted. Children cannot recursively
spawn children or create workspaces. **Docs**, `coder/coder@7eacfe9`
`docs/ai-coder/agents/architecture.md:87-169`.

A chat links organization, owner, workspace, project, build, workspace agent and model config.
It records parent or root chat, title, status, plan mode, last error, summary, PR-related state,
labels, files and `queued_for_capacity`. Child depth is capped at one. **Code**,
`coder/coder@7eacfe9 codersdk/chats.go:154-219`.

### B4. State, leases and client reconciliation

Current status values are exactly `waiting`, `running`, `error`, `requires_action` and
`interrupting`. These replace neither workspace build states nor historical task states.
The documented machine additionally tracks archive state, ownership, pending action and queue
presence. PostgreSQL row locks serialize transitions and versions fence history and queue
changes. **Code**, `coder/coder@7eacfe9 codersdk/chats.go:119-127`.
**Docs**, `coder/coder@7eacfe9 coderd/x/chatd/ARCHITECTURE.md:24-119`.

Workers acquire execution ownership with a lease. Heartbeats renew every nine seconds and
expire after 30 seconds. A stale lease is not revived and results from an obsolete owner are
fenced. This is recovery by durable state and ownership, not by preserving a worker's memory.
**Docs**, `coder/coder@7eacfe9 coderd/x/chatd/ARCHITECTURE.md:694-704,755-804`.
**Code**, `coder/coder@7eacfe9 coderd/x/chatd/chatstate/machine.go:20` (inference).

Stream event names include `message_part`, `message`, `status`, `error`, `queue_update`, `retry`,
`action_required`, `preview_reset` and `history_reset`. Partial messages carry history version,
generation attempt and sequence identity. The stream bootstraps durable database state and
reconciles revisions separately from ephemeral preview parts. An ownership or generation change
can reset the preview without deleting committed history. **Code**, `coder/coder@7eacfe9`
`codersdk/chats.go:2049-2082`.
**Docs**, `coder/coder@7eacfe9 coderd/x/chatd/ARCHITECTURE.md:1250-1329`.

### B5. Queue semantics, interruption and limits

Message input has `busy_behavior` of `queue` or `interrupt`. Interrupting preserves a partial
response and prioritizes the new message. Further messages can queue. Finishing a turn either
returns to `waiting` or promotes the queue head and continues running. A dynamic tool wait uses
`requires_action`, with a documented five-minute deadline. Unrecoverable failures set `error`
and `last_error`. **Code**, `coder/coder@7eacfe9 codersdk/chats.go:974-1007`.
**Docs**, `coder/coder@7eacfe9 coderd/x/chatd/ARCHITECTURE.md:125-150`.

Interrupt does not discard the backlog. Editing history clears the queue, invalidates work and
truncates the active suffix. Promotion can interrupt current execution. The implementation has
separate transition methods for sending, queue deletion, promotion, interruption, acquisition,
human-action waits and finishing. **Docs**, `coder/coder@7eacfe9`
`coderd/x/chatd/ARCHITECTURE.md:125-150`.
**Code**, `coder/coder@7eacfe9 coderd/x/chatd/chatstate/transitions.go:486-673,943-1095,1319-1719`.

SDK defaults are 1200 steps per turn, 25 generation retries, 20 queued messages per chat and
50 attachments per chat. Prompt input is capped at 128 KiB and an uploaded file at 10 MiB.
Capacity accounting separately handles active root chats, subagents and queued capacity.
These admission limits are not the same as the number of running workspaces. **Code**,
`coder/coder@7eacfe9 codersdk/chats.go:31-57`, `coderd/x/chatd/capacity.go:18-75` (inference).

### B6. Git, credentials and stop boundaries

Current agents use configured external OAuth authentication for git providers. The git-provider
documentation separates authentication and token URLs from API base URLs, including GitHub
Enterprise. The inspected material does not establish one mandatory branch naming, commit
authorship or autonomous PR-review webhook policy. **Docs**, `coder/coder@7eacfe9`
`docs/ai-coder/agents/platform-controls/git-providers.md:5-30`.

CLI modules have a different credential path. The Claude Code module can set
`CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_API_KEY` or an API-key helper. AI Gateway mode sets the base
URL to Coder's `/api/v2/aibridge/anthropic` and uses a workspace owner token for authentication.
Those modes should not be confused with current Chats keeping model keys outside workspaces.
**Code**, `coder/registry@50994be`
`registry/coder/modules/claude-code/main.tf:63-90,114-129,253-312`.

The migration replaces task resume with sending a new chat message. Workspace start and stop
remain tool-compute lifecycle operations. There is no basis here for treating a chat interrupt
as VM hibernation or assuming historical task pause endpoints still exist. **Docs**,
`coder/coder@7eacfe9 docs/ai-coder/agents/tasks-to-chats-migration.md:43-57`,
`docs/ai-coder/agents/architecture.md:112-149` (inference).

## C. Vibe Kanban

### C1. Attempts became workspaces and sessions

The requested older task-attempt vocabulary is now a migration boundary. The migration renames
`task_attempts` to `workspaces`, creates `sessions` and makes execution processes reference a
session. `executor_sessions` becomes `coding_agent_turns`. A workspace can therefore contain
multiple sessions and each session multiple execution processes. **Code**,
`BloopAI/vibe-kanban@d5cbb53`
`crates/db/migrations/20251216142123_refactor_task_attempts_to_workspaces_sessions.sql:1-42`.

Local task records contain project, title, description, status and optional
`parent_workspace_id`. A workspace records optional task linkage, `container_ref`, branch,
setup completion, archive or pin state and `worktree_deleted`. A session records workspace,
executor, name and agent working directory. The name `container_ref` is not proof of a Docker
security boundary. **Code**, `BloopAI/vibe-kanban@d5cbb53`
`crates/db/src/models/task.rs:14-33`, `crates/db/src/models/workspace.rs:41-82`,
`crates/db/src/models/session.rs:22-37` (inference).

Task statuses are `todo`, `inprogress`, `inreview`, `done` and `cancelled`. Execution statuses are
`running`, `completed`, `failed` and `killed`. Execution run reasons serialize as `setupscript`,
`cleanupscript`, `archivescript`, `codingagent` and `devserver`. An execution includes action
JSON, exit code, timestamps and `dropped`. Dropped records remain stored but are excluded from
active history. **Code**, `BloopAI/vibe-kanban@d5cbb53`
`crates/db/src/models/task.rs:14-33`, `crates/db/src/models/execution_process.rs:39-93`.

### C2. Provisioning is worktree and process orchestration

Workspace creation generates a branch from workspace identity and a supplied label. The
create-and-start path attaches repositories and files, then starts the selected executor.
The inspected local deployment runs agents against managed git worktrees. It does not establish
an OS-container sandbox per attempt. **Code**, `BloopAI/vibe-kanban@d5cbb53`
`crates/server/src/routes/workspaces/create.rs:23-47,212-315`,
`crates/local-deployment/src/container.rs:241-313` (inference).

There are separate setup, cleanup, archive and development-server processes. Cleanup scans
expired workspaces and runs every 30 minutes. `DISABLE_WORKTREE_CLEANUP` disables the expired
worktree cleanup path. Retaining a workspace therefore has a filesystem policy independent of
whether an agent turn completed. **Code**, `BloopAI/vibe-kanban@d5cbb53`
`crates/local-deployment/src/container.rs:276-316`,
`crates/db/src/models/execution_process.rs:39-93` (inference).

Executors include Claude Code, Amp, Gemini, Codex, OpenCode, Cursor Agent, Qwen Code, Copilot and
Droid. The common interface has `spawn`, `spawn_follow_up`, `spawn_review` and `normalize_logs`.
It normalizes a family of native harnesses rather than forcing every agent through one
text-terminal protocol. **Code**, `BloopAI/vibe-kanban@d5cbb53`
`crates/executors/src/executors/mod.rs:100-123,222-269`.

### C3. Transcript files and reconstructed streams

Current raw logs are flat files, not solely the older SQLite log table. The service migrates
database logs to files, reads files first and retains a database fallback. Paths contain
`sessions`, the session identity, `processes` and `{process_id}.jsonl`. stdout and stderr are
persisted. Native session and message IDs update turn metadata. JSON patches are not persisted
as the raw log record. **Code**, `BloopAI/vibe-kanban@d5cbb53`
`crates/services/src/services/execution_process.rs:30-43,195-228,241-344`,
`crates/utils/src/execution_logs.rs:8-51,99-100`.

Client routes expose raw-log and normalized-log WebSockets. When no live process store exists,
the service loads raw records and runs normalization again. The live `MsgStore` combines bounded
history with broadcast. A lagging subscriber can lose broadcast messages and gets a warning.
This is history reconstruction plus live streaming, not a verified durable sequence cursor
equivalent to OpenHands' protocol. **Code**, `BloopAI/vibe-kanban@d5cbb53`
`crates/server/src/routes/execution_processes.rs:44-183,288-301`,
`crates/services/src/services/container.rs:797-851,921-964`,
`crates/utils/src/msg_store.rs:22-61,101-113` (inference).

Normalized entry types include user or assistant messages, feedback, tool use, system messages,
errors, thinking, loading, next action, token usage and answered questions. Tool status can be
`created`, `success`, `failed`, `denied`, `pending_approval` or `timed_out`. Approval state belongs
to a tool entry, not the task's Kanban status. **Code**, `BloopAI/vibe-kanban@d5cbb53`
`crates/executors/src/logs/mod.rs:64-137` (inference).

### C4. Follow-ups are harness-specific

Follow-up actions contain prompt, native `session_id`, optional `reset_to_message_id`, executor
configuration and working directory. Claude uses stream-JSON input and output, with `--resume`
and optional `--resume-session-at`. The Codex adapter uses `codex app-server` JSON-RPC and forks
the existing native thread before starting the follow-up turn. Calling every adapter a simple
session resume would hide that difference. **Code**, `BloopAI/vibe-kanban@d5cbb53`
`crates/executors/src/actions/coding_agent_follow_up.rs:17-29,83-90`,
`crates/executors/src/executors/claude.rs:176-191,360-375`,
`crates/executors/src/executors/codex.rs:237-245,594-621`.

The queued-message service holds one message per session in an in-memory `Arc<DashMap>`.
Adding another replaces the queued value. Successful completion can consume it as the next
follow-up. Failed or killed execution discards it. This queue is not a durable FIFO and should
not be confused with persisted draft data. **Code**, `BloopAI/vibe-kanban@d5cbb53`
`crates/services/src/services/queued_message.rs:10-83`,
`crates/local-deployment/src/container.rs:619-667`.

Stop marks an execution `killed`. Shutdown waits for log persistence before dropping process
stores and cleans up process children. Native session files may permit another turn later, but
the inspected path does not freeze process memory. **Code**, `BloopAI/vibe-kanban@d5cbb53`
`crates/server/src/routes/execution_processes.rs:203-209`,
`crates/local-deployment/src/container.rs:795-804,1459-1465` (inference).

### C5. Git completion is explicit and provider-aware

The local deployment detects changes and commits them. PR creation resolves the push remote,
pushes the workspace branch, then calls the git-host provider with draft and head-repository
information. GitHub's provider wraps `GhCli` and fetches both general and review comments.
This reuses the GitHub CLI credential path rather than proving an installation-token broker.
**Code**, `BloopAI/vibe-kanban@d5cbb53`
`crates/local-deployment/src/container.rs:399-461`,
`crates/server/src/routes/workspaces/pr.rs:219-301`,
`crates/git-host/src/github/mod.rs:22-42,50-95` (inference).

Multiple workspaces can branch from a task and multiple sessions can use one workspace.
These are useful comparison boundaries, but sessions sharing a workspace also share its edits.
The inspected models do not establish a single automatic best-of-N scoring transaction.
**Code**, §C1 and §C2 (inference).

### C6. MCP and cloud mode are different surfaces

The documented MCP server is local stdio, launched with `npx vibe-kanban@latest --mcp`.
Its tools include repository inspection, issue management, workspace creation and
`start_workspace` with executor and repositories. Context can infer project or organization
from an active workspace. It is not documented as a publicly reachable MCP URL. **Docs**,
`BloopAI/vibe-kanban@d5cbb53`
`docs/integrations/vibe-kanban-mcp-server.mdx:6-13,40-60,123-146`.

Cloud documentation describes project, issue and progress synchronization, organizations and
team access. A signed host-relay route also exists in the local server. Neither fact alone proves
that Cloud provisions and owns each user's coding-agent compute. The managed-compute lifecycle
and numeric global executor limit remain unverified here. **Docs**, `BloopAI/vibe-kanban@d5cbb53`
`docs/cloud/index.mdx:7-53`.
**Code**, `BloopAI/vibe-kanban@d5cbb53 crates/server/src/routes/host_relay/mod.rs:1-19` (inference).

## D. Open SWE

### D1. The manager, planner and programmer diagram is historical

The current README calls this a software factory built on Deep Agents. Deployment as supplied
requires Managed Deep Agents, abbreviated MDA here. It warns that the project is under active,
unstable development. The `agent` graph is now a read-only compatibility graph for older threads.
Configured graphs are `agent`, `reviewer` and `review-scout`, not the requested older trio.
**Docs**, `langchain-ai/open-swe@a7bb530 README.md:12-30`.
**Code**, `langchain-ai/open-swe@a7bb530 langgraph.json:5-25`, `openswe/graphs/agent.py:1-4`.

MDA owns the model loop, checkpoints and tool execution. The Open SWE backend supplies the tool
surface and lifecycle hooks: `before_agent`, `before_model`, `model_result`, `after_model`,
`after_agent`, `sandbox_replaced` and `sandbox_unreachable`. Reading a small backend StateGraph
must not be mistaken for finding the full coding loop. **Code**,
`langchain-ai/open-swe@a7bb530 openswe/agent_api/graph.py:1-34`.

### D2. Thread index, queues and execution authority

The PostgreSQL `thread_index` keeps metadata, runtime identity and status. Its status values are
`idle`, `busy`, `interrupted` and `error`. It distinguishes MDA-backed and local records.
The index is not authoritative execution state. Start marks busy and completion reads status
from the owning runtime. A minute-level reconciler checks older busy records. **Code**,
`langchain-ai/open-swe@a7bb530`
`openswe/threads/index.py:1-38,105-143`, `openswe/threads/status.py:1-63`.

`thread_queued_message` stores thread ID, JSONB content, optional `queue_id`, identity sequence
and enqueue time. A unique queue ID per thread deduplicates retried dashboard sends. The cap is
100 messages. Overflow drops the oldest and logs it. A before-model hook injects queued
follow-ups as human messages. **Code**, `langchain-ai/open-swe@a7bb530`
`openswe/message_queue.py:24-74`, `openswe/middleware/check_message_queue.py:1-5`.

There is also runtime-level run queuing. Dashboard follow-ups can use
`multitask_strategy="enqueue"` and immediately receive a `run_id`, including for queued work
that may need cancellation. Steering an active run and queuing a later run are separate
operations. **Code**, `langchain-ai/open-swe@a7bb530`
`openswe/threads/runs.py:1120-1257,1288-1385`,
`openswe/threads/proxy.py:482-536`.

### D3. Per-thread sandbox reuse and conservative recovery

The provider registry supports `langsmith`, `daytona`, `modal`, `runloop`, `e2b` and `local`.
`SANDBOX_TYPE` defaults to LangSmith. Optional providers require their dependencies. Only
LangSmith honors the inspected snapshot and resource overrides. Thus the older Daytona-first
description is not the current default. **Code**, `langchain-ai/open-swe@a7bb530`
`openswe/sandboxes/providers/registry.py:15-29,73-106`.

Documented defaults for the managed sandbox are four vCPUs, 16 GiB RAM, 128 GiB disk, a
7200-second idle TTL and deletion 2592000 seconds after stop. Configuration includes a workspace
snapshot and root snapshot. These are provider settings, not guarantees shared by every
registry entry. **Docs**, `langchain-ai/open-swe@a7bb530 docs/CUSTOMIZATION.md:19-65`.

A stable sandbox proxy reconnects lazily to the metadata's `sandbox_id` with a lock. A transient
connection failure raises `SandboxUnreachableError`, preserving the identity because uncommitted
work may exist only there. Confirmed deletion is a separate `SandboxGoneError` condition.
Replacement is explicit and, for MDA sandboxes, coordinated with MDA. **Code**,
`langchain-ai/open-swe@a7bb530`
`openswe/sandboxes/state.py:32-74,137-165`,
`openswe/sandboxes/lifecycle.py:354-382,499-543,591-653`.

### D4. Streaming, plans and human decisions

The dashboard proxies `POST /threads/{thread_id}/stream/events` as SSE and forwards bytes from
the Agent Server. Run startup enables resumable streaming unless explicitly disabled.
The backend therefore delegates much of replay to the runtime rather than implementing an
independent transcript cursor in this proxy. The exact upstream retention window was not
established. **Code**, `langchain-ai/open-swe@a7bb530`
`openswe/threads/proxy.py:74-117`, `openswe/threads/runtimes.py:184-216` (inference).

Plans and comments have their own durable tables. `thread_plan` contains thread ID, JSONB value,
dismissal and update time. Comments record author, body, anchor, parent and creation time.
Plans are mirrored under `/workspace/plans`. The API supports plan reads, edits, comment threads
and comment submission that enqueues another run. **Code**, `langchain-ai/open-swe@a7bb530`
`openswe/threads/plan_store.py:28-89`, `openswe/threads/plan_api.py:83-158,162-222`.

The current plan response exposes `approvedBy` and `approvedAt`, but this alone does not prove
the old planner graph's mandatory approval gate still controls execution. Workflow-file push
approval is a different mechanism, with pending, approved and rejected decisions in thread
metadata. The README warns that this gate detects pushes rather than all possible writes.
**Code**, `langchain-ai/open-swe@a7bb530`
`openswe/threads/plan_api.py:90-107`, `openswe/threads/workflow_approval.py:11-12,43-90,161-183`.
**Docs**, `langchain-ai/open-swe@a7bb530 README.md:54-64`.

### D5. GitHub identity, git proxy and review feedback

GitHub App tokens are minted at runtime. The customization guide says not to store a GitHub
token as a deployment environment variable. Proxy rules inject credentials for GitHub git
traffic and `api.github.com`. Sandbox setup configures global git identity with
`OPEN_SWE_BOT_NAME` and `OPEN_SWE_BOT_EMAIL`. Repository routing bindings are not automatically
credential boundaries. **Docs**, `langchain-ai/open-swe@a7bb530`
`docs/CUSTOMIZATION.md:19-65`, `README.md:54-64`.
**Code**, `langchain-ai/open-swe@a7bb530 openswe/sandboxes/lifecycle.py:124-179,305-313`.

PR API identity is separately resolved. `open_pull_request` uses the requesting or selected
participant's OAuth token for user threads and an App token for system threads. It checks
consent and access, stamps attribution, then posts to `/repos/{owner}/{repo}/pulls`.
The tool contract requires the branch to be pushed first. An existing PR can be returned
instead of duplicated. **Code**, `langchain-ai/open-swe@a7bb530`
`openswe/tools/open_pull_request.py:71-81,1023-1051,1071-1138`.
**Docs**, `langchain-ai/open-swe@a7bb530`
`openswe/resources/prompts/tools/open_pull_request.md:1-29`.

Reviewer setup preclones the PR repository, fetches the requested base and head, checks out the
head and verifies its SHA. Trusted reviewer skills come from a trusted ref rather than the
PR author's modified instructions. This is a concrete distinction between task checkout and
review-policy checkout. **Code**, `langchain-ai/open-swe@a7bb530`
`openswe/sandboxes/repo_prep.py:28-80,126-156`.

GitHub follow-up handling supports tagged issue comments, inline review comments and reviews.
It resolves the existing thread, fetches comments since the last tag and creates or queues a new
run. Token refresh after a 401 is explicit. The native webhook handler also supports reviewer
requests and push-driven rereviews. **Code**, `langchain-ai/open-swe@a7bb530`
`openswe/github/webhook.py:747-810,1083-1094,1176-1252,1340-1355`.

### D6. Triggers, schedules and remaining scope

The current README advertises GitHub, Slack, dashboard and Linear-comment inputs, review or CI
feedback and schedules. The queue and runtime boundary above add implementation detail beyond
the transport and durable-runtime recap in `always-on-agents.md §C3` and §E1. **Docs**,
`langchain-ai/open-swe@a7bb530 README.md:43-64`.

Schedules retain five-field cron validation, automation and trigger identities, last thread or
run IDs and stored cron IDs. Code can adopt older Agent Server cron records into jobs. It would
be inaccurate to describe every current schedule as permanently owned by a LangGraph cron.
**Code**, `langchain-ai/open-swe@a7bb530`
`openswe/schedules/store.py:238-244,255-272,394-409,473-548`.

Model execution concurrency is partly upstream in MDA. No verified best-of-N attempt-comparison
model, universal sandbox egress allowlist or current label-only issue trigger is claimed here.
The current source was checked instead of assuming those older behaviors survived. **Code**,
§D1 to §D5 (inference). See Gaps.

## E. container-use

### E1. Environment state is the central entity

An environment stores creation or update times, configuration, Dagger container ID, title and
submodule paths. Config includes `base_image`, `workdir`, setup and install commands, environment
variables, secrets and services. Defaults are Ubuntu 24.04 and `/workdir`. The repository config
file is `.container-use/environment.json`. There is no task or conversation lifecycle enum in
this inspected state model. **Code**, `dagger/container-use@2e43e62`
`environment/state.go:9-17`, `environment/config.go:12-34`.

The repository layer owns source and fork paths, a worktree and locks. Environments map to
branches in a local remote named `container-use`. Git notes refs `container-use` and
`container-use-state` carry activity and state. Loading an environment reads both branch state
and its container ID. **Code**, `dagger/container-use@2e43e62`
`repository/repository.go:24-28,57-71,320-361`, `repository/git.go:475-507`.

### E2. The caller owns the loop and MCP supplies tools

The MCP server uses stdio. Tools create or select environments, update configuration, run
commands, manipulate files, add services and checkpoint. Multi-tenant mode requires
`environment_source` and `environment_id`. Single-tenant mode can inherit a selected environment.
This is an environment tool plane for an external agent, not an autonomous task dispatcher.
**Code**, `dagger/container-use@2e43e62 mcpserver/tools.go:29-97,139-206` (inference).

Every command uses a new Dagger `WithExec` step. It accepts any exit code, records output and
applies filesystem state even when the command fails. A loaded container ID reconstructs
container state. Background services use a separate Dagger service and tunnel path.
Continuing an environment is not equivalent to resuming the previous shell's process memory.
**Code**, `dagger/container-use@2e43e62`
`environment/environment.go:82-86,128-142,261-347`, `mcpserver/tools.go:576-577` (inference).

### E3. Git is the handoff and checkpoint substrate

Creation makes a branch and worktree, then an empty initial commit so state notes do not
collide on the parent commit. Filesystem changes are exported, committed and followed by state
notes. Local checkout defaults to `cu-<id>` tracking `container-use/<id>`. Log, diff, merge and
apply are first-class handoff operations. **Code**, `dagger/container-use@2e43e62`
`repository/repository.go:245-286,511-535,567-626`,
`repository/git.go:258-261,285-312,567-582`.

The inspected commit calls use ordinary git commit behavior and disable user hooks for internal
operations. They do not establish a product-wide bot author, GitHub App installation token or
automatic PR policy. Parallel environments provide separate branches. A service that wants
PR creation and feedback still needs another owner for that workflow. **Code**,
`dagger/container-use@2e43e62 repository/git.go:258-261,567-582` (inference).

### E4. Secrets are references, not a network firewall

Configuration stores secret references. The runtime resolves them as Dagger secrets and injects
them with `WithSecretVariable`. Documentation lists `op://`, `env://`, `vault://` and `file://`
schemes, dynamic injection and output redaction. Redaction does not establish universal
exfiltration prevention or a default egress allowlist. **Code**, `dagger/container-use@2e43e62`
`environment/environment.go:154-174`.
**Docs**, `dagger/container-use@2e43e62 docs/secrets.mdx:11-24,29-81` (inference).

No durable client transcript, hosted trigger receiver, numerical task-admission limit or
process-memory hibernation was verified in this slice. Those are outside the inspected
environment and MCP contract, not missing enum values to invent. **Code**, §E1 to §E3 (inference).

## F. SWE-ReX

### F1. It is a remote execution protocol, not a complete task platform

A deployment starts compute and returns `RemoteRuntime`. FastAPI inside the sandbox delegates
to `LocalRuntime`, which implements the same abstract methods. The calling agent owns the model
loop. A runtime can host multiple named shell sessions. This is the substrate distinction
already introduced in `cloud-agents.md §B9`, now visible in the actual wire. **Docs**,
`SWE-agent/SWE-ReX@5c995c3 docs/architecture.md:3-25`.

`CreateBashSessionRequest` defaults to session `default`, type `bash` and a one-second startup
timeout. `BashAction` carries command, session, timeout, interactive behavior, checking policy
and expected output. Observations contain output, exit code, failure reason and matched expect
string. A one-off `Command` separately supports environment, directory, shell and merged output.
**Code**, `SWE-agent/SWE-ReX@5c995c3 src/swerex/runtime/abstract.py:23-35,50-128,143-175`.

### F2. HTTP calls, request identity and limited retry safety

Server routes include `/is_alive`, `/create_session`, `/run_in_session`, `/close_session`,
`/execute`, `/read_file`, `/write_file` and upload. Authentication uses `X-API-Key` when
`AUTH_TOKEN` is configured. Exceptions are serialized with HTTP 511 and include class path,
message and traceback. **Code**, `SWE-agent/SWE-ReX@5c995c3 src/swerex/server.py:31-35,105-162`.

The client sets `X-Request-ID`. Its helper supports exponential backoff starting at 0.1 seconds,
jitter and a five-second cap. However, inspected create, run, close, execute, read and write
wrappers do not pass retries, so the helper's default of zero applies. A retry-capable helper is
not evidence that every action is retried. **Code**, `SWE-agent/SWE-ReX@5c995c3`
`src/swerex/runtime/remote.py:165-221`.

The server caches only the last response for request deduplication. Its own comment warns that
concurrent-client idempotency is not guaranteed. This is not an exactly-once journal for
arbitrary commands after disconnect. **Code**, `SWE-agent/SWE-ReX@5c995c3`
`src/swerex/server.py:42-61,77-102` (inference).

### F3. Sessions are persistent shells, not durable agent sessions

Local bash sessions use `pexpect` and live in an in-memory session map. Duplicate or missing
session names are errors. Shell interruption has its own retries and timeout. A runtime process
restart loses the Python session registry and its shell ownership. Saved agent messages are not
part of this runtime contract. **Code**, `SWE-agent/SWE-ReX@5c995c3`
`src/swerex/runtime/local.py:129-173,186-216,365-416` (inference).

The interrupt action defaults to a 0.2-second timeout and three retries. Command timeout and
shell exit are expressed through action exceptions or observations, not a task enum such as
waiting-for-user. The caller decides whether to retry, ask for help or end the coding task.
**Code**, `SWE-agent/SWE-ReX@5c995c3 src/swerex/runtime/abstract.py:50-128`,
`src/swerex/runtime/local.py:274-351` (inference).

### F4. Provider lifecycle and cleanup

Deployment configurations include Docker or Podman, Modal, Fargate, remote and Daytona.
Docker defaults include a Python 3.11 image, 180-second startup timeout, missing-image pulls and
container removal. The implementation generates an auth token, launches the runtime server,
waits for readiness and removes configured resources on stop. **Code**,
`SWE-agent/SWE-ReX@5c995c3 src/swerex/deployment/config.py:23-44,95-108,135-171,198-221`,
`src/swerex/deployment/docker.py:117-121,233-321`.

Modal creates a sandbox, exposes its tunnel and terminates it on stop. Fargate launches an ECS
task, obtains its public IP and stops the task at cleanup. Remote deployment attaches to
externally owned compute. Stopping that runtime closes shell sessions without proving that the
external machine was deleted. **Code**, `SWE-agent/SWE-ReX@5c995c3`
`src/swerex/deployment/modal.py:208-260`, `src/swerex/deployment/fargate.py:123-174`,
`src/swerex/deployment/remote.py:15-22,59-73`.

There is no verified warm-pool allocator, git credential broker, transcript replay service or
PR controller in this inspected boundary. Those responsibilities remain with the embedding
agent application. **Docs**, §F1 and **Code**, §F2 to §F4 (inference).

## G. Terragon

### G1. Public snapshot and shutdown must both be stated

Terragon's source is public as `terragon-labs/terragon-oss`, under Apache-2.0. Its README calls it
an unsupported snapshot and dates its notice January 16, 2026. The released shutdown page gives
the actual hosted-service end as February 9, 2026 and says sandboxes would be terminated.
The snapshot's later repository push does not mean the service remains available. **Docs**,
`terragon-labs/terragon-oss@83142a1 README.md:1-20`,
`apps/docs/content/docs/resources/shutdown.mdx:3-27`.

### G2. Threads separate repository state from agent chats

PostgreSQL and Drizzle store a thread with user, repository, base and work branch, PR or issue,
sandbox ID, provider, size, sandbox status, git diff, automation linkage and parent thread.
Version-zero records embed chat fields. Version-one threads have separate `thread_chat` rows.
Each chat has agent, status, messages JSONB, `queuedMessages`, native `sessionId`, error data,
schedule time and reattempt time. Chats share their parent thread's sandbox. **Code**,
`terragon-labs/terragon-oss@83142a1 packages/shared/src/db/schema.ts:248-368`.

The XState machine has `draft`, `scheduled`, `queued`, `queued-tasks-concurrency`,
`queued-sandbox-creation-rate-limit`, `queued-agent-rate-limit`, `booting`, `working`, `stopping`,
`working-done`, `working-error`, `checkpointing` and `complete`. Draft can queue or schedule.
Boot starts from queued work. Done or error proceeds to checkpointing, then complete.
New work on a complete chat queues again. **Code**, `terragon-labs/terragon-oss@83142a1`
`apps/www/src/agent/machine.ts:32-151`.

Some `system.error` transitions also lead to `complete`, so complete does not mean success.
Deprecated status strings remain in shared types. The transition machine is stronger evidence
for actual execution than treating every old type member as a currently reachable state.
**Code**, `terragon-labs/terragon-oss@83142a1 apps/www/src/agent/machine.ts:32-151`,
`apps/www/src/agent/thread-status.ts:57-81` (inference).

### G3. The loop runs inside the sandbox

The sandbox daemon listens on a Unix socket. Commands include `claude`, `ping`, `kill` and
`stop`. Despite the generic command name, start messages select different agents and carry
token, prompt, native session ID, thread ID, chat ID and permission mode. The daemon dispatches
Claude, Amp, Codex, Gemini and OpenCode. **Code**, `terragon-labs/terragon-oss@83142a1`
`packages/daemon/src/shared.ts:17-60`, `packages/daemon/src/daemon.ts:183-349,615-862`.

An active-process map is keyed by chat. Starting the same chat replaces its existing process.
This is more specific than the daemon README's older single-active-agent statement.
The default no-output watchdog is 300000 milliseconds and can terminate an agent with an error.
**Code**, `terragon-labs/terragon-oss@83142a1`
`packages/daemon/src/daemon.ts:183-349,491-551`.
**Docs**, `terragon-labs/terragon-oss@83142a1 packages/daemon/README.md:41-70`.

The daemon batches callback messages to `/api/daemon-event` using a per-thread token.
Payload includes messages, timezone, thread and chat. The receiver converts them into stored
chat messages and saves native session identity. PartyKit broadcasts data invalidations to
clients. This is not evidence of a durable token-by-token replay cursor or an on-disk daemon
outbox. **Docs**, `terragon-labs/terragon-oss@83142a1`
`packages/daemon/README.md:3-7`, `apps/broadcast/README.md:3`.
**Code**, `terragon-labs/terragon-oss@83142a1`
`packages/daemon/src/daemon.ts:1163-1198`,
`apps/www/src/server-lib/handle-daemon-event.ts:180-189,267-279` (inference).

### G4. Sandbox pause, follow-up queues and admission

Provider selection supports E2B and Daytona. Docker is for development and tests, with mock
providers for tests. E2B create and resume use a 15-minute timeout and the then-private
`autoPause` option. Resume retries three times with one-second delay and verifies a command can
run. The implementation also exposes explicit provider pause. **Code**,
`terragon-labs/terragon-oss@83142a1 packages/sandbox/src/provider.ts:8-35`,
`packages/sandbox/src/providers/e2b-provider.ts:13-71,185-227`.

Hibernation is guarded by shared sandbox-resource tracking. Redis records active chat IDs,
terminal use and other active users. Chat markers expire after a day and temporary resource
counters after ten minutes. This avoids assuming that one finished chat means a shared sandbox
is idle. **Code**, `terragon-labs/terragon-oss@83142a1`
`apps/www/src/agent/sandbox-resource.ts:3-12,27-58,125-139`,
`apps/www/src/agent/sandbox.ts:333-362` (inference).

Follow-ups persist in `queuedMessages`. Processing treats an initial slash command separately,
otherwise combining queued messages into a prompt. Agent-rate-limited chats are not processed.
Admission checks active-thread count against the user's concurrent-task allowance and checks
the sandbox-creation rate limit. Dequeue is atomic, so two workers do not simply select the
same queued chat and both boot it. **Code**, `terragon-labs/terragon-oss@83142a1`
`apps/www/src/server-lib/process-follow-up-queue.ts:9-101`,
`apps/www/src/server-lib/process-queued-thread.ts:16-73`.

### G5. Git checkpoints, credential choice and feedback

Sandbox setup receives the user's name, email, GitHub access token, repository, base branch,
branch generator, environment variables, model credentials and setup script. Repository-specific
environment values override global values. Stored credentials and environment values are
decrypted using `ENCRYPTION_MASTER_KEY`. **Code**, `terragon-labs/terragon-oss@83142a1`
`apps/www/src/agent/sandbox.ts:157-253`, `apps/www/src/agent/credentials.ts:9-35`.

PR completion protects the default branch, commits or pushes changes, finds an existing PR and
creates or updates one. One comment says installation token, but the actual create path calls
`getOctokitForUserOrThrow`. That helper obtains the user's access token. Separate read or update
paths also use App installation tokens. Authorship and authorization must therefore be read per
operation, not inferred from one comment. **Code**, `terragon-labs/terragon-oss@83142a1`
`apps/www/src/agent/pull-request.ts:59-179,204-276`,
`apps/www/src/lib/github.ts:235-275`.

Webhook handlers route issue comments, inline review comments and submitted reviews that mention
the app. Review comments include diff context and thread context. Scheduled and queued-task cron
routes are present. These supply event-driven follow-up work rather than creating a new sandbox
for every reviewer message. **Code**, `terragon-labs/terragon-oss@83142a1`
`apps/www/src/app/api/webhooks/github/handlers.ts:203-374`,
`apps/www/src/app/api/internal/cron/automations/route.ts:7-31`,
`apps/www/src/app/api/internal/cron/queued-tasks/route.ts:16-32` (inference).

## H. Imbue Sculptor

### H1. It is public code, but desktop ownership is still important

The old brief entry in `cloud-agents.md §B9` needs updating. Sculptor now has public MIT source.
Its README calls it an experimental research preview. A user connects a repository, creates a
workspace and can add multiple agents. Pi and Claude Code are integrated, with terminal agents
also supported. **Docs**, `imbue-ai/sculptor@5ebd30f README.md:3-59`.

SQLite persists immutable event tables and materialized latest views. Alembic migrations run
at startup. Projects record local repository URLs, setup commands and naming patterns.
Workspaces record project, initialization strategy, source or target branch, environment
identity, setup progress, diff state and CI-babysitter pause. Initialization strategies are
`IN_PLACE`, `CLONE` and `WORKTREE`. **Docs**, `imbue-ai/sculptor@5ebd30f`
`sculptor/sculptor/database/README.md:3-14,39-44`.
**Code**, `imbue-ai/sculptor@5ebd30f sculptor/sculptor/database/models.py:46-122`,
`sculptor/sculptor/database/workspace_enums.py:8-26`.

### H2. Durable tasks are restored, not immortal subprocesses

A task links user, project, typed input, optional `max_seconds`, state, outcome and error.
Saved messages link to task and record source and partial-message status. Agent state also
stores terminal-session identity, terminal shell PID and harness-specific continuation state.
**Code**, `imbue-ai/sculptor@5ebd30f`
`sculptor/sculptor/database/models.py:184-213,228-322`.

Task outcomes are exactly `QUEUED`, `RUNNING`, `FAILED`, `CANCELLED`, `DELETED` and `SUCCEEDED`.
The task-service contract says local subprocesses do not outlive the server. On backend restart,
previously running tasks are changed to queued. The spawner claims queued tasks as running.
That is durable task restoration followed by new execution, not proof that the same process
survived. **Code**, `imbue-ai/sculptor@5ebd30f`
`sculptor/sculptor/interfaces/agents/tasks.py:6-14`,
`sculptor/sculptor/services/task_service/concurrent_implementation.py:205-227`.
**Docs**, `imbue-ai/sculptor@5ebd30f sculptor/sculptor/services/task_service/api.py:33-44`.

The outcome enum is not a complete conversational state machine. Harness messages and saved
task state express active turns and interruption. The inspected enum has no waiting-for-user
member and should not acquire one merely to fit a generic comparison. **Code**, §H2 (inference).

### H3. Client snapshots, harness messages and stop escalation

The backend exposes `/api/v1/stream/ws`. Its stream sends an initial state dump and then scoped
incremental updates. Scopes can select all data, a project, workspace or agent. Setup output has
its own sequence and persisted-log handling. The inspected global stream is snapshot-based,
not a verified durable `Last-Event-ID` protocol. **Code**, `imbue-ai/sculptor@5ebd30f`
`sculptor/sculptor/web/app.py:3774-3786`,
`sculptor/sculptor/web/streams.py:123-213,223-263,585-667` (inference).

Claude's wrapper routes input messages and interrupt requests to a process manager. The manager
sends a stdin control request with subtype `interrupt`. It first tries a graceful protocol stop,
then escalates after a five-second wait to process-group termination and further cleanup.
Saved native session identity supports later continuation. **Code**, `imbue-ai/sculptor@5ebd30f`
`sculptor/sculptor/agents/default/claude_code_sdk/agent_wrapper.py:88-107`,
`sculptor/sculptor/agents/default/claude_code_sdk/process_manager.py:320-466,472-476`.

The implementation can resolve the visible request as interrupted even if an unresponsive
worker must be abandoned. This distinguishes UI completion from the lower-level cleanup path,
an important boundary for any claim that cancel means all execution has already stopped.
**Code**, `imbue-ai/sculptor@5ebd30f`
`sculptor/sculptor/agents/default/claude_code_sdk/process_manager.py:404-435` (inference).

### H4. Container and remote mode do not establish an always-on service

The experimental container backend runs the whole backend, not one container per agent.
A custom command prints a URL and the desktop connects to it. The example forwards shutdown
signals so closing the desktop stops the container. A custom command can instead establish an
SSH tunnel to a remote backend, but the mechanism is explicitly experimental. **Docs**,
`imbue-ai/sculptor@5ebd30f docs/help/experimental/container_backend.md:3-18,106-122`.

The example forwards `CLAUDE_CODE_OAUTH_TOKEN` into the container. PR creation pushes the
workspace branch and uses GitHub CLI authentication from `gh auth login`. The UI tracks PR
status, reviews and CI. This is different from a verified GitHub App installation-token service.
**Docs**, `imbue-ai/sculptor@5ebd30f`
`docs/help/experimental/container_backend.md:70-82`, `docs/help/pull_requests.md:3-22`.

Multiple agents and workspace strategies offer parallel work, but the inspected sources do not
establish managed warm pools, RAM hibernation, external issue or chat dispatch, a numeric global
admission limit or automatic best-attempt selection. **Docs**, §H1 and §H4 (inference). See Gaps.

## I. Additional public implementations, kept brief

### I1. Goose: scheduled execution belongs to a running server

Goose's current public repository is `aaif-goose/goose`. Session data uses `sessions.db`.
Session types include `user`, `scheduled`, `sub_agent`, `hidden`, `terminal`, `gateway` and `acp`.
Sessions record working directory, provider, model, schedule, recipe, usage and parent session.
**Code**, `aaif-goose/goose@c3bfc5f crates/goose/src/session/session_manager.rs:33-34,60-110`.

The scheduler persists job definitions as JSON. It handles five-field legacy cron by adding
seconds, supports cancellation tokens and records running-session state. Startup clears stale
running flags before restoring schedules. This is not automatic continuation of a lost agent
process. ACP request types include create, update, pause, unpause, inspect, kill and run-now
schedule operations. **Code**, `aaif-goose/goose@c3bfc5f`
`crates/goose/src/scheduler/full.rs:75-94,137-166,198-232,408-464`,
`crates/goose/src/acp/server/schedule.rs:1-14` (inference).

The callback checks schedule pause before starting. The inspected slice does not establish a
distributed scheduler lease, cross-replica exactly-once dispatch or a complete server replay
contract. A JSON running flag alone is not such a guarantee. **Code**, `aaif-goose/goose@c3bfc5f`
`crates/goose/src/scheduler/full.rs:175-198` (inference).

### I2. Happy: encrypted remote control is not sandbox ownership

Happy's current README distinguishes the original CLI and relay from separate desktop and
Happy Agent repositories. The original CLI wraps Claude Code or Codex in a terminal. The mobile
app continues those sessions with encrypted synchronization. The split matters when describing
what this particular repository owns. **Docs**, `slopus/happy@d6b85a0 README.md:30-70`.

The Prisma model has accounts, sessions, machines and messages. A session has encrypted-data
key material, metadata or agent-state versions, sequence and liveness fields. Messages have
`sessionId`, `localId`, `seq` and JSON content. The unique session/local-ID pair supports
deduplication. The original history route returns the newest 150 messages, including sequence,
but takes no replay cursor in its inspected schema. **Code**, `slopus/happy@d6b85a0`
`packages/happy-server/prisma/schema.prisma:95-120,148-160,236-249`,
`packages/happy-server/sources/app/api/routes/sessionRoutes.ts:367-416`.

The shared event vocabulary includes text, service, tool-call start or end, file, turn start,
turn end and stop. Turn-end status is `completed`, `failed` or `cancelled`. These relay records
do not establish a provisioner, a PR controller or survival after the machine running the
original CLI shuts down. **Code**, `slopus/happy@d6b85a0`
`packages/happy-wire/src/sessionProtocol.ts:19-107` (inference).

### I3. Omnara: the current project is broader than a mobile monitor

Omnara now describes itself as a self-hostable managed-agent platform under Apache-2.0.
The quickstart launches agents from configuration with model provider and machine-pool sources.
This note does not substitute an older monitor-only description for that current product.
**Docs**, `omnara-ai/omnara@1bcdea7 README.md:10-40,42-63`.

Its durable event kinds are `agent_input`, `model_output`, `tool_result` and
`context_checkpoint`. An event carries agent ID, positive sequence, timestamp and idempotency
key. The documented history API uses `after_sequence`, `before_sequence` and page size up to 500.
**Code**, `omnara-ai/omnara@1bcdea7 internal/events/event.go:11-27,37-53`.
**Docs**, `omnara-ai/omnara@1bcdea7 docs/events/streaming.mdx:7-38`.

SSE uses durable sequence as event ID and ten-second heartbeat comments. Clients reconnect
with `Last-Event-ID` or `after_sequence`. `model_output_delta` and `tool_call_update` are not
saved. Final model output replaces preview by `model_call_context_id`. Disconnected deltas are
not replayed, but completed output is. The deeper worker, provider and integration state
machines were not traced in this brief slice. **Docs**, `omnara-ai/omnara@1bcdea7`
`docs/events/streaming.mdx:55-109`.

## What a cloud-agent control plane must provide

These are deductions from the implementation evidence, not a product design. Each item is an
inference about responsibilities that need an explicit owner when building the described class
of service.

1. **Separate identities and state machines.** Keep task intent, execution attempt, native agent
   session, sandbox and client subscription distinct. A completed process need not mean a done
   task or an idle sandbox. **Code**, §A2, §B1, §C1 and §G2 (inference).
2. **Durable execution ownership.** Record who may run a conversation, how ownership expires and
   how stale results are fenced. Persisted running state is not proof a worker still exists.
   **Code** and **Docs**, §A4, §B4 and §H2 (inference).
3. **Explicit admission and retry outcomes.** Distinguish saved input from admitted execution.
   Bound queues and make overflow, rate limits and concurrency waits visible. Avoid resending
   a message when only execution admission failed. **Code**, §A4, §B5, §D2 and §G4 (inference).
4. **A defined agent adapter contract.** Specify native session persistence, follow-up, fork,
   steering, approval and cancellation per harness. Terminal screen parsing, ACP and native
   app-server protocols are not interchangeable. **Code** and **Docs**, §A3, §B2, §C4 and §H3
   (inference).
5. **A replayable record plus disposable preview.** Give durable events stable identity and a
   reconnect strategy. Define snapshot replacement, preview reset and slow-client behavior.
   Do not advance a replay cursor for unsaved deltas. **Code** and **Docs**, §A5, §B4, §C3, §H3
   and §I3 (inference).
6. **Conservative sandbox reconciliation.** Distinguish disconnected, stopped, suspended and
   deleted compute. Preserve identity when the only uncommitted work may still be there.
   Make cleanup ownership and retention explicit. **Code**, §A6, §C2, §D3 and §F4 (inference).
7. **Precise stop and resume semantics.** State whether stopping cancels a turn, kills a
   subprocess, closes shells, stops compute or preserves memory. Document escalation and what
   can be restored after each failure. **Code** and **Docs**, §A4, §A6, §F3, §G4 and §H3
   (inference).
8. **Credential scope per operation.** Separate model authentication, runtime transport keys,
   git push credentials and PR API identity. Keep their refresh and rotation paths distinct.
   Output redaction is not an egress boundary. **Code** and **Docs**, §A8, §B6, §D5, §E4 and §G5
   (inference).
9. **Git and artifact completion outside individual turns.** Own branch isolation, checkpoints,
   commits, push, PR creation and review-context routing. Multiple agents sharing one workspace
   are not isolated attempts. **Code**, §C5, §D5, §E3 and §G2 to §G5 (inference).
10. **Durable trigger-to-conversation correlation.** Track delivery identity, issue or chat
    subject and schedule execution separately. Route follow-ups to existing work without
    assuming every webhook means a new sandbox. Transport deadlines remain covered by
    `always-on-agents.md §E1`. **Code**, §A7, §D2, §D6 and §G5 (inference).
11. **Version-aware integration contracts.** Pin protocol and schema versions. A retained
    Terraform resource, old diagram or deprecated status type is not proof an endpoint still
    exists. **Code** and **Docs**, §A1, §B1, §C1, §D1 and §I2 (inference).

## Gaps

- **No deployment experiments.** Source and first-party docs were read, but no agents,
  sandboxes, migrations or external services were run. Operational timings and failure
  guarantees were not measured. Provider documentation defaults are not measured behavior.
- **OpenHands historical depth.** The current SDK and automation split was prioritized.
  The older V0 action server, full legacy resolver orchestration and microagent compatibility
  history were not reconstructed. The archived resolver helpers do not prove today's complete
  clone, branch, author, push and PR transaction.
- **OpenHands runtime service internals.** The remote API client was traced, not the deployed
  provider implementation. Its `/pause` contract does not prove RAM snapshot semantics,
  placement policy, exact idle cleanup or provider-side warm-pool behavior.
- **OpenHands automation execution.** Schema, scheduling and subject routing were traced.
  Full worker retry policy, webhook acknowledgment, deduplication and all cancellation races
  were not established. The subject index was not misrepresented as a uniqueness guarantee.
- **Coder historical support matrix.** Current main removes Tasks, while the Terraform docs
  retain ESR guidance. Exact supported older Premium release combinations were not tested.
  Current PR author policy, review-webhook dispatch and deployment-specific capacity numbers
  were not verified.
- **Vibe Kanban remote compute.** Cloud synchronization and a signed host-relay surface were
  verified. Managed sandbox ownership, full relay reconnect behavior, default git author and
  a universal executor concurrency cap were not established.
- **Open SWE upstream dependency.** Current execution depends on MDA. Its deployed checkpoint
  storage, stream retention, worker leases and concurrency policy are not fully visible in
  this repository. The old manager, planner and programmer version was not reconstructed.
  A current label-only issue trigger and mandatory plan-approval execution gate were not
  verified. Plan metadata alone was not treated as that gate.
- **Egress and secret limits.** No universal deny-by-default egress claim is made for these
  projects. Transport authentication, a credential proxy and output redaction were kept
  separate from provider-enforced network policy.
- **Terragon snapshot durability.** Callback-to-database persistence was traced, but an
  on-disk daemon outbox, cursor-based client replay, restart recovery after partial callback
  delivery and complete authentication middleware were not established. Hosted behavior
  cannot now be verified because the official service ended.
- **Sculptor remote ownership.** Public source and experimental remote or container options
  were verified. Supported independent cloud-worker deployment, admission ceilings and
  external-trigger guarantees were not established.
- **Parallel attempts.** Branch or workspace isolation and child-agent relations were found.
  A general persisted best-of-N comparison and selection workflow was not verified across
  these products. Concurrent sessions in a shared workspace were not called isolated runs.
- **Optional projects.** Goose, Happy and Omnara received bounded source slices, not complete
  worker audits. Happy's separate desktop and Agent repositories were not traced. Omnara's
  machine-pool and integration internals were not traced. Crystal and claude-squad were not
  researched beyond repository discovery, so no architecture claims are made about them.
