# How agent task and session APIs are shaped, for Hibernaut

**Date checked:** 2026-10-24.

**Summary**

This is one input to the Hibernaut cloud-agent design. It covers the public resources, state
machines and delivery contracts for creating, following, steering and finishing agent work.
`cloud-agents.md` surveys products. `cloud-agent-internals.md` covers execution and workers.
`always-on-agents.md` covers hosting, triggers and channel transport. This note builds on those
three notes and stays on the client side of each API.

- **A conversation is not a run.** Cursor v1 and OpenAI Agents separate durable sessions from
  individual turns. LangSmith separates threads from runs. A2A groups tasks with `contextId`.
  **Docs** (inference), §§A1, C1, D1 and H3.
- **A new message needs an explicit concurrency contract.** LangSmith names four policies.
  Cursor rejects ordinary concurrent runs but offers steering. Claude queues events and exposes
  when they were processed. These are different promises. **Docs** (inference), §§C2, D2 and G2.
- **Idle does not prove success.** Claude can be idle for approval or budget exhaustion.
  Devin can report `running` with detail `finished`. OpenAI has separate session and turn
  outcomes. **Docs** (inference), §§E2, G2 and H3.
- **Streaming recovery has several incompatible shapes.** A2A resubscribes with a snapshot.
  Cursor and background Responses replay from cursors. Claude and OpenAI Agents require
  reconciliation with saved history. **Docs** (inference), §§A3, D3, G3 and H1 to H3.
- **Human decisions need durable correlation.** A message is enough for some APIs. Others
  require a tool-call ID, interrupt ID or explicit plan approval. A session-level waiting flag
  is not the complete approval request. **Docs** (inference), §§B3, F2, G2, H4 and K2.
- **A webhook is not necessarily a durable event log.** A2A mandates one attempt but makes
  retries optional. Claude drops an event after three attempts. OpenAI retries for up to
  72 hours. **Docs** (inference), §§A4, G4 and H5.
- **Version and identity changes affect the API contract.** A2A 1.0 changes method names.
  Cursor v1, Devin v3, GitHub's task preview and Slack's agent sessions differ from their older
  surfaces. App identity also differs from user identity. **Docs** (inference), §§A1, B1, D1,
  E1, I1 and J1.

## Sources

Doc citations name primary sources. Code uses `owner/repo@sha7 path:lines`. Full SHAs follow.

- **A2A.** [Specification][a2a] 1.0.0. Protobuf and governance: `a2aproject/A2A`,
  release commit `173695755607e884aa9acf8ce4feed90e32727a1`.
- **Linear.** [GraphQL Agent API][linear], interaction docs, webhook docs and plan preview.
- **LangSmith Deployment.** [Reference][langsmith] and [OpenAPI][langsmith-spec].
  OpenAPI 3.1.0, API info version 0.1.0. SDK: `langchain-ai/langgraph`,
  `4c52e129eb1ab69a3da58b799f67929f77d81149`.
- **Cursor.** [v1 reference][cursor] and [OpenAPI][cursor-spec], plus legacy v0 docs.
  Public beta v1, OpenAPI 3.0.3, API info version 1.0.0.
- **Devin.** [API reference][devin]. API v3, OpenAPI 3.1.0, API info version 3.0.0.
- **Jules.** [Guide][jules] and [discovery][jules-spec], `v1alpha`, revision `20261022`.
- **Claude Managed Agents.** [Documentation][claude]. Beta `managed-agents-2026-04-01`,
  API version header `2023-06-01`.
- **OpenAI.** [API reference][openai-auth], Responses guides and Agents session docs.
  Agents beta `agents=v1`. SDK: `openai/openai-python`,
  `718aee979a9c334d37302aeff21cee6cbff4d43c`.
- **GitHub.** [CLI][github-cli] and [REST preview][github-rest], version `2026-03-10`.
  CLI source: `cli/cli`, `4f2cfe92e3499f8581bf39db77e0a1431fa7b55c`.
- **Slack.** [Agent documentation][slack], session guide and Web API method references.
- **AG-UI.** [Overview][agui], [events][agui-events] and [interrupts][agui-interrupts].
  Protocol 1.0, `@ag-ui/core` 1.0.3. Source: `ag-ui-protocol/ag-ui`,
  `73b04fdaec1f1b170a5efeeda3c050247a26075b`.

**Tags.** **Docs**: read on the vendor's docs, changelog or blog on the date above. **Code**: read
in source at the named commit. **Unverified**: only the named secondary source says so. News and
forums served only as leads. Quotes are verbatim. "(paraphrase)" marks spots where a quote would
break this note's prose rules. "(inference)" marks my own deductions. An untagged claim takes the
tag of its sentence's citation.

[a2a]: https://a2a-protocol.org/v1.0.0/specification
[linear]: https://linear.app/developers/agents
[linear-interaction]: https://linear.app/developers/agent-interaction
[linear-signals]: https://linear.app/developers/agent-signals
[linear-best]: https://linear.app/developers/agent-best-practices
[linear-hooks]: https://linear.app/developers/webhooks
[langsmith]: https://docs.langchain.com/langsmith/server-api-ref
[langsmith-spec]: https://docs.langchain.com/langsmith/agent-server-openapi.json
[langsmith-multitask]: https://docs.langchain.com/langsmith/double-texting
[langsmith-stream]: https://docs.langchain.com/langsmith/streaming
[langsmith-hooks]: https://docs.langchain.com/langsmith/use-webhooks
[langsmith-crons]: https://docs.langchain.com/langsmith/cron-jobs
[cursor]: https://cursor.com/docs/cloud-agent/api/endpoints
[cursor-spec]: https://cursor.com/docs-static/cloud-agents-openapi.yaml
[cursor-v0]: https://cursor.com/docs/cloud-agent/api/v0
[cursor-hooks]: https://cursor.com/docs/cloud-agent/api/webhooks
[cursor-auth]: https://cursor.com/docs/api
[devin]: https://docs.devin.ai/api-reference/overview
[devin-auth]: https://docs.devin.ai/api-reference/authentication
[devin-create]:
  https://docs.devin.ai/api-reference/v3/sessions/post-organizations-sessions
[devin-get]: https://docs.devin.ai/api-reference/v3/sessions/get-organizations-session
[devin-send]:
  https://docs.devin.ai/api-reference/v3/sessions/post-organizations-sessions-messages
[devin-messages]:
  https://docs.devin.ai/api-reference/v3/sessions/get-organizations-session-messages
[devin-delete]:
  https://docs.devin.ai/api-reference/v3/sessions/delete-organizations-sessions
[devin-upload]:
  https://docs.devin.ai/api-reference/v3/attachments/post-organizations-attachments
[devin-flows]: https://docs.devin.ai/api-reference/common-flows
[jules]: https://developers.google.com/jules/api
[jules-spec]: https://jules.googleapis.com/$discovery/rest?version=v1alpha
[claude]: https://platform.claude.com/docs/en/managed-agents/overview
[claude-sessions]: https://platform.claude.com/docs/en/managed-agents/sessions
[claude-events]: https://platform.claude.com/docs/en/managed-agents/events-and-streaming
[claude-operations]: https://platform.claude.com/docs/en/managed-agents/session-operations
[claude-permissions]: https://platform.claude.com/docs/en/managed-agents/permission-policies
[claude-reference]: https://platform.claude.com/docs/en/managed-agents/reference
[claude-outputs]: https://platform.claude.com/docs/en/managed-agents/define-outcomes
[claude-hooks]: https://platform.claude.com/docs/en/managed-agents/webhooks
[openai-background]: https://developers.openai.com/api/docs/guides/background
[openai-state]: https://developers.openai.com/api/docs/guides/conversation-state
[openai-conversations]: https://developers.openai.com/api/reference/resources/conversations
[openai-agents]: https://developers.openai.com/api/docs/guides/agents-api/sessions
[openai-manage]: https://developers.openai.com/api/docs/guides/agents-api/sessions/manage
[openai-events]: https://developers.openai.com/api/docs/guides/agents-api/sessions/events
[openai-functions]: https://developers.openai.com/api/docs/guides/agents-api/tools/functions
[openai-computer]: https://developers.openai.com/api/docs/guides/agents-api/tools/computer-use
[openai-files]: https://developers.openai.com/api/docs/guides/agents-api/environments/files
[openai-agent-hooks]: https://developers.openai.com/api/docs/guides/agents-api/sessions/webhooks
[openai-hooks]: https://developers.openai.com/api/docs/guides/webhooks
[openai-data]: https://developers.openai.com/api/docs/guides/your-data
[openai-auth]: https://developers.openai.com/api/reference/overview
[github-cli]: https://cli.github.com/manual/gh_agent-task
[github-rest]: https://docs.github.com/en/rest/agent-tasks/agent-tasks
[github-assignment]:
https://docs.github.com/copilot/how-tos/use-copilot-agents/cloud-agent/use-cloud-agent-via-the-api
[github-partners]:
  https://docs.github.com/en/copilot/concepts/agents/about-third-party-agents
