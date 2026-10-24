# How to drive coding agents headlessly, for Hibernaut

**Date checked:** 2026-10-24.

**Summary**

This note researches headless coding-agent interfaces for Hibernaut. It extends
`cloud-agents.md §C`, beside the hosted workers in `cloud-agent-internals.md` and the triggers
in `always-on-agents.md`. It separates live control, conversation recovery and broken model
streams. Findings come from primary docs and pinned source, without running authenticated agents.

- **JSON output is not a control protocol.** One-shot commands, persistent stdin protocols and
  detached servers have different lifetimes. A token stream alone provides neither approvals
  nor attachment. ACP and the Codex, Claude and Copilot interfaces illustrate the distinction
  (§A1, §B1 to §B3, §C1 and §D1).
- **ACP has two materially different versions.** Stable v1 completes a prompt through its
  response. Draft v2 acknowledges acceptance and reports completion through state updates.
  Standard remote transport is still draft work, despite working vendor transports (§A1, §A4).
- **Queueing, steering and interruption are different operations.** Codex has explicit steering,
  Copilot defaults to enqueueing, Amp offers a steering bit and Gemini's ACP handler aborts the
  preceding prompt. A portable client cannot assume one mid-turn behavior (§B2, §D2, §F1, §G2).
- **Resume does not mean live attachment or filesystem recovery.** Disk transcripts restore
  conversation context. Replay, pending permissions, external tool handlers and concurrent
  writers each need a separate contract (§A3, §C3, §D3, §E2 and §J2).
- **Pause recovery depends on model retries, not the control framing.** Codex now enables
  unbounded retries for a specific connection-failure class. Claude documents sleep detection
  and stream watchdogs. Other failure classes remain bounded or undocumented (§B5, §C4, §G3).
- **A local server can outlive its client, but a spawned child often cannot.** Codex's
  multi-client server preserves active unsubscribed work, while stdio closure shuts its server.
  SDK cleanup can terminate children even when transcripts are durable (§B4, §C2 and §D4).
- **Old interface names are unsafe integration targets.** Codex removed `mcp-server`.
  AgentAPI is archived, Goose is consolidating on ACP and OpenCode's development API differs
  from its released docs. Pin the executable and its schema together (§E1, §I1, §L1 and §M1).

## Sources

Inline citations drop `https://`. Code citations read `owner/repo@sha7 path:lines`.
Full HEAD SHAs came from the GitHub commits API. Development HEAD is not an installed release.

- **ACP.** agentclientprotocol.com/protocol/v1, /protocol/v2/migration and /get-started.
  Code: agentclientprotocol/agent-client-protocol
  `a0c3d83e5277a5b419dcd083742c0a14dc298d21`.
  Claude adapter: agentclientprotocol/claude-agent-acp
  `ebfbfbdaa9c2a6a8bd9eb0a4965b521009152350`.
  Codex adapter: agentclientprotocol/codex-acp
  `a77829fecc87cebbb2e53782ce6795aee072adc8`.
  Archived predecessor: zed-industries/codex-acp
  `296069e841634cd4bb9bc4515602d836e49231ec`.
- **Codex.** learn.chatgpt.com/docs/app-server, /non-interactive-mode and /auth.
  Code: openai/codex `970111f6cb62cc02fe36a82ea7d71b37a2889c97`.
  MCP removal commit: `531f3836a1e38ea61eaaba3dccda6711eb6c0dca`.
- **Claude Code and Agent SDK.** code.claude.com/docs/en/headless, /agent-sdk, /authentication,
  /errors, /network-config, /env-vars and /mcp.
  Code: anthropics/claude-agent-sdk-python
  `0e89b298e771259378a6cd3734e00d323dff5703`.
  Published packages: `@anthropic-ai/claude-agent-sdk@0.3.310` and
  `@anthropic-ai/claude-code@2.1.310`.
  SDK types were read from the package. Closed CLI behavior is docs evidence only.
- **Copilot.** docs.github.com/en/copilot/reference/copilot-cli-reference and
  /en/copilot/how-tos/copilot-cli. Code: github/copilot-sdk
  `be9764fe7c10e48e17b37bf6a3198b1f26f7e9e8`.
  Public CLI repository: github/copilot-cli
  `7ea98dfc33c68aaf6443ea8fc054e20ff7fcd120`.
  Published package metadata: `@github/copilot@1.0.103`. The closed runtime was not inspected.
- **OpenCode.** opencode.ai/docs/server, /cli and /sdk. Current repository:
  anomalyco/opencode `f20760b8bf3bced0fa5620b6afe7b78dd615ee36`.
  `sst/opencode` redirects there. The pinned branch is `dev`. Live SDK docs link release
  `v1.19.4`, which is not treated as equivalent to that development commit.
- **Amp.** ampcode.com/docs/cli, /cli/execute-mode, /cli/streaming-json,
  /cli/remote-control, /threads and /sdk.
  Published SDK: `@ampcode/sdk@0.1.0-20260918210405-g81edbf0`.
  Closed CLI retry internals were not inspected.
- **Gemini.** geminicli.com/docs and first-party repository docs.
  Code: google-gemini/gemini-cli
  `cd1b45c2db0a5ea7b7fc3b67c1ba5879601d6005`.
- **Cursor.** cursor.com/docs/cli/headless, /cli/acp and /cli/reference/parameters.
  Closed CLI behavior is docs evidence only.
- **Goose.** goose-docs.ai/docs/gdk/acp, /guides/remote-goose-server and
  /guides/environment-variables. Current code: aaif-goose/goose
  `c3bfc5f7b0e230180ba3164ec1ee84fd951eaf0d`. `block/goose` redirects there.
- **Factory.** docs.factory.com/droid-exec/overview and /sdk/typescript.
  SDK package names are documented. Their implementations were not inspected.
- **Kiro.** kiro.dev/docs/cli/headless, /cli/acp, /cli/v3/acp-migration,
  /reference/cli-commands and /getting-started/authentication.
- **Coder AgentAPI.** coder/agentapi
  `7c468d5b25ec9d3d76c41313cf4e08904ff5968d` and coder.com/docs/ai-coder/agents.
  The repository is archived. Its README explicitly says it is no longer maintained.
- **MCP.** code.claude.com/docs/en/mcp and the Codex removal commit above.
  MCP is discussed only as an agent control surface, not as a general tool protocol survey.

**Tags.** **Docs**: read on the vendor's docs, spec, changelog or blog on the date above.
**Code**: read in source at the named commit or a published package at the named version.
**Unverified**: only the named secondary source says so. News and forums served only as
leads. Quotes are verbatim. "(paraphrase)" marks spots where a quote would break this
note's prose rules. "(inference)" marks my own deductions. An untagged claim takes the
tag of its sentence's citation.

## Comparison table

Cells summarize the cited sections. Unknown means not established, not unsupported.
Do not mix release versions. No row promises successful recovery from a paused VM.