[slack]: https://docs.slack.dev/ai/developing-agents
[slack-migration]: https://docs.slack.dev/ai/migrating-to-agent-messaging
[slack-sessions]: https://docs.slack.dev/ai/agent-sessions
[slack-thread]: https://docs.slack.dev/reference/events/assistant_thread_started
[slack-status]: https://docs.slack.dev/reference/methods/assistant.threads.setStatus
[slack-prompts]: https://docs.slack.dev/reference/methods/assistant.threads.setSuggestedPrompts
[slack-start]: https://docs.slack.dev/reference/methods/chat.startStream
[slack-append]: https://docs.slack.dev/reference/methods/chat.appendStream
[slack-stop]: https://docs.slack.dev/reference/methods/chat.stopStream
[agui]: https://docs.ag-ui.com/introduction
[agui-events]: https://docs.ag-ui.com/concepts/events
[agui-interrupts]: https://docs.ag-ui.com/concepts/interrupts

## Comparison table

Sections A to K supply the tagged evidence. "Not found" means not verified in the checked public
contract, not proof that a private implementation lacks the feature. States are abbreviated here.

| API | Top entities | States | Message while running | Human input | Artifacts | Streaming and resume cursor | Webhooks | Idempotency |
|---|---|---|---|---|---|---|---|---|
| **A2A, §A** | AgentCard, Task, Message, Artifact, context | Submitted, working, two blocked states, four terminal states | Server may accept, policy unspecified | Message on same task, auth-required | Typed Parts and incremental Artifacts | SSE, resubscribe starts with current Task, no replay cursor mandated | Per-task push config, at least one attempt | Message dedup optional, cancel idempotent |
| **Linear, §B** | App user, issue, AgentSession, AgentActivity | Pending, active, awaitingInput, complete, error, stale | Prompted webhook, scheduling policy unspecified | Elicitation plus prompt or auth signal | Response, plan and external URLs | GraphQL reads and webhooks, no SSE cursor found | AgentSessionEvent, signed, 5-second ack | Delivery UUID, no create key found |
| **LangSmith, §C** | Assistant, thread, run, checkpoint, cron | Separate run and thread enums | Reject, interrupt, rollback or enqueue | Checkpoint interrupt and command.resume | Graph state, messages and custom output | Run SSE Last-Event-ID, protocol v2 seq and since | Run completion callback, auth headers | Thread conflict controls, no run key verified |
| **Cursor, §D** | Durable agent, run, environment | Agent ACTIVE/IDLE/ARCHIVED, separate run outcome | Ordinary 409, optional steer or queue | Prompt and plan mode, no typed approval found | Final text, branches, PRs and files | Per-run SSE Last-Event-ID, finite retention | v0 only, statusChange, HMAC | Client agentId conflicts, steer may duplicate |
| **Devin, §E** | Organization, session, message, playbook | Seven session states plus status_detail | Accepted message, suspended auto-resumes, scheduling unclear | Waiting details, message, no typed decision verified | Structured JSON, PRs and attachments | Poll sessions and messages with after cursor | Outgoing session webhook not found | No create or message key found |
| **Jules, §F** | Source, session, activity, plan | Queue, approval, feedback, work, pause and terminal states | sendMessage, scheduling unspecified | requirePlanApproval and approvePlan | Patch, command output, media and PR | Poll resources, pageToken, activity time filter | Not found | No request key found |
| **Claude, §G** | Versioned agent, environment, session, event | Idle, running, rescheduling, terminated | Ordered event queue, explicit interrupt | Correlated tool confirmation or custom result | Messages, outcomes and Files API | Live SSE plus saved history and event-ID dedup | Signed notifications, three total attempts | Duplicate custom result harmless, general key unverified |
| **OpenAI, §H** | Response, conversation, agent session, turn, item | Different response, session and turn states | New Response or Agents session steering | Required actions with turn and call IDs | Items, structured output and published files | Responses sequence_number, Agents live stream plus saved items | Signed, up to 72-hour retry window | Agents input Idempotency-Key, cancel Response idempotent |
| **GitHub, §I** | Task, session, repository, artifact | Eight task states | PR or issue interaction, no REST steering endpoint verified | waiting_for_user, no generic approval RPC found | Pull request and branch artifacts | REST polling, CLI --follow polls logs | No task-specific delivery contract verified | No create key found |
| **Slack, §J** | Channel, thread, agent session, message, task card | Processing, active, suspended, closed | App receives message and chooses policy | Interactive blocks or normal messages | Messages, blocks, task output and sources | Producer start/append/stop calls, no consumer replay cursor | Events API, 3-second ack | Event dedup differs from write idempotency |
| **AG-UI, §K** | Thread, run, event, message, interrupt | Lifecycle events and success/interrupt outcome | Ordinary concurrency unspecified, pending interrupts block input | resume array with interrupt IDs | Result, state patches and tool output | Event transport, no universal replay cursor | Not a hosted webhook contract | Interrupt resume replay must be safe |

## A. A2A

### A1. Version, discovery and resource identity

**Docs.** The latest released specification is 1.0.0. It defines JSON-RPC, gRPC and HTTP+JSON
bindings for the same operations. The canonical schema is protobuf, not the generated JSON
schema. This is deeper than the brief A2A mention in `cloud-agents.md §C`. [Specification][a2a].

**Code.** `AgentCard` has name, description, version, `supported_interfaces`, capabilities,
security schemes, security requirements, input/output modes and skills. Each interface names
its URL, binding and protocol version. Streaming and push notifications are advertised
capabilities. `a2aproject/A2A@1736957 specification/a2a.proto:336-446`.

**Code.** `Task` has `id`, `context_id`, status, artifacts, history and metadata.
`Message` has `message_id`, optional task/context IDs, role, Parts and referenced task IDs.
A Part contains text, raw bytes, a URL or JSON data, plus optional filename and media type.
An Artifact has its own ID and Parts.
`a2aproject/A2A@1736957 specification/a2a.proto:167-293`.

**Code.** One context groups related tasks, not one perpetual mutable task. Sending only a
context ID can start another task. A task ID lets the server infer the context. Conflicting
task/context pairs must be rejected.
`a2aproject/A2A@1736957 docs/specification.md:625-645`.

**Code.** Current JSON-RPC method names are `SendMessage`, `SendStreamingMessage`, `GetTask`,
`ListTasks`, `CancelTask` and `SubscribeToTask`. REST maps these to `POST /message:send`,
`POST /message:stream`, `GET /tasks/{id}`, `GET /tasks`, `POST /tasks/{id}:cancel` and
`POST /tasks/{id}:subscribe`. Names such as `message/send` and `tasks/resubscribe` belong to
older versions. `a2aproject/A2A@1736957 docs/specification.md:1166-1173`.

### A2. State and human intervention

**Code.** The exact protobuf enum is `TASK_STATE_UNSPECIFIED`, `TASK_STATE_SUBMITTED`,
`TASK_STATE_WORKING`, `TASK_STATE_COMPLETED`, `TASK_STATE_FAILED`, `TASK_STATE_CANCELED`,
`TASK_STATE_INPUT_REQUIRED`, `TASK_STATE_REJECTED` and `TASK_STATE_AUTH_REQUIRED`.
The old `unknown` value is not in this enum.
`a2aproject/A2A@1736957 specification/a2a.proto:187-207`.

**Code.** Completed, failed, canceled and rejected tasks are terminal. Another message cannot
restart them. A server may accept messages on nonterminal tasks, but the protocol does not
choose enqueue, interrupt or rollback behavior. Answer an input request with a message carrying
the same task and context IDs. Start another task for a follow-up to terminal work.
`a2aproject/A2A@1736957 docs/specification.md:176-178,569,634-645`.

**Docs.** `TASK_STATE_AUTH_REQUIRED` signals authentication, not a general approval enum.
The client performs the required authentication out of band. Ordinary input and tool-specific
approvals can be represented in Parts, but their business semantics need agreement between the
agents. The core does not define a universal approve-tool operation. [Specification][a2a].

### A3. Watching, artifacts and recovery

**Code.** A streaming send starts with a Task, then status or artifact updates. A new subscription
must first send the current Task. This closes the get/subscribe race. Subscribing to a terminal
task is unsupported. Multiple subscribers may follow a task.
`a2aproject/A2A@1736957 docs/specification.md:212,306-313,689`.

**Code.** Artifact updates carry `task_id`, `context_id`, an Artifact, `append` and `last_chunk`.
This supports incremental file or data output without treating every chunk as a chat message.
Results should be Artifacts. Not every Message must persist. Status messages can be lost across
reconnection, so they are not a guaranteed critical-message channel.
`a2aproject/A2A@1736957 specification/a2a.proto:308-321`,
`docs/specification.md:760-766`.

**Code.** `return_immediately` chooses acceptance versus waiting. False or unset waits until a
terminal or interrupted state. True returns promptly so the client can poll, subscribe or use
push. A resubscription is snapshot-first recovery, not a mandated `Last-Event-ID` replay log
(inference). `a2aproject/A2A@1736957 docs/specification.md:448-450,306-313`.

### A4. Push delivery, authentication and idempotency

**Code.** Push configuration has its own ID, task ID, URL, token and authentication scheme with
credentials. The operations create, retrieve, list and delete task push configurations.
AgentCard security can describe API keys, HTTP authentication, OAuth2, OpenID Connect or mTLS.
`a2aproject/A2A@1736957 specification/a2a.proto:90-131,464-509`.

**Code.** A push body is a `StreamResponse` containing exactly one of `task`, `message`,
`statusUpdate` or `artifactUpdate`. Credentials go in headers according to the configuration.
The receiver returns 2xx and tolerates duplicates. At least one attempt is required. Exponential
retry is optional. A 10 to 30 second timeout is recommended, not a guaranteed delivery deadline.
`a2aproject/A2A@1736957 docs/specification.md:864-889`.

**Code.** Send implementations may deduplicate by `messageId`. They are not required to.
Cancellation is idempotent, though a purged task can produce not-found. Deleting a push config
must be idempotent. The specification recommends a retention policy but sets no fixed interval.
`a2aproject/A2A@1736957 docs/specification.md:404,494-498,3185-3187`.

**Code.** The official `a2aproject/A2A` project has Technical Steering Committee oversight.
Its governance document defines committee composition and change procedures.
`a2aproject/A2A@1736957 GOVERNANCE.md:1-39`.

## B. Linear Agent API

### B1. App identity, delegation and sessions

**Docs** (paraphrase). Agents are OAuth app users. Install with `actor=app`, which replaces
`actor=application`. An admin installs the app. The app has a distinct user ID in each
workspace. `app:assignable` enables delegation and `app:mentionable` enables mentions.
An agent becomes the issue's `delegate` while the human can remain `assignee`. [Agents][linear].

**Docs.** A mention or delegation creates an `AgentSession`. An agent can also call
`agentSessionCreateOnIssue` or `agentSessionCreateOnComment`. The session links an issue or
comment to activities and external URLs. `externalUrls` contains `{label,url}` entries.
`externalLink` is deprecated. A PR can be linked without making the PR a native session
artifact. [Agents][linear] and [interaction][linear-interaction].

### B2. State is derived from activities

**Docs.** Session states are `pending`, `active`, `error`, `awaitingInput`, `complete` and
`stale`. Linear derives them from activity rather than exposing an unrestricted state setter.
Send a first activity or external URL within 10 seconds of `created`. This is separate from
acknowledging its webhook. Work can become stale after the documented 30-minute activity window.
Another activity can recover a stale session. [Interaction][linear-interaction],
[best practices][linear-best].

**Docs.** `agentActivityCreate` takes `agentSessionId` and typed content. Agents can emit
`thought`, `action`, `elicitation`, `response` and `error`. `prompt` is user-generated, not an
agent-authored activity. Text-bearing types use `body`. An action has `action`, `parameter`
and optional `result`. Thought/action activities can be ephemeral and replaced by the next
activity. Activities are snapshots, unlike editable comments. [Interaction][linear-interaction].

**Docs.** The plan preview is `agentSession.plan`, replaced as a whole through
`agentSessionUpdate`. Each item has `content` and status `pending`, `inProgress`, `completed`
or `canceled`. It is a progress plan, not a separate plan-approval transaction.
[Interaction][linear-interaction].

### B3. Prompts, stop and elicitation signals

**Docs.** A new human message produces an `AgentSessionEvent` with action `prompted`.
The new text is in `agentActivity.body`. The integration incorporates it into conversation
history. The checked contract does not prescribe whether active work queues, preempts or
rolls back. [Interaction][linear-interaction] and [signals][linear-signals].

**Docs.** A user prompt with signal `stop` means halt immediately. Only a final response or
error confirming the stop should follow. The webhook does not itself stop external computation.
An agent elicitation with `auth` supplies a URL and optional user/provider metadata.
Authentication happens outside the thread. A thought can mark resumed work.
[Signals][linear-signals].

**Docs.** An elicitation with `select` supplies options with `value` and optional `label`.
The human can choose one or type free text. Either answer arrives as an ordinary prompt.
This is not a strictly typed approval response bound to a tool call (inference).
[Signals][linear-signals].

### B4. Delivery and limits

**Docs.** Session webhooks have `created` or `prompted` actions. Creation includes the session,
issue/comment context, previous comments, guidance and `promptContext`. Only the owning app
receives its session events. There is no verified SSE replay cursor in these docs.
[Interaction][linear-interaction].

**Docs.** Return HTTP 200 within 5 seconds. Generic webhook retries occur at 1 minute, 1 hour
and 6 hours, at most three retries. Persistently failing endpoints can be disabled.
`Linear-Delivery` identifies the delivery. Verify `Linear-Signature` as HMAC-SHA256 over the raw
body. The docs recommend rejecting an old `webhookTimestamp`, with a one-minute freshness
window. [Webhooks][linear-hooks].

## C. LangSmith Deployment and Agent Server

### C1. Assistants, threads, runs and separate states

Hosting recap: Agent Server persists work without a client (`always-on-agents.md §C3`).

**Docs.** LangSmith Deployment assistants are configured graphs. Runs update thread state.
Stateless runs preserve no thread memory. Checkpoints save state and crons schedule runs.
[Server reference][langsmith] and [OpenAPI][langsmith-spec].

**Code.** A Thread has `thread_id`, timestamps, metadata, status, values and interrupts.
A Run has `run_id`, `thread_id`, `assistant_id`, timestamps, status, metadata and kwargs.
Thread states are `idle`, `busy`, `interrupted` and `error`.
Run states are `pending`, `running`, `error`, `success`, `timeout` and `interrupted`.
`langchain-ai/langgraph@4c52e12 libs/sdk-py/langgraph_sdk/schema.py:23-40,302-317,364-379`.

**Docs.** `POST /threads/{thread_id}/runs` starts background work and returns a Run.
The request can include `input`, `command`, `config`, `context`, metadata, interrupt settings,
stream settings and a completion `webhook`. It can select an existing checkpoint.
The hosted API authenticates with `X-Api-Key`. [OpenAPI][langsmith-spec].

### C2. Concurrent input is an explicit policy

**Code.** `multitask_strategy` is one of `reject`, `interrupt`, `rollback` or `enqueue`.
`langchain-ai/langgraph@4c52e12 libs/sdk-py/langgraph_sdk/schema.py:81-87`.

**Docs.** `enqueue` is the background-run default. The new run waits for current work.
`reject` refuses a second run while the thread is busy. `interrupt` stops the first run and
continues with its progress plus new input. `rollback` discards the first run's graph progress,
including its initial input, before the replacement runs. These are Deployment features.
[Double texting][langsmith-multitask] and [OpenAPI][langsmith-spec].

**Docs** (inference). Checkpoint rollback does not promise to undo external side effects.
Compensation outside the graph is separate. [Double texting][langsmith-multitask].