| Agent or protocol | Interfaces | Transport | Follow-up mid-turn | Interrupt | Approvals | Attach and replay | Session storage | Resume from a fresh process | Disconnect and EOF | Model API retry |
|---|---|---|---|---|---|---|---|---|---|---|
| ACP v1 and draft v2 (§A) | Session RPC and client callbacks | v1 stdio NDJSON JSON-RPC 2.0, remote standard draft | No universal v1 queue contract | `session/cancel` | Reverse permission RPC | v1 load replays, resume does not, live attach agent-specific | Agent-specific | Capability-gated load or resume | Agent-specific | Outside spec |
| Codex (§B) | app-server, exec and TS SDK | Headerless JSON-RPC over NDJSON or WS, exec JSONL | `turn/steer` with expected turn ID | `turn/interrupt` | Command, file and permission requests | Thread subscription plus history APIs, no universal missed-delta cursor | Rollouts under `~/.codex/sessions`, evolving thread store | Thread resume or exec resume | Stdio closure stops server, multi-client active turns differ | Four HTTP retries, five stream retries, special unbounded network retry |
| Claude Code and SDK (§C) | Print, streaming input and SDK | NDJSON stdio plus SDK control messages | Streaming SDK queues input | SDK interrupt, CLI signals differ | `canUseTool`, hooks and modes | No general SDK live attach, transcript resume | `~/.claude/projects/<encoded-cwd>/*.jsonl` | Resume, continue and fork | EOF drains work, SIGTERM leaves unfinished turn | Ten default retries, conditional watchdog and partial-output recovery |
| Copilot (§D) | Prompt, ACP and SDK server | Prompt JSONL, SDK JSON-RPC streams, ACP NDJSON | Enqueue default, immediate option | `session.abort` | Permission events and response RPC | History API, experimental cursor reads, concurrent writers undefined | `~/.copilot/session-state/<id>/events.jsonl` plus artifacts | CLI or SDK resume | Detach differs from abort and owned-child stop | Closed runtime details unknown |
| OpenCode (§E) | HTTP server, SDK, run, attach and ACP | HTTP plus SSE, ACP stdio | Dev durable inbox, steer default | Dev interrupt, released abort | HTTP permission reply or ACP | Live SSE has no replay ID, history is separate | SQLite in XDG data directory | Session ID against persistent server state | SSE disconnect loses delivery, turn lifecycle not fully established | Dev ten retries, timeout subset three |
| Amp (§F) | Execute, streaming input and SDK | NDJSON local process plus service-owned threads | Queue or `steer: true` | SDK AbortSignal, exact wire unknown | Plugin policy, no verified generic approval RPC | Web Cross-Client Access, raw replay cursor unknown | Server-side threads, local cache details unknown | Continue thread ID | EOF waits for completion, background subagents differ | Undocumented closed implementation |
| Gemini (§G) | Headless and native ACP | Headless JSONL, ACP NDJSON | ACP aborts preceding prompt | ACP cancel and signal abort | ACP permission RPC, headless policy | ACP load replays, no general second-writer guarantee | Project chats directory, current JSONL | `--resume` and ACP load | ACP EOF cleans up, SIGTERM aborts headless | Ten connection attempts, four midstream attempts |
| Cursor (§H) | Print and native ACP | JSONL or ACP NDJSON | Not established | ACP cancel | ACP plus blocking Cursor requests | Load supported, live replay cursor unknown | Exact path unverified | Resume, continue and ACP load | Unverified | Unverified |
| Goose (§I) | Run, ACP and current serve | stdio or vendor ACP HTTP and WS | Agent-specific, not established here | ACP cancel | ACP or headless pre-policy | Load supported, transport replay details incomplete | Data directory `sessions/sessions.db`, SQLite | Saved session and ACP load | Stdio process client-owned, remote lifecycle needs verification | Shared provider default three retries |
| Factory Droid (§J) | Exec, JSON-RPC, ACP and SDK | NDJSON JSON-RPC or output stream | SDK one active turn, no queue promise | SDK interrupt | Reverse permission and ask-user requests | Saved resume, daemon connection, replay cursor unknown | Local store, exact path unverified | Session ID and SDK resume | SDK iterator break interrupts, close releases child | Unverified |
| Kiro (§K) | Headless and native ACP | JSONL or ACP NDJSON | Headless none, ACP extensions negotiated | ACP cancel | ACP options plus consent metadata | Load replay, V3 live updates not buffered | V2 documented files, V3 server-owned layout | Resume ID or ACP load | Headless broken stdout exits, other signals incomplete | Unverified |
| Coder AgentAPI (§L) | Deprecated HTTP wrapper | HTTP, SSE and CLI terminal adaptation | Wrapper input, not semantic steering | CLI-specific terminal input | CLI-specific | Messages endpoint, not native agent replay | Wrapper history, underlying CLI state separate | Underlying CLI-dependent | Wrapper process-dependent | Underlying CLI-dependent |
| MCP as control (§M) | Claude tool server, removed Codex command | Claude stdio MCP | No common agent-turn contract | Tool/protocol-specific | Claude client must confirm tools | No common conversation attach | No common session store | No common resume | Tool-server lifecycle | Not an agent retry contract |

## A. Agent Client Protocol

### A1. Version and transport are negotiated contracts

ACP v1 is stable. V2 is a draft with breaking lifecycle changes. The current repository is
`agentclientprotocol/agent-client-protocol`, not the old Zed namespace
(**Docs**: agentclientprotocol.com/protocol/v1/overview and /protocol/v2/migration).

V1 uses JSON-RPC 2.0 over UTF-8 newline-delimited JSON on stdio. Each message occupies one
line. Stdout is protocol-only and diagnostics belong on stderr. Streamable HTTP is still a
draft transport proposal. Implementations may supply other bidirectional transports, but that
does not make their endpoints a stable interoperable ACP remote standard
(**Docs**: agentclientprotocol.com/protocol/v1/transports).