**Docs.** Human input can pause execution at `interrupt()`. Resume by creating another run on
the same thread with `command: {"resume": answer}`. The saved checkpoint, not a free-floating
chat reply, supplies the continuation point. Command also supports state update and graph
routing. [OpenAPI][langsmith-spec] and [streaming][langsmith-stream].

### C3. Run streams and thread protocol v2 are different contracts

**Code.** Stream modes are `values`, `messages`, `updates`, `events`, `tasks`, `checkpoints`,
`debug`, `custom` and `messages-tuple`. They cover state, patches, messages and application data.
`langchain-ai/langgraph@4c52e12 libs/sdk-py/langgraph_sdk/schema.py:51-79`.

**Docs.** Create-and-stream uses `POST /threads/{thread_id}/runs/stream`.
Join an existing run at `GET /threads/{thread_id}/runs/{run_id}/stream`.
Create with `stream_resumable: true` to retain chunks for reconnect. Join accepts
`Last-Event-ID`. The value `-1` asks for all saved events.
`cancel_on_disconnect` defaults to false. [OpenAPI][langsmith-spec].

**Docs.** The newer protocol v2 SSE endpoint is
`POST /threads/{thread_id}/stream/events`. Its body filters channels and namespaces.
Frames have `type`, `event_id`, monotonic `seq`, `method` and `params`.
SSE `id` is the sequence number and `event` is the method. Reconnect sends the last `seq` as
body field `since`. Buffered events with a larger sequence replay before live events.
Browser `EventSource` auto-resume does not apply to this POST endpoint.
[OpenAPI][langsmith-spec].

**Docs.** The spec also describes thread-centric WebSocket streaming. Closing the v2 connection
unsubscribes its filters. The checked description does not specify buffer lifetime.
[OpenAPI][langsmith-spec].

### C4. Completion callbacks are not interrupt snapshots

**Docs.** A run's callback is an HTTP POST to its `webhook` URL. Payload includes run metadata,
final values, timestamps and optional error. Callback statuses include `success`, `error`,
`timeout`, `interrupted` and `rollback`. The ordinary Run status schema does not include
`rollback`. [Webhooks][langsmith-hooks] and [OpenAPI][langsmith-spec].

**Docs.** A graph paused at `interrupt()` can send a `success` callback. Pending interrupts
are not included in that payload. Read thread state before declaring the workflow finished.
Canceling a pending run sends no callback. Each actual run has its own callback.
Rollback callbacks have null values. [Webhooks][langsmith-hooks].

**Docs.** Callback auth can use configured static headers. Environment substitutions default
to the `LG_WEBHOOK_` prefix. Header support requires `langgraph-api` 0.5.36 or later.
The checked guide does not promise standardized signatures, a retry schedule or a receiver
deadline. Run callbacks are not a verified exactly-once channel. [Webhooks][langsmith-hooks].

### C5. Scheduling, retention and request identity

**Docs.** `POST /runs/crons` creates a fresh thread on each tick.
`POST /threads/{thread_id}/runs/crons` reuses one thread.
`DELETE /runs/crons/{cron_id}` removes the schedule. Schedules use UTC.
For fresh-thread crons, `on_run_completed` defaults to `delete`, with `keep` as the alternative.
That setting requires API 0.5.18 or later. [Cron jobs][langsmith-crons].

**Docs.** Thread creation accepts `thread_id` and `if_exists` of `raise` or `do_nothing`.
This is not a general replay-safe run key. Thread `ttl` sets minutes and strategy `delete` or
`keep_latest`. The latter prunes old checkpoints but keeps the thread and latest state.
The checked schema establishes no universal retention duration. [OpenAPI][langsmith-spec].

## D. Cursor Cloud Agents API

### D1. Current v1 separates agents from runs

Runtime recap: loop and worker differ (`cloud-agent-internals.md §A`). v1 changes the task API.

**Docs.** v1 is public beta. `POST /v1/agents` returns a durable `agent` and its initial `run`.
The Agent includes ID, name, environment, repository settings, URL, timestamps and
`latestRunId`. Agent states are `ACTIVE`, `IDLE` and `ARCHIVED`.
Recoverable run errors can leave the Agent idle. Read the Run for the error.
[v1 reference][cursor].

**Docs.** The checked OpenAPI Run enum is `CREATING`, `RUNNING`, `FINISHED`, `ERROR`,
`CANCELLED` and `EXPIRED`. The prose also treats `WAITING_FOR_BACKGROUND_WORK` as active.
That value is missing from the enum, so the sources disagree on its serialization.
The prose calls archived terminal for worker claims, yet the API supports unarchive.
[v1 reference][cursor] and [OpenAPI][cursor-spec].

**Docs.** Agent and run reads are separate. Agent listing defaults to 20 and caps at 100.
`nextCursor` is omitted at the end. `repos` allows 500 entries with target restrictions.
Prompts allow five images of 15 MB each. [v1 reference][cursor].

### D2. Follow-up, steering and cancellation

**Docs.** `POST /v1/agents/{id}/runs` ordinarily creates another run on the existing conversation
and workspace. Only one run is active. A second ordinary submission gets `409 agent_busy`.
To redirect current work, submit `delivery: "steer"` with only the prompt.
Other top-level settings are invalid for steering. [v1 reference][cursor].

**Docs.** Steering returns HTTP 202 with `delivery: "steered"` or `"queued"`.
It injects at a safe step or becomes the next run if current work ends first.
The response has no run ID or request ID. Retrying after a lost response can deliver twice.
A feature gate can return `403 feature_unavailable`. [v1 reference][cursor].

**Docs.** `POST /v1/agents/{id}/runs/{runId}/cancel` ends the active run as `CANCELLED`.
An already terminal run can return `409 run_not_cancellable`. A later run can reuse the Agent.
`POST /v1/agents/{id}/archive` and `/unarchive` are idempotent lifecycle operations.
`DELETE /v1/agents/{id}` is permanent. [v1 reference][cursor].

**Docs.** `mode: "plan"` changes the first run's behavior. The checked API does not document a
typed approve-plan or approve-tool endpoint. [v1 reference][cursor].

### D3. Per-run SSE and durable results

**Docs.** `GET /v1/agents/{id}/runs/{runId}/stream` emits `status`, `assistant`, `thinking`,
`tool_call`, `interaction_update`, `heartbeat`, `result`, `error` and `done`.
Assistant/thinking events carry text deltas. Tool events identify the call, tool, status and
optional arguments/result. `interaction_update` is a richer parallel representation.
Consuming both representations as independent activity duplicates UI output.
[v1 reference][cursor].

**Docs.** Most events have opaque SSE IDs. The sticky initial status has no ID and repeats on
reconnect. Send `Last-Event-ID` for this run only. A foreign or invalid cursor gets 400.
`X-Cursor-Stream-Retention-Seconds` announces retention. Expired history can return
`410 stream_expired`, after which the client retrieves the Run.
The checked docs do not publish one fixed retention duration. [v1 reference][cursor].

**Docs.** A terminal Run has `durationMs` and final `result` text.
`git.branches` reports repository, branch and optional PR URL. This is an Agent-wide git
snapshot, not a run-local diff. `autoCreatePR` controls PR creation.
`GET /v1/agents/{id}/artifacts` lists path, size and update time.
The download endpoint returns a presigned URL valid for 15 minutes.
[v1 reference][cursor].

### D4. Legacy surface, webhook boundary and identity

**Docs.** v0 has launch/list/get/delete, `/followup`, `/stop` and `/conversation`.
Conversation entries have user/assistant type and text. Stop pauses and follow-up restarts.
These are not v1 cancellation semantics. [Legacy reference][cursor-v0].

**Docs.** v1 webhooks are described as coming soon. v0 supports `statusChange` notifications
for `FINISHED` and `ERROR`. Verify `X-Webhook-Signature` as `sha256=` plus HMAC-SHA256 of the
raw body. `X-Webhook-ID` identifies a notification. A webhook secret is optional in the
configuration, so a receiver must not assume every integration enabled signing.
The docs allow retries but give no precise schedule or deadline. [Webhooks][cursor-hooks].

**Docs.** v1 accepts Basic or bearer authentication with a user API key or enterprise
service-account key. v0 examples use the key as Basic username and an empty password.
A client-selected `agentId` makes duplicate creation return `409 agent_id_conflict`.
It does not replay the original 201 response. It cannot be combined with `envVars`.
[v1 reference][cursor], [legacy reference][cursor-v0] and [API overview][cursor-auth].

## E. Devin API

### E1. Versions, organizations and principals

Runtime recap: compute and history have separate lifetimes (`cloud-agent-internals.md §E`).

**Docs.** v1 and v2 remain available during deprecation but receive no new features.
v3 names an organization in `/v3/organizations/{org_id}/...`.
Service-user API keys and human personal access tokens both use the `cog_` prefix and bearer
authentication. They represent different principals with different audit attribution and RBAC.
`create_as_user_id` requires the impersonation permission. [Overview][devin],
[authentication][devin-auth].

**Docs.** `POST /v3/organizations/{org_id}/sessions` requires a prompt.
Inputs include repositories, `playbook_id`, `child_playbook_id`, knowledge IDs, secret IDs,
session secrets, attachment URLs, tags, links, `max_acu_limit`, `resumable` and
`bypass_approval`. A playbook is reusable session instruction/context, not a separate run.
The response identifies `session_id` and the web URL. [Create][devin-create].

### E2. Session state and messages

**Docs.** Session `status` is `new`, `claimed`, `running`, `exit`, `error`, `suspended` or
`resuming`. While running, `status_detail` can be `working`, `waiting_for_user`,
`waiting_for_approval` or `finished`. A finished task therefore need not terminate its
resumable session. Suspension details distinguish inactivity, user request and several
budget/quota failures. A complete allowed-transition matrix is not specified.
[Retrieve][devin-get].

**Docs.** `POST /v3/organizations/{org_id}/sessions/{devin_id}/messages` accepts `message`,
optional attachment URLs and optional user attribution. It automatically resumes a suspended
session. The docs do not specify active-message queueing or preemption.
They expose waiting-for-approval state but no typed approval-response method was found in
the API index. [Send message][devin-send] and [overview][devin].

**Docs.** `DELETE /v3/organizations/{org_id}/sessions/{devin_id}` terminates the session.
It cannot then resume. History remains available for reference. Optional `archive=true`
also archives descendants and puts running descendants to sleep.
Termination is not data deletion (inference). [Terminate][devin-delete].

### E3. Structured results, attachments and polling

**Docs.** `structured_output_schema` is JSON Schema Draft 7, at most 64 KB with no external refs.
`structured_output_required` defaults to true and requires a final result before the turn ends.
Retrieve/list includes `structured_output`, PR URL/state and parent/child session IDs.
[Create][devin-create] and [retrieve][devin-get].

**Docs.** Upload an attachment with multipart field `file` to
`POST /v3/organizations/{org_id}/attachments`. The response gives ID, name and URL.
Pass URLs to create or message calls. Session attachment listing gives URLs for produced
files, logs and screenshots. These are distinct from the structured JSON result.
[Attachments][devin-upload] and [common flows][devin-flows].

**Docs.** Poll the session and its `/messages` collection. Messages have `event_id`,
`created_at`, text and `source` of `devin` or `user`, plus optional attribution.
Pagination uses `after`, `first`, `has_next_page` and `end_cursor`.
The default is 100 messages and the maximum is 200.
This is a history-page cursor, not a documented SSE replay position.
[Messages][devin-messages].

**Docs** (inference). Incoming automation webhooks are not outgoing session notifications.
The checked common flows document polling. [Common flows][devin-flows].

## F. Jules API

### F1. Sources, sessions and current state enum

Runtime recap: Google runs the worker (`cloud-agent-internals.md §F`). API version is `v1alpha`.

**Docs.** A Source names a connected repository, such as `sources/github/owner/repo`.
GitHub sources require the Jules app. A Session has name `sessions/{id}`, prompt, title,
source context, timestamps, state, URL, outputs and archived flag. Optional source context
selects repository and starting branch. Without it, work starts from scratch.
[Guide][jules] and [discovery][jules-spec].

**Docs.** Session states are `STATE_UNSPECIFIED`, `QUEUED`, `PLANNING`,
`AWAITING_PLAN_APPROVAL`, `AWAITING_USER_FEEDBACK`, `IN_PROGRESS`, `PAUSED`, `FAILED` and
`COMPLETED`. The discovery document marks `PLANNING` deprecated and no longer used.
Do not build a mandatory transition through it (inference). [Discovery][jules-spec].

### F2. Plan approval and follow-up

**Docs.** `POST /v1alpha/sessions` creates the session.
Set input-only `requirePlanApproval` to wait for a human plan decision.
Otherwise plans are automatically approved. `automationMode: "AUTO_CREATE_PR"` requests a PR.
Its unspecified default does not automatically create one. [Guide][jules],
[discovery][jules-spec].

**Docs.** `POST /v1alpha/sessions/{id}:approvePlan` approves the latest plan.
Its request body is empty, with no caller-supplied plan ID.
The activity log does identify the approved plan through `planApproved.planId`.
`POST /v1alpha/sessions/{id}:sendMessage` accepts `{"prompt":"..."}` and returns an empty
response. Follow the later activities for the agent's reaction.
The checked contract does not specify queueing versus interruption. [Discovery][jules-spec].

### F3. Activities, artifacts and collection

**Docs.** Activities have ID, resource name, creation time, originator, description and
artifacts. Variants include `planGenerated`, `planApproved`, `progressUpdated`,
`userMessaged`, `agentMessaged`, `sessionCompleted` and `sessionFailed`.
Plans have their own IDs and ordered steps. [Discovery][jules-spec].

**Docs.** Artifacts can be `changeSet`, `bashOutput` or `media`.
A git patch carries `baseCommitId`, `unidiffPatch` and optional suggested commit message.
Command output includes command, output and exit code. Media carries data and MIME type.
Session outputs can hold a change set or PR with URL, title and description.
The API therefore exposes a diff even when no PR is created. [Discovery][jules-spec].

**Docs.** Get/list sources, sessions and session activities with ordinary REST reads.
Sessions default to 30 per page. Activities default to 50. Both cap at 100.
Use `pageToken` and returned `nextPageToken`. Activities also support `create_time` filters.
These are polling and collection cursors, not a live-event resume protocol.
[Guide][jules] and [discovery][jules-spec].

### F4. Lifecycle, authentication and unverified guarantees

**Docs.** Discovery includes session delete, `:archive` and `:unarchive`.
Archive/unarchive return a Session. No explicit cancel operation is listed.
Deletion should not be described as graceful run cancellation without a documented guarantee
(inference). API keys use `X-Goog-Api-Key`. Jules allows up to three keys per account.
[Guide][jules] and [discovery][jules-spec].

## G. Claude Managed Agents API

### G1. Versioned configuration and session creation

Worker recap: Anthropic runs the loop, workers run tools (`cloud-agent-internals.md §C8`).

**Docs.** Use `anthropic-beta: managed-agents-2026-04-01`, `anthropic-version: 2023-06-01`
and `x-api-key`. Sessions reference versioned agent configuration and an environment.
`POST /v1/sessions` without input starts idle. `initial_events` accepts 50 events atomically
and in order, limited to `user.message` and `user.define_outcome`.
[Sessions][claude-sessions] and [reference][claude-reference].

### G2. State, ordered input and blocking calls

**Docs.** Session statuses are `idle`, `running`, `rescheduling` and `terminated`.
Transient failures can reschedule automatically. Idle covers end of turn, required action
and budget exhaustion. Termination is not ordinary task completion.
An idle session can still have an open workflow run. [Operations][claude-operations],
[events][claude-events].

**Docs.** Submit events with `POST /v1/sessions/{id}/events?beta=true`.
Events queue behind earlier events. `processed_at` is null until processing completes.
`user.define_outcome`, `user.custom_tool_result` and `user.tool_result` are processed on receipt.
To redirect work explicitly, enqueue `user.interrupt` followed by `user.message`.
The request returns once queued, not once the interrupt has taken effect.
[Events][claude-events].

**Docs.** An in-progress model response stops immediately when the interrupt applies.
Running tool calls can delay application. The interrupted turn has stop reason `end_turn`,
the same as natural completion. An interrupt does not end open workflow runs.
Its own `processed_at` and the workflow state matter more than assuming an idle event must
arrive. [Events][claude-events].

**Docs.** `session.status_idle.stop_reason.type` is `end_turn`, `requires_action` or
`budget_reached`. Required-action event IDs identify pending calls.
Confirm a tool through `user.tool_confirmation` with `tool_use_id` and `result` of `allow`
or `deny`. Permission policies include `always_allow`, `always_ask` and `auto`.
Built-in tools and MCP toolsets have different defaults. [Events][claude-events],
[permissions][claude-permissions].