The client sends `initialize` with `protocolVersion`, `clientCapabilities` and optional
`clientInfo`. It reads agent capabilities and `authMethods`, then calls `authenticate` when
needed. `session/new` takes an absolute `cwd` and `mcpServers`. Its response carries
`sessionId`. The following is an abbreviated v1 exchange with illustrative IDs
(**Docs**: agentclientprotocol.com/protocol/v1/initialization and /protocol/v1/session-setup).

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{}}}
{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/workspace","mcpServers":[]}}
{"jsonrpc":"2.0","id":2,"result":{"sessionId":"s1"}}
{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"text","text":"Run tests"}]}}
{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Checking tests"}}}}
{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}
```

### A2. Turns, permissions and delegated tools

V1 progress is `session/update`. Discriminators include `agent_message_chunk`,
`agent_thought_chunk`, `tool_call`, `tool_call_update`, `plan`, `current_mode_update` and
`config_option_update`. Completion is the response to the original `session/prompt`, with
`stopReason` such as `end_turn`, `cancelled`, `refusal` or `max_turn_requests`
(**Docs**: agentclientprotocol.com/protocol/v1/prompt-turn).

The documented v1 sequence sends the next prompt after the preceding turn completes.
It does not establish a universal mid-turn queue or steering policy. Treat concurrent prompts
as agent-specific. Gemini demonstrates why that matters (§G2). `session/cancel` is a
notification, not an RPC whose response confirms completion
(**Docs**: agentclientprotocol.com/protocol/v1/prompt-turn. Policy distinction: inference).

`session/request_permission` is an agent-to-client request. It includes `sessionId`,
`toolCall` and `options`. Each option has `optionId`, `name` and a kind such as `allow_once`,
`allow_always`, `reject_once` or `reject_always`. Reply using the supplied option ID
(**Docs**: agentclientprotocol.com/protocol/v1/tool-calls).

```json
{"jsonrpc":"2.0","id":"p1","method":"session/request_permission","params":{"sessionId":"s1","toolCall":{"toolCallId":"t1","title":"Run tests","status":"pending"},"options":[{"optionId":"yes","name":"Allow once","kind":"allow_once"}]}}
{"jsonrpc":"2.0","id":"p1","result":{"outcome":{"outcome":"selected","optionId":"yes"}}}
{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}
```

Cancellation requires the client to answer pending permissions with the `cancelled` outcome.
The agent should stop model requests and tools, may emit final updates and must eventually
resolve the original prompt with `stopReason: "cancelled"`. A cancelled request must not be
treated as confirmed merely because the notification was written
(**Docs**: agentclientprotocol.com/protocol/v1/prompt-turn. Last sentence: inference).

Optional client methods are `fs/read_text_file`, `fs/write_text_file`, `terminal/create`,
`terminal/output`, `terminal/wait_for_exit`, `terminal/kill` and `terminal/release`.
Paths are absolute and line numbers are 1-based. The client advertises the capabilities
it actually implements. These callbacks can put tool execution in the client's environment
rather than beside the agent process (inference)
(**Docs**: agentclientprotocol.com/protocol/v1/file-system and /protocol/v1/terminals).

`session/set_mode` selects an advertised mode ID. Modes and configuration options belong to
the agent. A common mode name does not promise identical permission policy across agents
(**Docs**: agentclientprotocol.com/protocol/v1/session-modes. Last sentence: inference).

### A3. Loading, resuming and the implementation matrix

V1 `session/load` requires `agentCapabilities.loadSession: true`. The agent replays the
conversation through `session/update` before responding. The separate, capability-gated
`session/resume` restores without history replay. `session/close`, list and delete are also
capability-based surfaces. They are no longer all merely future ideas
(**Docs**: agentclientprotocol.com/protocol/v1/session-setup).

Load does not guarantee live second-client attachment. Storage, concurrent writers, v1 replay
cursors, EOF, SIGTERM and model retries remain implementation concerns
(**Docs**: v1 session-setup and transports. Absence of a shared guarantee: inference).

The official registry lists native integrations including Gemini, Copilot, OpenCode, Cursor,
Goose, Factory and Kiro. Claude and Codex use adapters. Verified clients include Zed,
JetBrains IDEs and Emacs `agent-shell.el`. Registry membership is not a capability guarantee
(**Docs**: agentclientprotocol.com/get-started/agents and /get-started/clients).

The current Claude adapter advertises load and resume, reads stored history and resumes the
Agent SDK session. Its current home is `agentclientprotocol/claude-agent-acp`
(**Code**: agentclientprotocol/claude-agent-acp@ebfbfbd
`src/acp-agent.ts:2953-2986,3065-3083,3149`).
The current Codex adapter supports load, replay and resume and negotiates protocol 1 or 2.
The old `zed-industries/codex-acp` repository is archived
(**Code**: agentclientprotocol/codex-acp@a77829f
`src/CodexAcpServer.ts:475-584,644-666,1640-1753`. Archive: GitHub repository metadata).

Native load support was verified for Gemini (§G2), OpenCode (§E3), Cursor (§H1), Goose (§I1)
and Kiro (§K1). Copilot's native ACP command exists, but its current load advertisement was
not established by the inspected SDK source. Its SDK resume is a different interface (§D3).

### A4. Draft v2 changes what completion means

Draft v2 makes `session/prompt` acknowledge acceptance. `state_update` then reports `running`,
`idle` or `requires_action` and completion reasons. Clients cannot reuse v1's rule that a
prompt response means the turn ended. Updates become ID-based upserts with explicit patch
semantics. Omitted values preserve state, null clears values and chunks append
(**Docs**: agentclientprotocol.com/protocol/v2/migration).

V2 replaces `authenticate` with `auth/login`, removes client filesystem and terminal execution
and removes session modes. It replaces load with resume using `replayFrom: {"type":"start"}`.
These are draft-version facts, not instructions to send v2 fields to a v1 server
(**Docs**: agentclientprotocol.com/protocol/v2/migration).

## B. Codex

### B1. App-server is the full local control interface

`codex exec-server` is the outbound executor for an OpenAI-owned cloud loop, not this local
agent server. Its relayed tool-process protocol is covered in `cloud-agent-internals.md §B6`.

`codex app-server` uses bidirectional JSON-RPC-style messages without the `jsonrpc` header.
Default stdio uses one JSON message per line. `--listen ws://127.0.0.1:4500` uses one JSON
message per WebSocket text frame. Unix sockets use HTTP Upgrade. The docs label the
WebSocket transport experimental and unsuitable as a production compatibility promise
(**Docs**: learn.chatgpt.com/docs/app-server).

Each connection sends `initialize`, receives its response and sends `initialized`.
`clientInfo` identifies the client. `capabilities.experimentalApi` opts into experimental
methods and fields. Generate matching types or JSON Schema from the same executable
(**Docs**: learn.chatgpt.com/docs/app-server).

```sh
codex app-server generate-ts --out ./schemas
codex app-server generate-json-schema --out ./schemas
```

Core requests are `thread/start`, `thread/resume`, `thread/fork`, `thread/read`,
`thread/list`, `turn/start`, `turn/steer` and `turn/interrupt`. Notifications include
`turn/started`, `turn/completed`, `item/started`, `item/completed` and
`item/agentMessage/delta`. A started thread subscribes that connection to its events
(**Docs**: learn.chatgpt.com/docs/app-server).

```json
{"id":1,"method":"initialize","params":{"clientInfo":{"name":"supervisor","version":"1"}}}
{"method":"initialized","params":{}}
{"id":2,"method":"thread/start","params":{"cwd":"/workspace"}}
{"id":3,"method":"turn/start","params":{"threadId":"thread1","input":[{"type":"text","text":"Run tests"}]}}
{"id":4,"method":"turn/steer","params":{"threadId":"thread1","expectedTurnId":"turn1","input":[{"type":"text","text":"Focus on unit tests"}]}}
{"id":5,"method":"turn/interrupt","params":{"threadId":"thread1","turnId":"turn1"}}
```

Illustrative IDs stand for returned IDs. Interleaved responses and notifications are omitted.
These shapes come from the docs and protocol types
(**Docs**: app-server. **Code**: openai/codex@970111f
`codex-rs/app-server-protocol/src/protocol/v2/turn.rs:317-351`).

### B2. Steering and approval are explicit

`turn/steer` adds input to the active turn. `expectedTurnId` must match that turn, preventing
a stale client from steering a different one. `turn/interrupt` stops the active turn, whose
completion status becomes interrupted. Sending another ordinary start is not a substitute
for choosing the intended operation
(**Docs**: app-server. **Code**: openai/codex@970111f
`codex-rs/app-server-protocol/src/protocol/v2/turn.rs:317-351`. Last sentence: inference).

Server requests include `item/commandExecution/requestApproval`,
`item/fileChange/requestApproval` and `item/permissions/requestApproval`.
Command and file decisions include `accept`, `acceptForSession`, `decline` and `cancel`.
Decline can let the turn continue. Cancel interrupts it. Responses match the server request ID
(**Code**: openai/codex@970111f
`codex-rs/app-server-protocol/src/protocol/common.rs:1822-1849` and
`codex-rs/app-server-protocol/src/protocol/v2/item.rs:68-125`).

```json
{"id":"approval1","result":{"decision":"accept"}}
```

This answers an existing request. Sandbox and approval policies remain separate
(**Docs**: learn.chatgpt.com/docs/app-server).

### B3. Exec, SDK and persisted history

`codex exec --json` emits JSONL events such as `thread.started`, `turn.started`,
`item.started`, `item.completed`, `turn.completed`, `turn.failed` and `error`.
It is a one-shot process. Its stdin supplies the prompt, not a reusable JSON-RPC channel.
`codex exec resume --last "Continue"` or an explicit session ID starts a fresh process using
saved context. `--ephemeral` disables session persistence
(**Docs**: learn.chatgpt.com/docs/non-interactive-mode).

The TypeScript SDK wraps exec. `runStreamed()` reads its JSONL events, remembers the
`thread.started` ID and passes that ID to a later exec resume invocation. Its process helper
writes the prompt and immediately closes stdin. EOF is normal input completion in this
interface, not a request to abort
(**Code**: openai/codex@970111f `sdk/typescript/src/thread.ts:65-115` and
`sdk/typescript/src/exec.ts:173-216`).

The rollout recorder stores JSONL under
`~/.codex/sessions/YYYY/MM/DD/rollout-<timestamp>-<conversation_id>.jsonl`.
`CODEX_HOME` changes the root. Current code also migrates legacy rollouts into paginated
thread history, so the legacy JSONL tree is not a complete stable storage API
(**Code**: openai/codex@970111f `codex-rs/rollout/src/recorder.rs:101-102,1775` and
`codex-rs/cli/src/main.rs:221-222`. Root override: **Docs**, /auth).

Use `thread/resume` for persisted context and subscription, with `thread/read`,
`thread/turns/list` or `thread/items/list` for history. Do not equate text deltas with a
durable replay log. The inspected docs do not establish a lossless cursor for every live
delta or a safe concurrent-writer policy
(**Docs**: learn.chatgpt.com/docs/app-server. Limit of the evidence: inference).

### B4. Client loss, shutdown and credentials

The multi-client server unloads a thread only after it has no subscribers and is inactive.
Its unloading deadline uses the later of those two transitions plus a grace delay. Active
work is not eligible merely because its last client disappeared
(**Code**: openai/codex@970111f
`codex-rs/app-server/src/request_processors/thread_lifecycle.rs:22-82`).

That rule does not make stdio detached. In single-client mode a closed stdio connection
breaks the server loop and starts shutdown. Multi-client signal handling can drain active
turns on the first SIGTERM or Ctrl-C and force shutdown on a second signal. SIGHUP is
graceful-only. The graceful signal restart path excludes single-client mode
(**Code**: openai/codex@970111f
`codex-rs/app-server/src/lib.rs:226-317,817,1196-1219`).

The exec SDK kills a still-running child during cleanup. Process ownership matters independently
(**Code**: openai/codex@970111f `sdk/typescript/src/exec.ts:249-260`. Implication: inference).

Saved credentials live in `~/.codex/auth.json` or an OS credential store.
`cli_auth_credentials_store` chooses `file`, `keyring`, `auto` or `ephemeral`.
File storage follows `CODEX_HOME`. API-key login reads a supplied key and the exec SDK
maps its `apiKey` option to `CODEX_API_KEY`. Do not assume that option overrides every
app-server authentication mode
(**Docs**: learn.chatgpt.com/docs/auth. **Code**: openai/codex@970111f
`sdk/typescript/src/exec.ts:196-205`. Final caution: inference).

### B5. Model retries and recovery after network loss

Provider defaults are `request_max_retries = 4`, `stream_max_retries = 5`,
`stream_idle_timeout_ms = 300000` and `websocket_connect_timeout_ms = 15000`.
Retry counts are capped at 100. These keys belong to the model provider configuration
(**Code**: openai/codex@970111f
`codex-rs/model-provider-info/src/lib.rs:68-77,190-199,508-533`).

The HTTP retry layer retries transport errors and 5xx, not 429 at that layer. It starts
from 200 ms with exponential backoff and roughly 10 percent jitter. Despite the internal
name `max_attempts`, its loop includes attempt zero. A setting of four therefore permits
the initial HTTP request plus four retries. Server retry advice can replace the delay
(**Code**: openai/codex@970111f
`codex-rs/model-provider-info/src/lib.rs:464-470` and
`codex-rs/codex-client/src/retry.rs:23-51,84-117`).

The higher response layer classifies stream failures, rate limits, timeouts, connection
failures and internal errors separately. Invalid requests, quota exhaustion, policy
failures and interruptions are terminal. A WebSocket-to-HTTPS fallback can reset the
bounded retry count after respecting the server retry deadline
(**Code**: openai/codex@970111f `codex-rs/protocol/src/error.rs:418-469` and
`codex-rs/core/src/responses_retry.rs:121-139`).

Important current change: `unbounded_connection_retries` is stable and enabled by default.
For sampling turns with `ConnectionFailed`, outside internal session sources and outside
Amazon Bedrock, it waits for the network without consuming the ordinary stream retry
budget. Delay starts at five seconds and doubles to 60 seconds
(**Code**: openai/codex@970111f `codex-rs/features/src/lib.rs:1485-1490` and
`codex-rs/core/src/responses_retry.rs:23-24,94-118`).

Thus five stream retries is not a blanket offline limit. Other reset classes can remain
bounded. Successful VM pause recovery was not tested (inference from §B5).

## C. Claude Code and Claude Agent SDK

### C1. Print mode can be one-shot or bidirectional

The SDK's local subprocess hosting model is covered in `always-on-agents.md §C4`.
It is distinct from Anthropic's self-hosted runner and cloud-owned Managed Agents loop in
`cloud-agent-internals.md §C7` and §C8.

`claude -p` runs non-interactively. `--output-format stream-json --verbose` emits NDJSON.
`--include-partial-messages` adds raw streaming events. `--input-format stream-json`
keeps structured user input on stdin, rather than treating stdin as one plain prompt.
Output includes initialization, user and assistant messages, partial events and results
(**Docs**: code.claude.com/docs/en/headless).

```sh
claude -p --verbose --input-format stream-json --output-format stream-json
```

```json
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Run tests"}]}}
```

The TypeScript SDK uses `query({prompt: AsyncIterable<SDKUserMessage>, options})`.
Python's `ClaudeSDKClient` supports a persistent connection. Streaming input permits
sequential queued messages, permission callbacks and interruption. It is not equivalent
to the single-string query convenience interface
(**Docs**: code.claude.com/docs/en/agent-sdk/streaming-vs-single-mode and /agent-sdk/python).

The SDK wraps the child process protocol. Python routes `control_request` and
`control_response` messages, dispatches `can_use_tool` and hook requests and sends an
interrupt control request. Applications should use the SDK instead of copying undocumented
control fields as if they were an independent versioned public wire spec
(**Code**: anthropics/claude-agent-sdk-python@0e89b29
`src/claude_agent_sdk/_internal/query.py:398-412,577-733,792-794`. Advice: inference).

```json
{"type":"control_request","request_id":"r1","request":{"subtype":"interrupt"}}
```

That abbreviated message illustrates the SDK control layer, not a user message.
TypeScript exposes `interrupt()`. Python consumers can use `receive_messages()` for a
continuous stream. `receive_response()` stops at the first result
(**Docs**: Agent SDK Python reference. **Code**:
`npm:@anthropic-ai/claude-agent-sdk@0.3.310 package/sdk.d.ts:2929`).

### C2. Permissions, EOF and process termination

`canUseTool` handles requests not already resolved by policy. It is not a callback for
every tool. A `PreToolUse` hook is the per-invocation gate. Deny rules, ask rules,
permission modes and allow rules can resolve a call before the callback is reached
(**Docs**: code.claude.com/docs/en/agent-sdk/permissions).

Modes include `default`, `dontAsk`, `acceptEdits`, `bypassPermissions`, `plan` and `auto`.
`dontAsk` denies unresolved requests rather than waiting for interaction. `allowedTools`
auto-approves listed tools, not a guarantee that unlisted tools are absent.
SDK callbacks can allow with updated input or deny with a message
(**Docs**: Agent SDK permissions and TypeScript reference).

After streaming input reaches EOF, print mode finishes the turn and may wait for background
work. A result is a better completion marker than an assistant's `end_turn` alone.
The default continuous idle background wait ceiling is ten minutes, with documented
exceptions for an active main background command. `CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS`
changes it and zero removes that ceiling
(**Docs**: code.claude.com/docs/en/headless).

SIGTERM is not the SDK interrupt operation. The documented CLI exits 143, terminates Bash
descendants and leaves the turn unfinished without a result. It runs SessionEnd rather than
all normal end-of-turn behavior. Send SIGINT or use SDK interruption before stopping when
the caller needs a completed interrupted turn
(**Docs**: code.claude.com/docs/en/headless).

The Python SDK registers child cleanup at parent exit. Losing an SSH-owned SDK parent need not
leave its child alive, regardless of model progress
(**Code**: anthropics/claude-agent-sdk-python@0e89b29
`src/claude_agent_sdk/_internal/transport/subprocess_cli.py:54-68`. Implication: inference).

### C3. Session files, new-process resume and credentials

`--continue` selects the latest conversation for the working directory. `--resume ID`
selects a saved session. `--session-id UUID` chooses an ID and `--fork-session` creates
a new session from resumed history. SDK options include `resume` and `forkSession`
(**Docs**: code.claude.com/docs/en/headless and /agent-sdk/sessions).

Transcripts are JSONL under `~/.claude/projects/<encoded-cwd>/`. Nonalphanumeric path
characters become hyphens. Long directory encodings are truncated with a hash.
`CLAUDE_CONFIG_DIR` changes the root and `CLAUDE_CODE_PROJECT_DIR_NAME` can override
the project directory name. These are conversations, not filesystem snapshots
(**Docs**: code.claude.com/docs/en/agent-sdk/sessions and /env-vars).

TypeScript has `persistSession: false` and an alpha `sessionStore` transcript adapter.
A transcript does not serialize children, callbacks or arbitrary tools. The local SDK does not
document general live attachment with a missed-token cursor
(**Docs**: Agent SDK sessions. **Code**:
`npm:@anthropic-ai/claude-agent-sdk@0.3.310 package/sdk.d.ts:1867`.
Limits of transcript recovery: inference).

Normally resume waits for a new prompt. `CLAUDE_CODE_RESUME_INTERRUPTED_TURN=1` requests
continuation of an interrupted turn, not restoration of external tool connections
(**Docs**: code.claude.com/docs/en/headless. Last sentence: inference).

`ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `apiKeyHelper` and
`CLAUDE_CODE_OAUTH_TOKEN` are supported credential paths with documented precedence.
Linux stores saved login credentials in `~/.claude/.credentials.json`, mode 0600.
macOS normally uses Keychain, with a documented file fallback. `CLAUDE_CONFIG_DIR`
also changes that credential namespace. Bare mode does not read the OAuth token variable
(**Docs**: code.claude.com/docs/en/authentication).

### C4. Retries explicitly account for sleep and partial output

The default transient retry count is ten. `CLAUDE_CODE_MAX_RETRIES` is normally capped at
15. The docs distinguish failures before response content from failures after completed
text or tools. They explicitly describe detecting a connection broken by computer sleep
(**Docs**: code.claude.com/docs/en/errors#automatic-retries).

Before completed output, eligible dropped connections can reissue the request.
Some failures after thinking but before text or tools get two quick retries.
A stalled stream before completed output can be retried once. Once completed text or
tool calls exist, Claude avoids blindly replaying the request because tools could execute
twice. It retains completed output and can continue from completed tool results
(**Docs**: /errors#automatic-retries).

`CLAUDE_CODE_RETRY_WATCHDOG=1` retries eligible 429 and 529 capacity errors indefinitely.
It excludes spend limits and exhausted credits. Since 2.1.199 it raises other transient
errors' default retry allowance to 300 and removes the ordinary count cap.
`CLAUDE_CODE_RETRY_WATCHDOG_MAX_WAIT_MS`, added in 2.1.295, can bound each request's
cumulative wait for 429 and 529. An unset budget is unlimited
(**Docs**: /errors#tune-retry-behavior and /env-vars).

Normal 529 overload backoff starts at 500 ms and caps at 32 seconds. The
`CLAUDE_CODE_OVERLOADED_RETRY_BASE_DELAY_MS` and
`CLAUDE_CODE_OVERLOADED_RETRY_MAX_DELAY_MS` controls do not govern watchdog or fast mode.
Certificate validation errors are not retried. Transient TLS failures can be
(**Docs**: /errors#automatic-retries and /env-vars).

Four timers cover different stalls. The event watchdog defaults to 300 seconds.
On direct Anthropic API connections, the byte watchdog defaults to 180 seconds.
The first-byte deadline is normally 180 seconds there, with request-size adjustment.
Other providers have different timer coverage and defaults. The body idle timeout is
five minutes on the documented fallback connections
(**Docs**: code.claude.com/docs/en/network-config#streaming-idle-watchdogs).

`CLAUDE_STREAM_IDLE_TIMEOUT_MS` configures watchdogs, with documented clamps.
`CLAUDE_BYTE_STREAM_IDLE_TIMEOUT_MS` overrides the byte timer.
`CLAUDE_STREAM_FIRST_BYTE_TIMEOUT_MS` controls the first-byte deadline.
`API_TIMEOUT_MS` limits waiting for headers, not an already streaming response.
These are not interchangeable whole-turn deadlines
(**Docs**: /network-config#streaming-idle-watchdogs).

SDK `system` messages with subtype `api_retry` carry `attempt`, `max_retries`,
`retry_delay_ms`, `error_status` and `error`. Hypervisor pause equivalence was not tested
(**Code**: `npm:@anthropic-ai/claude-agent-sdk@0.3.310`
`package/sdk.d.ts:3664-3683`. Final limitation: inference).

## D. GitHub Copilot CLI and SDK

### D1. Three different entry points

`copilot -p PROMPT` exits after completion. Piped stdin supplies a prompt when `-p` is absent.
`--output-format=json` produces JSONL. `--allow-tool`, `--deny-tool`, URL permissions and
path permissions are separate controls. `--allow-all-tools` is not the same as
`--allow-all`, which also grants paths and URLs. Programmatic examples support scoped
permissions rather than requiring unrestricted access in every case
(**Docs**: CLI command reference and Running GitHub Copilot CLI programmatically).

`--resume=ID` restores a selected session and `--continue` chooses the most recent for
the directory, then falls back globally. Bare resume may require an interactive picker.
The native `--acp` flag exists. Its framing follows ACP, not the SDK's transport
(**Docs**: docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference).

The SDK is generally available and promises semantic versioning. It supports multiple
languages and drives a Copilot CLI process through JSON-RPC. The Node client starts
`--headless --no-auto-update`, then selects `--stdio` or a TCP port
(**Code**: github/copilot-sdk@be9764f `README.md:144-146` and
`nodejs/src/client.ts:3180-3208`).

Its stdio uses `vscode-jsonrpc` stream readers and writers, not ACP's NDJSON reader.
The SDK protocol pin is version 3. Reuse the SDK framing and handshake rather than sending
ACP lines to the headless server
(**Code**: github/copilot-sdk@be9764f `nodejs/src/client.ts:3529-3537` and
`sdk-protocol-version.json:1-3`. Integration consequence: inference).

### D2. Sending, watching and approving

Core requests include `session.create`, `session.resume`, `session.send`, `session.getMessages`
and `session.abort`. `session.send` returns a user-message ID, not the completed answer
(**Code**: github/copilot-sdk@be9764f `nodejs/src/session.ts:790-831,2698-2703,2776-2779`).

`session.event` carries events including `assistant.message_delta`, `assistant.message`,
`tool.execution_start`, `tool.execution_complete`, `session.error` and `session.idle`.
Idle means no background agents or attached shell commands remain in flight
(**Code**: github/copilot-sdk@be9764f `nodejs/src/client.ts:3601-3603` and
`nodejs/src/generated/session-events.ts:2206-2317,6632-7113,8037-8358`).

```json
{"jsonrpc":"2.0","id":2,"method":"session.send","params":{"sessionId":"s1","prompt":"Run tests","mode":"enqueue"}}
{"jsonrpc":"2.0","id":3,"method":"session.abort","params":{"sessionId":"s1"}}
```

These are decoded JSON-RPC bodies, not NDJSON framing examples. The send mode defaults
to `enqueue`. The other documented value is `immediate`. The inspected type comment says
send immediately, not kill the current tool. `sendAndWait()` has a default 60-second
client wait timeout. That timeout does not abort the agent's work
(**Code**: github/copilot-sdk@be9764f `nodejs/src/types.ts:3844-3849` and
`nodejs/src/session.ts:845-851`).

Permissions arrive as `permission.requested` events. The SDK invokes
`onPermissionRequest`, then calls its pending-permission response RPC with `requestId`
and the result. If a hook already resolved the request, the SDK does not prompt again.
Without a handler, a consumer must handle pending requests rather than assuming approval
(**Code**: github/copilot-sdk@be9764f `nodejs/src/session.ts:1513-1521,1714-1763` and
`nodejs/src/types.ts:2923-2927`).

User questions also use a distinct reverse request, `userInput.request`.
An unattended client needs a policy for those requests as well as tool permissions
(**Code**: github/copilot-sdk@be9764f `nodejs/src/client.ts:3609-3617`.
Required client behavior: inference).

### D3. History, replay and resumed pending work

`getEvents()` calls `session.getMessages`. New experimental APIs include
`session.eventLog.read`, `session.eventLog.tail` and `sessions.readPersistedEvents`.
Cursor reads support forward or backward windows, optional waiting and filter parameters.
They are stronger catch-up tools than relying only on live notifications
(**Code**: github/copilot-sdk@be9764f `nodejs/src/session.ts:2698-2703` and
`nodejs/src/generated/rpc.ts:2430-2466,42544-42578`. Comparison: inference).

An expired cursor is not successful completion. Active-log reads can return an overlapping
boundary window. Persisted reads return an empty terminal page and do not switch journal
generations. Deduplicate or rebuild rather than treating every reconnect as a fresh live
subscription with no missing history
(**Code**: github/copilot-sdk@be9764f `nodejs/src/generated/rpc.ts:2456-2466`.
Consumer rule: inference).

Session files include `~/.copilot/session-state/<id>/events.jsonl`, checkpoints, plans and
tracked files. `session-store.db` indexes cross-session data. `COPILOT_HOME` changes the
root. The docs distinguish user `settings.json` from internal `config.json`
(**Docs**: docs.github.com/en/copilot/reference/copilot-cli-reference/cli-config-dir-reference).

SDK resume normally treats pending work as interrupted. `continuePendingWork: true` can
continue pending external tools or permissions. Completed durably recorded results remain.
Pending permissions are re-emitted and external tool results require the consumer's RPC.
This is not equivalent to serializing in-memory callbacks
(**Code**: github/copilot-sdk@be9764f `nodejs/src/types.ts:3415-3439`.
Final distinction: inference).

The guide requires locking for undefined concurrent access. Exported remote history is
read-only, not steering. Resume and cursors do not establish safe simultaneous writers
(**Code**: github/copilot-sdk@be9764f
`docs/features/session-persistence.md:727-730` and `docs/features/remote-sessions.md:1-16`).

### D4. Detach, stop and credentials

`session.disconnect()` calls `session.detach`. It frees local handlers and leaves saved
data. `abort()` interrupts work while keeping the session usable. `CopilotClient.stop()`
is different again. For an owned stdio child it closes stdin, waits for exit and can
terminate the child. An externally managed server has different process ownership
(**Code**: github/copilot-sdk@be9764f `nodejs/src/session.ts:2727-2748,2776-2779` and
`nodejs/src/client.ts:1317-1351`).

There is no default session idle timeout. That is not proof that TCP disconnect or SIGTERM
preserves a running turn. Runtime retries, watchdogs and raw EOF remain gaps
(**Code**: github/copilot-sdk@be9764f
`docs/features/session-persistence.md:578-580`. Evidence limits: inference).

Authentication checks `COPILOT_GITHUB_TOKEN`, then `GH_TOKEN`, then `GITHUB_TOKEN`.
Saved login uses system credential storage, with a documented plaintext fallback.
Supported token type and Copilot entitlement matter. A classic `ghp_` PAT is not supported.
Explicit SDK tokens are passed through a dedicated child environment variable
(**Docs**: Copilot CLI command reference.
**Code**: github/copilot-sdk@be9764f `nodejs/src/client.ts:3180-3208`).

## E. OpenCode

### E1. Released docs and development source are different APIs

The current home is `anomalyco/opencode`. Live docs describe `opencode serve` on
`127.0.0.1:4096`, OpenAPI at `/doc`, session routes under `/session` and SSE at `/event`.
`OPENCODE_SERVER_PASSWORD` enables HTTP Basic authentication, with optional
`OPENCODE_SERVER_USERNAME`. The documented SDK is `@opencode-ai/sdk`
(**Docs**: opencode.ai/docs/server and /sdk).

Those docs describe `POST /session/:id/message`, asynchronous `prompt_async` and
`POST /session/:id/abort`. The pinned development OpenAPI instead has `/api/session`,
`/api/session/{sessionID}/prompt`, `/interrupt` and `/api/event`.
Do not splice fields from one version into the other
(**Docs**: /server. **Code**: anomalyco/opencode@f20760b
`packages/protocol/openapi.json:797,2090,4100,9985`. Integration rule: inference).

For the development snapshot, this example creates a session, enqueues input and interrupts.
The illustrative `s1` must be replaced with the returned ID. An HTTP 200 prompt response
contains an inbox item, not the final model answer
(**Code**: anomalyco/opencode@f20760b `packages/protocol/openapi.json:797,2090,4100`).

```http
POST /api/session
Content-Type: application/json

{}
POST /api/session/s1/prompt
Content-Type: application/json

{"text":"Run tests","delivery":"steer"}
POST /api/session/s1/interrupt
```

### E2. Durable input and live-only events

Development prompt input includes `text`, `files`, `agents`, `skills`, `metadata` and
`delivery`. Delivery defaults to `steer`, promoted at a step boundary during work.
`input` waits for an idle input boundary. Inbox entries are backed by database tables,
not only a process-local queue
(**Code**: anomalyco/opencode@f20760b
`packages/core/src/session/prompt.ts:20-27,43-50,77-87` and
`packages/core/src/session/inbox.ts:44-49`).

Permission requests use `POST /api/session/{sessionID}/permission/{requestID}/reply`,
with `decision` and optional `message`. Messages and an experimental log are separate routes
(**Code**: anomalyco/opencode@f20760b `packages/protocol/openapi.json:3927,4924,9117`).

The development SSE feed writes `data: <JSON>` frames without SSE IDs. Each subscriber
gets a new queue, with capacity 4096. No subscribers means no delivery and overflow fails
the subscriber. Reopening the event stream does not replay missed events. Recover history
through the session API rather than assuming `Last-Event-ID` works
(**Code**: anomalyco/opencode@f20760b `packages/server/src/event-feed.ts:8,29-30,48-79`.
Recovery rule: inference).

`opencode attach URL` joins a server. `opencode run --attach URL` uses an existing server.
`--session`, `--continue`, `--fork` and `--format json` are documented run controls.
The SDK can start a server with `createOpencode` or connect using `createOpencodeClient`
(**Docs**: opencode.ai/docs/cli and /sdk).

### E3. Persistence, ACP and model recovery

Current storage is SQLite. Default roots follow XDG and otherwise use
`~/.local/share/opencode`. `OPENCODE_DB` overrides the database filename.
Normal channels use `opencode.db`, while other channel names can select separate files
(**Code**: anomalyco/opencode@f20760b `packages/util/src/global-roots.ts:4-18` and
`packages/cli/src/database-path.ts:4-12`).

Released docs use the data root's `auth.json` or provider environment variables.
Development has credential API routes. Copying the old auth file is not a verified migration
(**Docs**: opencode.ai/docs/cli. **Code**: anomalyco/opencode@f20760b
`packages/protocol/openapi.json:7944`. Migration limit: inference).

`opencode acp` is native stdio ACP. Development advertises protocol 1, `loadSession: true`
and resume, close, list, delete and fork capabilities. Its load handler attaches the stored
session and replays history before returning
(**Code**: anomalyco/opencode@f20760b `packages/cli/src/acp/service.ts:119-163`).

The development model retry schedule starts at two seconds, doubles and caps gaps at ten
seconds, for ten retries with jitter. Provider retry advice can extend gaps, capped at
15 minutes. Transport timeout retries have a separate limit of three. Plugins can alter
session retry decisions and the runner emits `RetryScheduled`
(**Code**: anomalyco/opencode@f20760b `packages/core/src/model-retry.ts:11-58` and
`packages/core/src/session/runner/retry.ts:32-75`).

Retryable classes include rate limits, provider internal errors, eligible transport errors
and incomplete streams. Authentication, quota, policy and invalid requests are terminal.
An accepted WebSocket write and a failed read are distinguished. The caller still decides
whether to retry or continue from durable output
(**Code**: anomalyco/opencode@f20760b `packages/ai/src/provider-error.ts:81-114`).

## F. Amp

### F1. Local execution, streaming input and steering

`amp --execute` or `amp -x` runs locally, waits and exits. `--stream-json` requires execute
mode and emits NDJSON with `system`, `user`, `assistant` and final `result` messages.
`--stream-json-input` reads structured user messages on stdin
(**Docs**: ampcode.com/docs/cli/execute-mode and /cli/streaming-json).

```sh
amp --execute --stream-json --stream-json-input
```

```json
{"type":"user","steer":true,"message":{"role":"user","content":[{"type":"text","text":"Focus on unit tests"}]}}
```

Ordinary incoming messages queue. A user event appears when processing starts, not merely
when input arrives. `steer: true` takes effect at the next interruption point. That does not
promise an immediate process kill or a new independent turn
(**Docs**: ampcode.com/docs/cli/streaming-json. Last distinction: inference).

EOF alone is not completion. Execute waits for the assistant, closed stdin and background
subagents. An assistant `end_turn` can precede the final result. The docs also say background
subagents can survive the initiating Amp process and are reported on thread continuation
(**Docs**: ampcode.com/docs/cli/streaming-json).

### F2. Threads are service-owned, execution need not be

Threads are server-side and retain a stable thread URL. Continue using
`amp threads continue T-... --execute "Continue" --stream-json`.
`-ox` starts an orb and returns a thread URL while remote work continues.
`--executor runner:ID` targets a connected runner. These are not the same process-lifetime
contract as local execute mode
(**Docs**: ampcode.com/docs/threads and /cli/execute-mode).

Cross-Client Access lets the web app send messages to a live CLI thread. Workspace policy
can disable it. Optional terminal access has its own flag. The public docs do not specify
a raw client attachment handshake, event replay cursor or safe simultaneous SDK writers
(**Docs**: ampcode.com/docs/cli/remote-control. Evidence limit: inference).

The TypeScript and Python SDKs accept a string or asynchronous input, continuation and
executor options. TypeScript uses `execute({prompt, options, signal})`. This subprocess API
does not establish a stable public equivalent of the internal synchronization channel
(**Docs**: ampcode.com/docs/sdk. Last distinction: inference).

### F3. Approval, authentication and recovery limits

Docs route custom permissions through plugins and document SDK AbortSignal cancellation.
Raw approval/cancel wire messages, SIGTERM and lost-client callback recovery were not established
(**Docs**: ampcode.com/docs/cli and /sdk. Unestablished details are gaps).

`AMP_API_KEY` supplies a long-lived access token. The docs warn against using an expiring
login session token for this role. An environment key overrides saved accounts and a
running process keeps its startup account. Exact saved-account files, model retries,
backoff and stalled-stream timers were not verified
(**Docs**: ampcode.com/docs/cli).

## G. Gemini CLI

### G1. Headless output and saved conversations

Headless `gemini -p PROMPT --output-format stream-json` emits JSONL events.
Types include `init`, `message`, `tool_use`, `tool_result`, `error` and `result`.
The JSON output format instead returns a result containing response, stats and error.
Neither output mode is a bidirectional session-control protocol
(**Code**: google-gemini/gemini-cli@cd1b45c `docs/cli/headless.md:8-35`.
Final distinction: inference).

```sh
gemini -p "Run tests" --output-format stream-json
gemini --resume -p "Continue"
gemini --acp
```

Sessions are automatically recorded under `~/.gemini/tmp/<project_hash>/chats/`.
Resume selects the latest session, an index or a UUID
(**Code**: google-gemini/gemini-cli@cd1b45c `docs/cli/session-management.md:7-53`).

Optional file checkpoints use a shadow Git repository at `~/.gemini/history/<project_hash>`.
JSON metadata sits in the project's `checkpoints/` directory beside `chats/`.
`general.checkpointing.enabled` enables this disabled-by-default feature. `/restore` can
restore files, conversation and a proposed tool call, unlike conversation resume alone
(**Code**: google-gemini/gemini-cli@cd1b45c `docs/cli/checkpointing.md:10-57`).

Current source writes `session-<timestamp>-<short-id>.jsonl` and can normalize legacy
`.json` transcripts to JSONL. Subagent transcripts are nested under the parent session.
Do not hard-code the older JSON representation as the only resumable format
(**Code**: google-gemini/gemini-cli@cd1b45c
`packages/core/src/services/chatRecordingService.ts:732-733,779-823,970-971`).

### G2. Native ACP and process ownership

The current ACP flag is `--acp`. The server supports initialize, authentication, new,
load, prompt, cancel and mode selection. It advertises `loadSession: true`
(**Code**: google-gemini/gemini-cli@cd1b45c `docs/cli/acp-mode.md:8-12,78-92` and
`packages/cli/src/acp/acpRpcDispatcher.ts:92,183-208`).

It uses the ACP NDJSON transport. When that connection closes, its finalizer runs exit
cleanup. At the start of an ACP prompt, the session aborts the previous pending prompt
controller before installing another. This is concrete interrupting behavior, not a
universal ACP input queue
(**Code**: google-gemini/gemini-cli@cd1b45c
`packages/cli/src/acp/acpStdioTransport.ts:15-34` and
`packages/cli/src/acp/acpSession.ts:319-328`).

Approvals use ACP `session/request_permission`. Headless callers instead need preselected
approval policy. In the inspected headless path SIGINT and SIGTERM both abort through an
AbortController. A repeated signal forces exit 130. Broken stdout with EPIPE exits.
These details do not establish safe second-client live attachment
(**Code**: google-gemini/gemini-cli@cd1b45c
`packages/cli/src/acp/acpSession.ts:764,1114` and
`packages/cli/src/nonInteractiveCli.ts:139-170`. Final limitation: inference).

Credential paths include `GEMINI_API_KEY`, saved API keys, Google OAuth and Vertex
credentials. `GOOGLE_GENAI_USE_VERTEXAI` selects Vertex and its project/location settings.
The storage layer names the OAuth file `oauth_creds.json` under the Gemini configuration
root. These credentials are separate from project chat logs
(**Code**: google-gemini/gemini-cli@cd1b45c
`packages/core/src/core/contentGenerator.ts:76-96,174-179` and
`packages/core/src/config/storage.ts:22`).

### G3. Connection retries and midstream retries are separate

The generic retry helper defaults to ten total attempts, initial delay five seconds and
maximum delay 30 seconds. Backoff is exponential with about 30 percent jitter.
Retriable failures include 429, 499, 5xx and network errors such as ECONNRESET, EPIPE,
ETIMEDOUT, DNS failures and transport header/body timeouts. An explicit 400 is not retried
(**Code**: google-gemini/gemini-cli@cd1b45c
`packages/core/src/utils/retry.ts:20-62,170-208,322-328`).

`general.maxAttempts` can lower the allowance. `general.retryFetchErrors` enables generic
fetch-failed and incomplete-JSON retries and defaults true in current configuration.
Quota and availability policy can alter behavior, including provider retry advice
(**Code**: google-gemini/gemini-cli@cd1b45c
`packages/core/src/config/config.ts:1391-1395` and
`packages/core/src/utils/retry.ts:349-370`).

The chat streaming layer has a separate four-attempt loop, meaning three midstream
retries, with one, two and four second delays. It does not repeat already exhausted
connection-phase retries. A retry stream event tells consumers to discard partial output
from the failed attempt. Aborted requests are not retried
(**Code**: google-gemini/gemini-cli@cd1b45c
`packages/core/src/core/geminiChat.ts:78-109,733-791`).

## H. Cursor CLI

### H1. Print and ACP are both current

`agent -p --output-format stream-json` runs headlessly.
`--stream-partial-output` adds partial events. Print mode needs `--force` to actually
modify files rather than merely propose changes. `--resume` selects a chat and
`--continue` resumes the recent one. `agent create-chat` and `agent ls` expose IDs
(**Docs**: cursor.com/docs/cli/headless and /cli/reference/parameters).

`agent acp` is native JSON-RPC 2.0 over stdio NDJSON. It supports session load and
cancel, with `agent`, `plan` and `ask` modes. Authentication advertises `cursor_login`.
Existing login, `CURSOR_API_KEY` or `CURSOR_AUTH_TOKEN` can authenticate startup
(**Docs**: cursor.com/docs/cli/acp).

### H2. Extra blocking requests matter

ACP permissions offer `allow-once`, `allow-always` and `reject-once`.
Cursor also sends blocking `cursor/ask_question` and `cursor/create_plan` requests.
A client that implements only tool permissions can still stall at a question or plan.
Other extensions such as `cursor/update_todos` are notifications
(**Docs**: cursor.com/docs/cli/acp. Stalling consequence: inference).

The hosted worker lifecycle is a different surface in `cloud-agent-internals.md §A`.

## I. Goose

### I1. The old goosed surface is being replaced

The project now lives at `aaif-goose/goose`. Its architecture post identifies the older
`goosed` REST and SSE server and the move to a unified ACP server. Current docs use
`goose acp` for stdio and `goose serve` for HTTP or WebSocket. Do not treat the old
goosed REST routes as the current default integration
(**Code**: aaif-goose/goose@c3bfc5f
`documentation/blog/2026-04-08-goose-acp-and-new-tui/index.md:13-36`.
**Docs**: goose-docs.ai/docs/gdk/acp).

The remote server listens on loopback port 3284 by default and exposes `/acp`.
`GOOSE_SERVER__SECRET_KEY` authenticates HTTP with `X-Secret-Key`.
WebSocket clients can use that header or a token query parameter. TLS and origin controls
are documented separately. These vendor transports do not settle ACP's draft standard
(**Docs**: /docs/gdk/acp and /docs/guides/remote-goose-server.
Last distinction: inference).

`goose run -t "Run tests"` is headless. `--no-session` avoids saving its session.
Headless runs do not provide normal interactive permission prompts, so policy must be
chosen beforehand. ACP supplies session load and standard permission/cancel exchanges
(**Code**: aaif-goose/goose@c3bfc5f
`documentation/docs/tutorials/headless-goose.md:120-126,241-248` and
`crates/goose/src/acp/server/load_session.rs:24-49`).

### I2. Storage, secrets and retries

Session management uses SQLite `sessions.db` under the data directory's `sessions/`.
`GOOSE_PATH_ROOT` overrides roots, with `data`, `config` and `state` subdirectories.
Without it, platform directory selection preserves the historical Block namespace
(**Code**: aaif-goose/goose@c3bfc5f
`crates/goose/src/session/session_manager.rs:34,74,1096-1097` and
`crates/goose/src/config/paths.rs:8-45`).

Secrets normally use the system keyring. `GOOSE_DISABLE_KEYRING` changes that behavior.
Provider environment credentials are also configuration-dependent. A session database is
not a replacement for making credentials available to a resumed process
(**Docs**: goose-docs.ai/docs/guides/environment-variables. Last sentence: inference).

The shared provider retry defaults are three retries, one-second initial delay, doubling,
30-second cap and roughly 20 percent jitter. Network, server and rate-limit errors retry.
Default policy can also retry some request failures, while `transient_only` excludes them.
This shared helper alone does not establish every provider's stalled-stream behavior
(**Code**: aaif-goose/goose@c3bfc5f
`crates/goose-provider-types/src/retry.rs:14-42,71-114`. Scope limit: inference).

## J. Factory Droid

### J1. A real multi-turn JSON-RPC mode exists

`droid exec` is read-only by default. `--auto low`, `medium` or `high` increases autonomy.
Output formats include `text`, `json`, `stream-json`, `stream-jsonrpc` and `acp`.
Multi-turn input uses `--input-format stream-jsonrpc` with matching output.
Older `stream-json` input is deprecated, not the preferred current control interface
(**Docs**: docs.factory.com/droid-exec/overview).

```sh
droid exec --input-format stream-jsonrpc --output-format stream-jsonrpc
```

```json
{"jsonrpc":"2.0","id":"1","method":"droid.initialize_session","params":{"machineId":"worker1","cwd":"/workspace"}}
```

Methods include `droid.load_session` and `droid.add_user_message`.
`droid.session_notification` carries progress and completion.
`droid.request_permission` and `droid.ask_user` are reverse requests needing replies.
`--session-id` resumes and `--fork` branches saved context
(**Docs**: docs.factory.com/droid-exec/overview).

### J2. SDK ownership is explicit

The TypeScript package is `@factory/droid-sdk`, with Node APIs under its `/node` export.
Python has `droid-sdk`. TypeScript supports `run`, `createSession`, `resumeSession` and
connection to an existing daemon. A session runs one turn at a time.
`FACTORY_API_KEY` is read from the environment or supplied as an SDK option
(**Docs**: docs.factory.com/sdk/typescript).

`session.interrupt()` normally leaves a terminal `interrupted` result and a reusable
session. Aborting a per-turn signal interrupts and throws its reason. Breaking an SDK
stream iterator also interrupts, rather than merely hiding output.
`close()` releases owned processes and subscriptions
(**Docs**: /sdk/typescript#cancellation-and-interruption).

Resume restores history, directory and settings. Reattach permission and ask-user handlers,
SDK MCP servers, abort signals and observability sinks. These runtime objects are not serialized
(**Docs**: /sdk/typescript#what-persists).

## K. Kiro CLI

### K1. CLI generation is not ACP protocol version

Kiro has V2 and V3 agent harnesses. Current V3 ACP starts with
`kiro-cli acp --agent-engine=v3`, using JSON-RPC 2.0 over stdio NDJSON.
Both harnesses can negotiate ACP protocol 1. A protocol version alone does not identify
their extensions, settings or session storage
(**Docs**: kiro.dev/docs/cli/v3/acp-migration).

V3 supports advertised new, list, load, fork, prompt, cancel and configuration methods.
It replaces V2 model selection with `session/set_config_option`, using `configId: "model"`.
V3 rejects several V2-only ACP launch flags. Optional methods are advertised under
`agentCapabilities._meta.kiro.extensionMethods`, not a universal fixed method catalog
(**Docs**: /cli/v3/acp-migration and /acp).

Permission requests use the supplied ACP options. Persistent consent can additionally
use `_meta.kiro.consent`. Client-owned token mode uses `--auth-method=client` and
`_kiro/auth/getAccessToken`. Otherwise the CLI manages access using its login
(**Docs**: /cli/v3/acp-migration).

### K2. Replay has documented limits

The V2 discovery path is `~/.kiro/sessions/cli/`, with documented JSON metadata and
JSONL events. V3 explicitly treats storage as server-owned. Use list and load, not a
hard-coded parser for the V2 directory. Client-provided MCP definitions must be resent
on load because V3 does not persist them in `session.json`
(**Docs**: kiro.dev/docs/cli/acp and /cli/v3/acp-migration).

Register updates before loading. Live updates are not buffered for future subscribers.
Replay can rebuild state, but it cannot recover a lost prompt response.
The docs explicitly warn against automatically resending a prompt after disconnect
because completed edits could repeat. Remote replay marking is not guaranteed for every
update. Completion still comes from the v1 prompt response, not a turn-end-looking update
(**Docs**: /cli/v3/acp-migration).

### K3. Headless is a different, one-input interface

`kiro-cli chat --no-interactive` accepts one positional or fully piped instruction.
`--output-format stream-json` emits JSONL and V3 interruption has a final interruption
record. There is no headless mid-session input. If stdout closes before completion,
the docs say the run exits nonzero rather than finishing the turn
(**Docs**: kiro.dev/docs/cli/headless).

`--resume-id` restores a local or cloud session. `KIRO_API_KEY` supports headless auth,
but an active browser login takes precedence
(**Docs**: /cli/headless and /getting-started/authentication).

## L. Coder AgentAPI

### L1. Archived wrapper, not a current universal protocol

The repository is archived. Its README says it is deprecated and no longer maintained.
It points to Coder Agents and updated Registry modules instead. The wrapper exposed
`/messages`, `/message`, `/status` and `/events` around a CLI terminal interface.
Those are not native thread, tool and permission APIs from each wrapped agent
(**Code**: coder/agentapi@7c468d5 `README.md:3-4,85-90`.
Last distinction: inference).

The AgentAPI entry in `cloud-agents.md §C` is historical. CLI storage, cancellation and
model retries remain CLI-specific, not guaranteed by wrapper history (inference).

### L2. Its named successor has a different boundary

Coder Agents puts its Go loop, model credentials and history in Coder's control plane.
Workspaces execute tools. This is not a drop-in HTTP-to-any-CLI replacement
(**Docs**: coder.com/docs/ai-coder/agents. Last distinction: inference).

## M. MCP as a control channel

### M1. Codex's historical server was removed

`codex mcp-server` and the `codex-mcp-server` crate were removed on 2026-09-05.
The first-party removal commit deletes the subcommand, implementation, interface docs
and tests. Current CLI subcommands expose MCP configuration as `codex mcp`, not an
agent-running MCP server. Do not build a new client around historical `codex-reply` examples
(**Code**: openai/codex@531f383 commit message and removal diff.
Current command list: openai/codex@970111f `codex-rs/cli/src/main.rs:149-250`).

### M2. Claude's tool server is not its headless agent

`claude mcp serve` is a current stdio MCP server. It exposes Claude Code tools to an MCP
client, which must implement user confirmation for individual calls. It does not expose
the same multi-turn agent lifecycle as print streaming input or the Agent SDK
(**Docs**: code.claude.com/docs/en/mcp#use-claude-code-as-an-mcp-server).

MCP can carry an agent-launching tool without standardizing its turns, approvals, attachment
or retry contract. JSON-RPC alone does not define that boundary (inference from §A and §M2).

## What a supervisor needs from an agent interface

1. **A versioned launch contract.** Pin the binary, schema and adapter together.
   Negotiate capabilities instead of treating a product name as a feature matrix
   (inference from §A1, §A4, §B1, §E1 and §K1).
2. **An explicit owner for the process.** Decide who keeps stdin open, drains stdout,
   retains the child and shuts the server down. Detached work cannot rely on the laptop's
   SSH process lifetime. Local stdio avoids an outside control socket, but SSH-carried stdio
   does not. Neither removes outside model connections
   (inference from §A1, §B1, §B4, §C2, §D4 and §E1).
3. **A real completion marker.** Distinguish prompt acceptance, text completion, tool
   completion, turn completion and background-work completion
   (inference from §A2, §A4, §C2, §D2, §F1 and §K2).
4. **Separate queue, steer and cancel operations.** Preserve the running turn ID and
   confirm interruption before starting replacement work
   (inference from §A2, §B2, §D2, §E2, §F1 and §G2).
5. **An approval and question policy.** Keep request IDs, supplied options and cancellation
   outcomes. Handle blocking questions and plan requests, not only command approvals
   (inference from §A2, §C2, §D2, §H2 and §J1).
6. **A transcript and artifact persistence boundary.** Keep session IDs, working directory,
   credentials and workspace files available independently. Do not interpret conversation
   resume as filesystem rollback (inference from §B3, §C3, §D3, §E3 and §G1).
7. **A reconnect strategy separate from resume.** Specify replay cursor handling, gap
   detection, deduplication and history reconstruction. Never assume SSE reconnect replays
   missed events (inference from §A3, §B3, §D3, §E2 and §K2).
8. **A single-writer or arbitration rule.** A second reader, second controller and fresh
   process are different roles. Avoid assuming resume provides concurrent ownership
   (inference from §A3, §B3, §D3 and §F2).
9. **Reinstalled runtime handlers.** Reattach permission callbacks, user questions and
   external tool implementations when restoring a saved conversation
   (inference from §C3, §D3, §J2 and §K2).
10. **Observable retry state and a bounded operational policy.** Distinguish API retries,
    stream retries, controller reconnects and a final failed turn. Record effective
    provider-specific watchdogs rather than one generic timeout.
    A surviving process does not imply a surviving stream
    (inference from §B5, §C4, §E3 and §G3).
11. **Credential refresh after long idle.** Know whether credentials come from a renewable
    saved login, keychain, fixed environment token or a reverse token callback
    (inference from §B4, §C3, §D4, §F3, §G2 and §K1).
12. **Evidence-driven recovery instead of blind replay.** Inspect turn state and completed
    tool results before repeating a prompt whose response was lost
    (inference from §B3, §C4, §D3, §E2, §G3 and §K2).

## Gaps

- No live CLI, network-fault or hypervisor pause experiment was performed. Retry policies
  are source or docs findings, not proof that each suspended stream recovers in practice.
- ACP's standard remote transport remains draft work. Vendor HTTP and WebSocket support
  does not establish interoperability, shared replay cursors or multi-writer ownership.
- Copilot's native ACP load capability was not verified. Its SDK resume is verified.
  Closed runtime retry counts, stream stall deadlines, raw EOF, TCP disconnect and SIGTERM
  semantics were not established from the public SDK alone.
- Codex's exact treatment of every reset after a hypervisor pause was not verified.
  The unbounded branch is conditional. Current paginated thread storage also means the
  legacy rollout tree should not be treated as a complete stable export format.
- Claude's sleep recovery is documented, but equivalence to microVM suspension was not
  tested. The alpha SessionStore interface is not a stable live-attachment protocol.
- OpenCode's released docs and pinned development API differ. Exact request-disconnect,
  SIGTERM, model stall deadlines and credential migration were not established across
  both versions. The note deliberately does not merge their routes into one contract.
- Amp's local cache and saved-account paths, raw approval/cancel protocol, replay cursor,
  model retry budget and stalled-stream timers were not verified from public sources.
- Gemini's general model-stream idle deadline and safe concurrent-client attachment were
  not established. Generic retry and midstream retry counts are not one combined limit.
- Cursor's exact session and credential files, mid-turn policy, replay cursor, retries,
  stream timeouts and disconnect or signal behavior remain unverified.
- Goose's current remote replay guarantees, mid-turn policy, EOF and SIGTERM behavior
  were not established. Shared provider retry code does not prove every provider's
  midstream recovery or timeout policy.
- Factory's exact local session path, raw daemon attach replay, permission recovery after
  disconnection and model retries remain unverified. SDK cleanup behavior is documented.
- Kiro V3 intentionally treats storage as server-owned. Exact local layout, saved-login
  files, model retries and SIGTERM behavior were not verified. V2 files are not a V3 API.
- No inspected source promises restoration of arbitrary external tool sessions or exactly
  once side effects after an uncertain model response. That requires tool-specific
  evidence, not an assumption based on a resumable conversation.