**Docs.** A custom tool response is `user.custom_tool_result`, correlated by
`custom_tool_use_id`. A second result for that call is accepted without effect.
Answer as soon as the call appears. With open workflow runs, a blocking call can coexist
with a running session. Do not wait for session-wide idle.
Budget pauses resume through a sufficient budget update, not an ordinary message.
[Events][claude-events].

### G3. Live streams, persisted history and outputs

**Docs.** `GET /v1/sessions/{id}/events/stream?beta=true` is live-only.
Open the stream before sending work. After reconnect, open the stream first, list all persisted
history and deduplicate live/history overlap by event ID. The documented algorithm does not
use `Last-Event-ID` to replay missed events. [Events][claude-events].

**Docs.** Events include `agent.message`, `agent.tool_use`, `agent.tool_result`,
`agent.custom_tool_use`, MCP calls/results, status, usage and errors. `agent.thinking` signals
progress without thinking content. Messages arrive after model requests complete.
Optional `event_start`/`event_delta` previews never persist. [Reference][claude-reference].

**Docs.** Deliverables written to `/mnt/session/outputs/` become Files API objects.
List with `scope_id` equal to the session ID and the Managed Agents beta header.
Download by file ID. Uploads can finish a few seconds after idle, so a first empty list is not
proof there are no deliverables. Outcomes and their evaluations are separate from file contents.
[Outputs][claude-outputs].

### G4. Notification delivery is deliberately bounded

**Docs.** Webhook names differ from stream names. Examples are `session.status_run_started`,
`session.status_idled`, `session.budget_reached`, `session.status_rescheduled`,
`session.status_terminated`, `session.updated` and `session.deleted`.
The small payload names the resource and workspace. Retrieve current state after notification.
A deleted resource cannot be retrieved. [Webhooks][claude-hooks].

**Docs.** Endpoints are workspace-scoped and require public HTTPS on port 443.
Verify `webhook-id`, `webhook-timestamp` and `webhook-signature` using the signing secret.
SDK verification rejects timestamps older than five minutes.
The event ID remains the same across attempts. Order is not guaranteed.
[Webhooks][claude-hooks].

**Docs.** There are at most three delivery attempts total, with jittered backoff from 5 to
120 seconds. Afterward the event is dropped without a durable queue or loss notification.
Any 2xx acknowledges it. A 3xx disables the endpoint immediately. Disabled-period events and
events predating subscription are not backfilled.
The checked page gives no numeric receiver deadline. [Webhooks][claude-hooks].

### G5. Retention and limits

**Docs.** Conversation history persists until explicit session deletion.
Cloud sandbox state lasts 30 days from sandbox creation, not 30 days from last activity.
After that a resumed session has fresh compute state. Exported deliverables therefore have a
different lifetime from workspace state. Create requests allow at most 32 MB.
Organization limits are 300 create requests and 1,200 read requests per minute.
[Events][claude-events], [sessions][claude-sessions] and [reference][claude-reference].

## H. OpenAI

### H1. Background Responses are asynchronous response objects

Hosting recap: background execution survives the client socket (`always-on-agents.md §C5`).

**Docs.** Create at `POST /v1/responses` with `background: true`.
Poll `GET /v1/responses/{id}` while status is `queued` or `in_progress`.
`POST /v1/responses/{id}/cancel` cancels background work.
Repeated cancellation is idempotent and returns the final object.
[Background mode][openai-background].

**Code.** The full status enum is `completed`, `failed`, `in_progress`, `cancelled`, `queued`
and `incomplete`. `openai/openai-python@718aee9`
`src/openai/types/responses/response_status.py:1-7`.

**Docs.** Create with both `background: true` and `stream: true` for resumable SSE.
Save each received event's `sequence_number`. Reconnect by retrieving with `stream=true`
and `starting_after` set to that sequence. The guide explicitly excludes keepalive counters
from saved resume positions. Events include response lifecycle, output-item changes and text
deltas. A transport disconnect is not a cancellation request.
[Background mode][openai-background].

**Docs.** Follow-up creates another Response, optionally with conversation state.
No in-flight message-mutation endpoint was verified. Output is a typed list of messages,
tool calls and tool-specific results. [Conversation state][openai-state].

**Docs.** Current background docs allow ZDR requests with `store=false` and roughly ten minutes
of temporary disk storage for asynchronous polling. For Modified Abuse Monitoring, background
output survives that polling window only with explicit `store=true`.
This differs from older blanket claims that background mode cannot be used with ZDR.
[Background mode][openai-background].

### H2. Conversations store items but do not execute work

**Docs.** A durable Conversation stores messages, tool calls, tool outputs and other items.
Pass its ID as a Response's `conversation` to extend that state automatically.
Alternatively, `previous_response_id` chains Responses.
Conversation/item state is retained until deleted and is not ZDR-eligible.
[Conversation state][openai-state] and [data controls][openai-data].

**Docs.** Conversations have create/read/update/delete operations. Items have create/read/delete
and listing. Creation accepts up to 20 initial items. Deleting a conversation does not delete
its items. Adding an item does not establish steering or execution (inference).
[Conversations reference][openai-conversations].

### H3. Agents sessions add an explicit turn lifecycle

Worker recap: OpenAI runs the harness, executors run tools (`cloud-agent-internals.md §B6`).
The Agents API differs from Responses and the open-source Agents SDK.

**Docs.** The beta uses `/v1/agents/sessions` and `OpenAI-Beta: agents=v1`.
A session contains agent configuration, optional environment, turns and saved items.
Input to an idle session starts a turn. Input while work is active steers the current turn.
Send `agent.session.input.message` to
`POST /v1/agents/sessions/{id}/events`.
Cancel current work with `agent.session.input.cancel`, preserving the session.
[Sessions][openai-agents].

**Code.** Session states are `idle`, `in_progress`, `requires_action` and `failed`.
The session contains agent, environment, error and `required_actions`.
`openai/openai-python@718aee9 src/openai/types/beta/agent_session.py:220-258`.

**Docs.** Root turn outcomes are `agent.session.turn.completed`,
`agent.session.turn.failed` and `agent.session.turn.cancelled`.
Check that `turn.subagent_id` is null. A subagent's terminal event is not the root outcome.
`agent.session.idle` alone is not proof of success.
[Session events][openai-events].

**Docs.** `GET /v1/agents/sessions/{id}/events?stream=true` is live-only.
It does not replay missed events after reconnect. Retrieve the session, its saved items and
turns to reconcile. Items preserve messages and tool activity, not every transient event.
The items endpoint accepts ordering, limit and pagination cursors.
[Session events][openai-events] and [management][openai-manage].

**Docs.** Configure structured output with `agent.text.format.schema`.
Hosted files under `/workspace/outputs` become immutable artifacts when the turn finishes.
The session Artifacts API lists, retrieves, downloads and deletes those published copies.
They survive environment expiry. Self-hosted files use the provider's filesystem/API instead.
[Sessions][openai-agents] and [files][openai-files].

### H4. Required actions and submission identity

**Docs.** After `agent.session.requires_action`, retrieve `required_actions`.
A function call has `name`, `arguments`, `turn_id` and `call_id`.
Return `agent.session.input.tool_result` with those IDs plus `success` and `output` or `error`.
History containing a tool call does not prove its result is still pending.
Save side-effect results durably under session/turn/call identity before retrying delivery.
[Functions][openai-functions].

**Code.** Required actions also include `environment_connection` and
`computer_use_approval_request`, which identifies `request_id` and `turn_id`.
`openai/openai-python@718aee9 src/openai/types/beta/agent_session.py:138-203`.

**Docs.** Answer computer approval with
`agent.session.input.computer_use_approval_request_result` and `request_id`.
For origin access, nested `response` has type `browser_origin_access` and decision `approve`,
`deny` or `cancel`. Origin permission does not guarantee confirmation before each consequential
browser action. [Computer use][openai-computer].

**Docs.** Session input submission supports `Idempotency-Key`.
Reuse the same key, session and payload for retries of one logical submission.
Use a fresh key for a distinct submission, even if the text is identical.
The session input/schema runtime budget is 4 MiB. Tool results must leave room for metadata
within that size. [Sessions][openai-agents] and [functions][openai-functions].

### H5. Webhooks, retention and reconciliation

**Docs.** API authentication accepts bearer API keys or workload-federation access tokens.
The Agents beta header is additional to authentication. [API overview][openai-auth].

**Docs.** Agents webhooks include `agent.session.created`, `agent.session.action_required`,
`agent.session.in_progress`, `agent.session.idle` and `agent.session.failed`.
The webhook says `action_required`, while the stream says `requires_action`.
Retrieve detailed pending actions. Environment connection waits have a five-minute deadline,
so webhook retry duration is not the worker-connect deadline.
[Session webhooks][openai-agent-hooks].

**Docs.** Shared delivery uses Standard Webhooks signatures and headers `webhook-id`,
`webhook-timestamp` and `webhook-signature`. Return 2xx within a few seconds.
Failures retry with exponential backoff for up to 72 hours. Redirects are not followed.
Duplicate events can occur. Deduplicate with `webhook-id`.
Background Responses can notify through `response.completed`.
[Webhooks][openai-hooks].

**Docs.** The data policy lists `/v1/agents` application state as retained until deleted.
Deleting a session removes it from the API while physical cleanup can continue asynchronously.
This is separate from hosted compute expiry and transient stream recovery.
No missed-event replay should be inferred from durable saved items (inference).
[Data controls][openai-data], [management][openai-manage] and [events][openai-events].

## I. GitHub Copilot agent tasks

### I1. A public REST preview now exists

Runtime recap: follow-up can start a fresh runner (`cloud-agent-internals.md §D1`).

**Docs.** The public preview lists these operations:
`GET` and `POST /agents/repos/{owner}/{repo}/tasks`,
`GET /agents/repos/{owner}/{repo}/tasks/{task_id}`,
`GET /agents/tasks` and `GET /agents/tasks/{task_id}`.
Task creation requires Business or Enterprise. The reference recommends API version
`2026-03-10`, while the how-to still has `2022-11-28` examples.
[REST reference][github-rest] and [API how-to][github-assignment].

**Docs.** Creation requires `prompt` and can set model, custom agent, base/head refs and
`create_pull_request`, which defaults to false. A supplied existing PR head can be updated.
`custom_agent` selects a repository agent-profile name, not a third-party vendor.
The task includes creator, owner, repository, collaborators, timestamps, session count,
archived time and artifacts. [REST reference][github-rest].

**Docs.** Task states are `queued`, `in_progress`, `completed`, `failed`, `idle`,
`waiting_for_user`, `timed_out` and `cancelled`. Detailed task retrieval includes sessions
with their own state, refs, prompt, model, timestamps, usage and optional error.
Artifacts identify GitHub `pull` or `branch` resources.
Listing supports state/time filters and page/per-page pagination capped at 100.
[REST reference][github-rest].

### I2. Identity and issue assignment

**Docs.** The task preview uses user-to-server credentials, not GitHub App installation tokens.
Supported fine-grained credentials include PATs and GitHub App user tokens with Agent tasks
permission. The how-to also documents classic PAT and OAuth-app access.
A GitHub App acting as a user is not an installation acting independently (inference).
[REST reference][github-rest] and [API how-to][github-assignment].

**Docs.** Issue REST APIs can assign `copilot-swe-agent[bot]` and attach `agent_assignment`
with `target_repo`, `base_branch`, `custom_instructions`, `custom_agent` and `model`.
GraphQL provides corresponding `agentAssignment` fields in issue-assignment mutations.
The documented GraphQL feature flags are `issues_copilot_assignment_api_support` and
`coding_agent_model_selection` in `GraphQL-Features`.
[API how-to][github-assignment].

### I3. CLI following is implemented as polling

**Docs.** `gh agent-task` has create, list and view subcommands.
Create accepts a prompt, base branch, custom agent, file input and `--follow`.
View selects a session ID or PR identifier and supports `--follow`, `--log` and JSON.
The CLI calls these task sessions and marks the commands preview.
[CLI manual][github-cli].

**Code.** The CLI discovers a Copilot backend URL through GraphQL
`viewer.copilotEndpoints.api`. Its create job uses `/agents/swe/v1/jobs`.
Session and log retrieval use `/agents/sessions/{id}` and its `/logs` suffix.
These implementation endpoints are not the documented public REST task paths.
`cli/cli@4f2cfe9 pkg/cmd/agent-task/shared/capi.go:48-68`,
`pkg/cmd/agent-task/capi/job.go:55-89`,
`pkg/cmd/agent-task/capi/sessions.go:302-307,338-349`.

**Code.** `--follow` polls logs every five seconds. The renderer retains the prior full string,
renders its new suffix and recognizes `data:` log lines. This is not a public resumable SSE
contract with a server-side cursor.
`cli/cli@4f2cfe9 pkg/cmd/agent-task/view/view.go:27-29,370-392`,
`pkg/cmd/agent-task/shared/log.go:29-65`.

### I4. Follow-up and third-party boundaries

**Docs.** Claude and Codex are third-party previews with UI and issue/PR entry points.
That does not establish a public registration API for arbitrary vendors.
[Third-party agents][github-partners].

## J. Slack agent UX primitives

### J1. Legacy assistant threads and newer agent sessions

Transport recap: acknowledge Events API callbacks in three seconds (`always-on-agents.md §E`).

**Docs.** Legacy `assistant_thread_started` gives the user, channel, thread timestamp and
viewing context. `assistant_thread_context_changed` updates that context.
In the newer Agent messaging experience, use `app_home_opened` with `tab="messages"` for a
user opening the DM. `app_context_changed` is the newer context event.
The assistant events are not interchangeable with ordinary messages.
[Thread event][slack-thread] and [migration][slack-migration].

**Docs.** New agent sessions use the channel/thread pair and states `processing`, `active`,
`suspended` and `closed`. `agents.sessions.setStatus` and `agents.sessions.rename` manage
visible state and title. Processing expires to active after one hour. Refreshing processing
restarts that timer. Posting a message does not itself clear current processing state.
[Agent sessions][slack-sessions].

**Docs.** Subscribe to `agent_session_stopped` to expose the stop action.
The callback names channel, thread, user and any streaming-message timestamps.
The application must stop computation, clean up and update status. Slack does not do those
things automatically. A UI stop is a request, not proof the backend stopped (inference).
[Agent sessions][slack-sessions].

### J2. Status and suggested prompts

**Docs.** `assistant.threads.setStatus` still supports a textual loading indicator and
`loading_messages` with up to ten strings. Empty status clears it.
Slack recommends migrating to agent session status. The legacy reference describes a
two-minute loading timeout, whereas the new processing session has the one-hour timeout.
Do not collapse these timers into one lifecycle.
[Status method][slack-status] and [migration][slack-migration].

**Docs.** `assistant.threads.setSuggestedPrompts` accepts up to four `{title,message}` prompts.
In legacy assistants they are thread-scoped. In agent apps they appear atop the Messages tab.
Do not pass `thread_ts` for an agent app. The method says this silently fails.
Selecting a suggestion generates ordinary user message input, not a typed approval.
[Prompts method][slack-prompts] and [agent development][slack].

### J3. Streaming messages are producer writes

**Docs.** `chat.startStream` creates a streaming message and returns channel and timestamp.
`chat.appendStream` appends content. `chat.stopStream` finalizes it.
These are authenticated HTTP Web API calls from the application to Slack, not a client SSE
subscription. The bot needs `chat:write`. Ordinary thread replies use `thread_ts`.
New channel-level cases have additional recipient requirements.
[Start][slack-start], [append][slack-append] and [stop][slack-stop].

**Docs.** Supply `markdown_text` or typed `chunks`.
Chunks include `markdown_text`, `task_update`, `plan_update` and `blocks`.
A task update has a stable ID, title, status, details, output and sources.
Repeated IDs update the same card. `task_display_mode` chooses timeline or plan rendering.
These are progress representations, not server-managed coding tasks.
[Start][slack-start] and [append][slack-append].

**Docs.** A stream lasts at most ten minutes total. Appends do not extend it.
The message allows 12,000 cumulative markdown characters, 50 unique tasks and 50 sources
per task. Oversized task details/output can be silently omitted.
Automatic finalization marks unfinished tasks as errors. Later appends/stops get
`message_not_in_streaming_state`. [Start][slack-start] and [append][slack-append].

**Docs.** Stop can set `session_status`, defaulting to `active`.
Use `processing` between successive messages when backend work continues.
Stop can also add final blocks. Its separate 50-block allowance is additional to chunk blocks.
Start and stop have Tier 2 limits, while append has Tier 4.
This is a bounded rendering window, not a maximum backend task duration (inference).
[Stop][slack-stop], [start][slack-start] and [append][slack-append].

### J4. Input, approval and durability belong to the app

**Docs** (inference). Messages, blocks and stop events supply input, not an execution policy.
Event deduplication does not establish idempotent stream appends. These methods establish no
consumer replay cursor or task-retention contract. §§J1 to J3 and `always-on-agents.md §E`.

## K. AG-UI

### K1. A UI event protocol, not a task-hosting service

**Docs.** AG-UI connects agent backends to user interfaces through an event protocol over
web transports. It does not itself supply a hosted task database, OAuth app model or webhook
service. The official source repository is `ag-ui-protocol/ag-ui`.
[Overview][agui].

**Code.** At the pinned commit, `@ag-ui/core` is package 1.0.3 and declares protocol 1.0.
Events include `RUN_STARTED`, `RUN_FINISHED`, `RUN_ERROR`, step events, text-message events,
tool-call events, `STATE_SNAPSHOT`, `STATE_DELTA`, `MESSAGES_SNAPSHOT`, activity events,
custom events and raw events.
`ag-ui-protocol/ag-ui@73b04fd sdks/typescript/packages/core/package.json:1-7`,
`sdks/typescript/packages/core/src/generated/types.ts:9-45`.

**Docs.** Run identity is `threadId` plus `runId`. Text and tool events use their own message
and call IDs. State deltas use JSON Patch, while snapshots reset the client's known state.
`RUN_FINISHED.result` carries application results. There is no universal durable-file or PR
artifact resource. [Events][agui-events].

### K2. Interrupts have a precise resume contract

**Docs.** `RUN_FINISHED.outcome` can be omitted for legacy normal completion, be
`{"type":"success"}` or be an interrupt outcome with a nonempty `interrupts` array.
Errors use `RUN_ERROR`, not an error outcome. Each interrupt has ID and reason, with optional
message, tool-call ID, response schema and expiry.
Core reasons are `tool_call`, `input_required` and `confirmation`.
[Interrupts][agui-interrupts].

**Docs.** Start a new run on the same thread with `RunAgentInput.resume`.
Each entry names `interruptId` and status `resolved` or `cancelled`, plus optional payload.
A denial is a resolved payload such as `{"approved":false}`, not a third status.
The request must answer all open interrupts. Ordinary input is blocked until then.
Replay of the same thread/interrupt/status/payload must be safe.
[Interrupts][agui-interrupts].

**Docs.** Emit required state and message snapshots before the interrupted run ends.
A tool result in the resumed run uses the original tool-call ID without replaying the original
call sequence. Invalid, stale or incomplete resumes produce `RUN_ERROR`.
These are resume semantics, not a universal transport replay cursor.
The checked protocol does not mandate retention, webhook retry or ordinary concurrent-input
scheduling. [Interrupts][agui-interrupts] and [events][agui-events].

## What an agent task API must offer

Each generic deduction is **Docs** (inference). Not every provider offers every item.

1. **Separate durable identity from execution.** Name conversation, task, run/turn and
   environment independently. Make relationships queryable. §§A1, C1, D1, H2 to H3 and I1.
2. **Publish state meaning and transition boundaries.** Distinguish queued, working, blocked,
   finished, failed, canceled and expired work. Keep session readiness separate from run
   outcome. Document which objects can resume. §§A2, E2, G2 and H3.
3. **Specify concurrent-input behavior.** State whether a message queues, steers, interrupts,
   rejects or starts another run. Expose accepted versus processed input and identify the run
   receiving it. §§B3, C2, D2 and G2.
4. **Make human requests durable and correlated.** Return the pending request, target call
   or plan, expected answer and expiry. Define stale/replayed decision handling.
   A waiting flag is insufficient. §§F2, G2, H4 and K2.
5. **Separate interruption, cancellation, archival and deletion.** Say whether compute stops,
   the conversation survives, descendants stop and resumption remains possible. Do not imply
   external side effects were undone. §§A2, C2, D2, E2, F4 and H3.
6. **Offer a watch contract with a repair path.** Define event ordering, scope, identity,
   cursor validity and retention. Support snapshot/history reconciliation when replay is
   absent or expired. §§A3, C3, D3, G3 and H1 to H3.
7. **Distinguish notification delivery from state storage.** Publish webhook signatures,
   duplicate behavior, ack deadline, retry count/window, subscription scope and loss behavior.
   Let a receiver reconcile current state independently. §§A4, B4, C4, G4 and H5.
8. **Define idempotency for writes, not just delivery.** Specify key scope, replay response
   and conflict behavior for create, message, decision and cancel operations. A delivery UUID
   and a conflict-on-duplicate resource ID solve different problems. §§A4, B4, D2, D4, H4 and K2.
9. **Model results beyond the last message.** Return structured output, git refs, patches,
   PRs and files with ownership and download expiry. Distinguish run-local results from
   session snapshots. §§A3, D3, E3, F3, G3 and H3.
10. **State which principal acts.** Distinguish app installation, app user, service identity,
    delegated user and personal token. Record attribution and permissions. §§B1, E1 and I2.
11. **Expose independent clocks and budgets.** Separate input deadlines, stream retention,
    workspace expiry, history retention and cost limits. A Slack stream ending does not end
    a task. A webhook retry window does not extend an approval deadline. §§D3, G5, H5 and J3.
12. **Version the contract and surface migrations.** Publish exact enums, feature gates and
    preview limitations. Keep old/new webhook and lifecycle semantics distinct.
    §§A1, D1, E1, I1 and J1.

## Gaps

- **No live behavior was tested.** Public docs and pinned source are not measured guarantees.
  No vendor account, token or authenticated vendor API request was used.
- **Transition matrices are incomplete.** No edges were inferred beyond documented transitions.
- **Cursor has a state-schema mismatch.** v1 prose names `WAITING_FOR_BACKGROUND_WORK`, while
  the checked OpenAPI enum omits it. The docs also call archived terminal while exposing
  unarchive. Exact runtime behavior needs vendor clarification. §D1.
- **Concurrent-message policy is not always specified.** Linear, Devin and Jules expose input
  but do not clearly promise queueing, preemption or rollback. A2A leaves policy to the server.
  §§A2, B3, E2 and F2.
- **Some approval contracts remain unverified.** No typed Devin approval-response API or
  generic Cursor confirmation endpoint was found. §§D2 and E2.
- **Webhook contracts have holes.** No outgoing Devin or Jules session webhook was found.
  Cursor v1 says webhooks are coming soon. Precise Cursor retry timing, LangSmith retry/signing
  guarantees and Claude's numeric receiver timeout were not verified. §§C4, D4, E3, F4 and G4.
- **Replay horizons are not uniformly published.** Cursor exposes retention through a response
  header. LangSmith protocol v2 describes a buffer without a checked fixed lifetime.
  Claude and OpenAI Agents need reconciliation rather than missed-event replay. §§C3, D3,
  G3 and H3.
- **General write idempotency is often unspecified.** No create/message key was verified for
  Linear, LangSmith runs, Devin, Jules, Claude or GitHub tasks. A duplicate custom-tool-result
  guarantee does not settle ordinary messages. §§B4, C5, E3, F4, G2 and I1.
- **Retention and limits remain account-specific or unclear.** Fixed API-history retention
  was not verified for Linear, Cursor, Devin, Jules or GitHub tasks. Runtime snapshot windows
  in the companion note are not substitutes for API-history guarantees.
- **GitHub partner APIs are not established.** Arbitrary registration, vendor selection,
  task steering, approval, cancellation and task-specific webhooks were not verified.
  CLI backend code is not an endorsed public integration surface. §§I1 to I4.
- **Jules OAuth support is unresolved.** Discovery scopes exist, but the checked guide only
  establishes API-key access. Discovery's delete operation also does not establish graceful
  cancellation semantics. §F4.
- **Protocol and channel gaps are intentional.** A2A does not choose application approval
  semantics. AG-UI does not choose hosting retention or universal transport replay. Slack
  does not stop backend work or define the agent's concurrency policy. §§A2, K2 and J4.
