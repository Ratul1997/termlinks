# Termlinks AI Workflows: product and implementation plan

Status: implementation contract. The secure sequential vertical slice and local
team-room messaging are implemented; later phases below remain intentionally gated.

## 1. Decision

Build AI workflows as an optional Termlinks subsystem, but do not turn Termlinks
into a generic IDE, model provider, or opaque autonomous swarm.

The useful product is a local-first, cross-harness supervision layer that:

- discovers supported AI tools already installed and configured by the user;
- turns directed natural-language requests into explicit workflow stages;
- keeps each stage attached to a persistent Termlinks session;
- lets a user inspect, steer, approve, pause, resume, or stop exact workers from
  the phone, browser, CLI, or native computer terminal;
- preserves authoritative workflow state on the user's computer;
- uses the existing encrypted outbound bridge for public remote access; and
- isolates concurrent write work with Git worktrees.

The coordinator is deterministic infrastructure. An AI may propose a workflow,
but it cannot grant itself authority, bypass validation, or decide that an
unsafe action is permitted.

## 2. Why this is useful

This feature is valuable for users who already run long-lived AI terminal work
and need to:

- leave the computer and continue from a phone;
- coordinate different installed harnesses in one task;
- preserve plans, implementation, review, test, and correction dependencies;
- see which exact worker is waiting for input;
- intervene in a child session without losing the parent workflow; and
- return to the same local process and working context later.

It is not especially useful for a user who runs one short agent request at a
time. Native Codex, Claude Code, Gemini CLI, OpenCode, and existing agent
managers already handle many single-harness or desktop-first cases.

## 3. Market reality and Termlinks' position

This is not a novel category:

- The Codex app manages parallel threads and worktrees while reusing Codex CLI
  projects and configuration.
- Claude Code provides subagents, agent teams, shared tasks, inter-agent
  messaging, agent view, and worktree sessions. Some team behavior remains
  experimental and has documented resume, coordination, and shutdown limits.
- Gemini CLI and OpenCode support discoverable/configurable subagents and `@`
  routing inside their own ecosystems.
- AgentOS, AGX, Daintree, Hivemind, and other open-source projects already
  launch several local coding harnesses, manage sessions/worktrees, and expose
  various desktop, mobile, terminal, or remote-control interfaces.
- Codex Orchestrator already demonstrates cross-Codex/Claude
  plan-implement-review pipelines, worktree isolation, and human approval.

Termlinks should therefore compete on its existing strengths instead of feature
breadth:

1. Phone-first supervision and input.
2. The same daemon-owned session visible remotely and locally.
3. A provider-neutral adapter layer using each user's own local configuration.
4. No mandatory hosted account or hosted source-code copy.
5. Application-layer encryption through an untrusted relay.
6. Ordinary terminal, file-transfer, and optional desktop control beside AI
   workflow control.
7. Clean cross-device handoff to the exact workflow and child session.

## 4. Non-goals

The first implementation will not:

- support an arbitrary unknown executable without an adapter;
- infer that every installed executable is authenticated;
- scrape provider credential files or send credentials to the portal;
- scan the user's entire disk or shell history for projects;
- silently merge generated work into the user's main branch;
- promise that a logical worktree boundary is an operating-system sandbox;
- preserve a live PTY across a computer reboot;
- replicate a full IDE, code editor, issue tracker, or infinite canvas;
- allow agents to create unbounded nested teams or correction loops; or
- make cloud-backed AI inference local. Only orchestration and state are local.

## 5. User model

### Simple task

```text
@codex fix the reconnect bug and run the tests
```

This creates one workflow with one directed worker stage.

### Dependent multi-agent task

```text
@codex inspect the project and create an implementation plan.

@claude implement the Codex plan.

After implementation, use three high-reasoning @codex reviewers for security,
correctness, and tests. Send findings back to @claude. Finish after all required
reviews approve and the final tests pass.
```

This becomes an explicit dependency graph:

```text
Codex plan
    |
Claude implementation
    |
    +-- Codex security review ----+
    +-- Codex correctness review -+--> decision
    +-- Codex test review --------+
                                      |
                         changes requested?
                            | yes       | no
                            v           v
                       Claude fix    final tests
                            |           |
                            +-- review -+--> complete
```

The user sees and may edit this graph before execution when it has multiple
write stages, elevated permissions, more than two workers, or a feedback loop.

## 6. Architecture

```text
Mobile PWA / browser / local TUI
              |
      authenticated command
              |
      Termlinks coordinator
        |       |         |
        |       |         +-- workflow/event database
        |       +------------ workspace/worktree manager
        +-------------------- agent adapter registry
                                  |
                    +-------------+-------------+
                    |                           |
               ACP driver                  PTY driver
          structured agent protocol     interactive CLI fallback
                    |                           |
              compatible agent            Codex/Claude/etc.
```

### Coordinator

The coordinator runs inside the local Termlinks daemon and owns:

- workflow validation and state transitions;
- stage dependencies and readiness;
- global and per-project concurrency limits;
- worker start, cancel, retry, and timeout decisions;
- approval and user-input queues;
- local event persistence and replay;
- worktree creation, locking, validation, and cleanup;
- structured artifact handoff;
- controller leases for raw terminal input; and
- final completion evaluation.

The scheduler must not depend on an AI response to preserve invariants.

### AI-assisted workflow compiler

Explicit mention blocks are parsed deterministically first. Complex natural
language may be sent to the configured local planner agent to propose a
`WorkflowSpec`. The proposal is treated as untrusted input and must pass a local
schema and policy validator before it can run.

The compiler may infer dependencies, roles, and review loops. It may not infer
new filesystem roots, credentials, elevated permission, automatic merging, or
unbounded concurrency.

### Adapter registry

Each supported harness implements a capability-based adapter:

```text
Identity        stable id, display name, executable
Discovery       PATH probe and safe version command
Readiness       documented status/auth handshake when available
Capabilities    prompt, resume, cancel, modes, effort, attachments, structured output
Launch          exact argv; never shell-string interpolation
Input           structured prompt or PTY bytes
Events          output, plan, tool call, permission request, waiting, completion
Recovery        resume id and supported restart behavior
Security        native sandbox/approval flags and their actual guarantees
```

Use the Agent Client Protocol (ACP) first when a compatible implementation or
adapter exists. ACP provides structured initialization, authentication,
session creation/loading, prompts, progress updates, tool calls, permission
requests, cancellation, and terminal operations. Structured protocol events are
more reliable than screen scraping.

Use the existing Termlinks PTY driver as a compatibility fallback. PTY adapters
must use harness-specific start, prompt, readiness, and resume behavior. Do not
pretend an unsupported capability exists.

Initial supported adapters:

1. Codex
2. Claude Code
3. ACP-compatible generic agent

Next adapters:

4. Gemini CLI
5. OpenCode
6. Aider/provider combinations
7. User-defined adapter manifests with strict validation

## 7. Discovery

Discovery runs locally and never returns tokens or secret configuration values.

Agent states:

```text
unsupported
not_installed
installed_unknown_auth
login_required
ready
temporarily_unavailable
```

Rules:

- Search only the daemon's effective executable path plus explicitly configured
  executable locations.
- Resolve and store the canonical executable path and version.
- Use documented, non-interactive readiness/authentication methods.
- Do not launch a paid model request merely to label an agent `ready`.
- Do not parse or copy raw token files.
- Cache results briefly and allow an explicit refresh.
- Detect capability changes per version; unknown versions use conservative
  capabilities.
- Explicit `@agent` selection fails clearly if that exact adapter is not ready.
- `Auto` may choose another ready compatible adapter using local preferences.

## 8. Workspace suggestions

Do not crawl the entire home directory or parse shell history by default.

Rank suggestions from:

1. Current workflow or terminal directory.
2. Running and recently used Termlinks session directories.
3. Previously approved Termlinks projects.
4. Git repositories found under explicitly configured roots with bounded depth,
   count, time, and ignored-directory rules.
5. Manual local directory browsing within approved roots.

The portal receives display-safe names and opaque/canonical paths only after
authentication. Public relay infrastructure sees ciphertext.

## 9. Local state

Authoritative runtime state stays outside repositories in the existing private
Termlinks state directory:

```text
state/
├── coordinator.db
├── control.sock
├── auth.token
├── settings.json
├── cloud.json
├── artifacts/<workflow-id>/
└── worktrees/<project-id>/<workflow-id>/
```

Use SQLite in WAL mode with restrictive file permissions and versioned schema
migrations. Core entities:

```text
agents
projects
workflows
stages
stage_dependencies
worker_sessions
events
artifacts
approvals
input_requests
controller_leases
workspace_locks
```

Every mutating client request carries a unique idempotency key. The event stream
uses monotonically increasing per-workflow sequence numbers so a reconnecting
device requests a snapshot plus events after its last observed sequence.

An optional project `.termlinks/` contains only safe, shareable configuration:

```text
.termlinks/
├── project.yaml
└── workflows/*.yaml
```

It may define test commands, allowed project-relative paths, reusable roles,
and completion gates. Runtime events, credentials, absolute private paths, and
provider configuration never belong there.

## 10. Workflow and stage states

Workflow states:

```text
draft
awaiting_approval
queued
running
waiting_for_user
reviewing
revising
completed
failed
cancelled
```

Stage states:

```text
pending
ready
starting
running
waiting_for_user
succeeded
changes_requested
stale
failed
cancelled
```

Only the coordinator performs transitions. Worker output requests a transition;
it does not directly mutate workflow state.

Completion gates may include:

- required stages succeeded;
- required reviewer verdicts approved;
- configured commands exited successfully;
- no unresolved input or approval requests;
- correction-loop count below its maximum; and
- candidate branch clean and based on the expected revision.

## 11. Concurrency and isolation

Recommended defaults:

- two active AI worker turns globally;
- one write-capable stage per workflow;
- one integration operation per repository;
- up to three read-only reviewers when explicitly requested;
- maximum three review/correction cycles;
- bounded stage duration and output; and
- queued overflow instead of uncontrolled process creation.

Different repositories may run concurrently. Write-capable stages in the same
repository receive separate Git worktrees and branches. Read-only planning and
review may use a read-only view of the correct base or candidate worktree.

Only the coordinator integrates branches. Default completion produces a
reviewed candidate branch and diff; it does not merge or push. Conflicts pause
for an explicit resolution stage or user decision.

Non-Git directories serialize write access in the first release. Snapshot/copy
isolation may be added later.

## 12. Artifact and context handoff

Stages exchange bounded structured artifacts instead of complete terminal
scrollback:

```text
plan            Markdown plus declared acceptance criteria
implementation  branch, base revision, commit(s), diff summary, changed files
review          verdict, severity, findings, evidence, requested changes
test            commands, exit codes, bounded logs, environment metadata
decision        accepted/rejected/stale with reason
```

The coordinator constructs each worker prompt from:

- the user's relevant instruction block;
- the stage role and permissions;
- required upstream artifacts;
- project instructions already loaded by that harness; and
- explicit completion/output requirements.

Repository content and worker output are untrusted data. They cannot introduce
new coordinator instructions merely by containing `@agent` text or a workflow
fragment.

## 13. User inspection and intervention

The mobile **Work** surface shows parent team rooms. The implemented room screen
shows the human and agent participants, durable shared messages, exact agent
turn status, replies, and links to the corresponding terminal. Planned artifact,
diff, permission, and test summaries remain later-phase work.

The current room provides:

- a readable human/agent conversation;
- a shared `@team` composer that adds context without scheduling inference;
- direct `@agent` messages and replies that queue safe follow-up turns;
- exact raw terminal links for agent messages; and
- on-demand native Show/Hide controls for live PTYs.

Changed-file/diff summaries, structured permission requests, pause/retry, and a
controller lease for taking over a live PTY remain planned work.

Structured user input is preferred. It is stored as a local room message. A
direct message to an agent becomes or joins a queued follow-up turn; a shared
message is supplied to later scheduled turns. Dependency invalidation and stale
downstream-stage propagation are not implemented yet.

Raw terminal control uses an expiring single-controller lease. Other devices
remain viewers. Returning control to the agent creates a recorded event and the
adapter determines whether the turn can continue or must be restarted.

## 14. Cross-device continuity

The local coordinator is the source of truth. Browser storage is only a cache
of non-secret view preferences, drafts, and the existing non-exportable
authentication key.

Shared state:

- workflows, stages, workers, events, artifacts, approvals, questions;
- current process and PTY output;
- selected project and authoritative completion state; and
- controller ownership.

Device-local presentation state:

- scroll position;
- mobile keyboard visibility;
- panel dimensions; and
- optional last-opened tab preference.

Closing the phone or native terminal only detaches that viewer. Reconnecting
loads a snapshot and missing events and attaches to existing worker sessions.

Add a local TUI command:

```text
termlinks work
termlinks work <workflow-id-or-name>
```

It attaches to the same parent workflow and lets the user navigate child
workers. It must never start duplicate workers. `termlinks attach <session-id>`
continues to expose an exact PTY-backed child session.

## 15. Restart and recovery

Browser, relay, connector, and native-viewer restarts must not affect local
workers.

A daemon restart cannot preserve an in-memory PTY. Before restart, persistent
workflow metadata and adapter resume identifiers allow best-effort recovery:

- resumable structured agent: load the existing session;
- resumable CLI agent: start with its documented resume identifier;
- non-resumable worker: mark interrupted and offer a new stage using saved
  artifacts and context;
- worktree and commits remain intact regardless of process recovery.

Never report an exact session as resumed when a replacement session was
created.

## 16. Protocol and API changes

Reuse the existing authenticated direct API and encrypted generic HTTP bridge
where possible. Add versioned local APIs:

```text
GET    /api/agents
POST   /api/agents/refresh
GET    /api/projects/suggestions
POST   /api/workflows/compile
POST   /api/workflows
GET    /api/workflows
GET    /api/workflows/{id}
POST   /api/workflows/{id}/messages
POST   /api/workflows/{id}/pause
POST   /api/workflows/{id}/resume
POST   /api/workflows/{id}/cancel
POST   /api/stages/{id}/input
POST   /api/stages/{id}/approve
POST   /api/stages/{id}/retry
POST   /api/stages/{id}/take-control
POST   /api/stages/{id}/release-control
```

Add an encrypted workflow subscription message carrying snapshot/event data.
Cloudflare continues to route opaque ciphertext and must not gain a workflow
database, task parser, agent registry, or credential knowledge.

All objects and messages have explicit size limits. Event streams are bounded
and reconnectable. Large logs remain local artifacts and are fetched in bounded
pages.

## 17. Security requirements

1. Treat the portal token as full local-user authority until per-device
   identities exist.
2. Never use `sh -c` or concatenate user text into a command string.
3. Launch only canonical executables registered by validated adapters.
4. Keep provider secrets in provider-owned local configuration.
5. Make actual sandbox guarantees visible: `OS-enforced`, `harness-enforced`, or
   `not enforced`.
6. Keep planning/review stages read-only wherever the harness supports it.
7. Require approval for `sudo`, new filesystem roots, destructive Git actions,
   network expansion, automatic merge/push, or permission escalation.
8. Cap workers, loops, duration, input/output sizes, artifacts, and disk use.
9. Validate every state transition and treat agent output as untrusted.
10. Record local audit events without storing credentials.
11. Avoid claiming a readiness/auth state that the adapter cannot prove.
12. Ensure existing terminal, desktop, file-transfer, and E2E security tests
    continue to pass unchanged.

Before enabling workflows for anyone beyond one trusted owner, implement
per-device credentials, revocation, scoped permissions, and an audit viewer.

## 18. Implementation phases

### Phase 0: architecture contract

Deliver:

- workflow/stage/event schemas;
- coordinator state-machine specification;
- adapter interface and capability matrix;
- threat model and privacy model;
- SQLite migration and backup policy; and
- protocol versioning decision.

Acceptance:

- invalid dependency cycles, unknown agents, excessive workers, permission
  escalation, and path escape fail before any process starts;
- existing sessions and connector protocol remain backward compatible.

### Phase 1: discovery and project suggestions

Deliver:

- local adapter registry;
- Codex, Claude, and generic ACP discovery;
- conservative readiness states;
- approved project roots and bounded Git-repository index;
- authenticated agent and workspace APIs; and
- portal `@` and directory suggestions.

Acceptance:

- no secret file contents enter logs, API responses, or relay packets;
- discovery performs no paid inference call;
- unavailable/unknown states are accurate and actionable.

### Phase 2: durable single-worker workflows

Status: partially implemented. Local SQLite state, PTY-backed stages, team-room
list/detail UI, durable human replies, safe follow-up turns, cancellation, and
truthful restart interruption are present. Pause/retry, idempotency keys, and
provider-native resume remain.

Deliver:

- local database and migrations;
- parent workflow records and event replay;
- one stage backed by ACP or the existing PTY manager;
- workflow list/detail UI;
- direct worker follow-up and exact terminal view;
- pause/cancel/retry; and
- reconnect idempotency.

Acceptance:

- create on phone, disconnect, reconnect, and continue the exact live worker;
- duplicate client requests cannot create duplicate workers;
- daemon restart truthfully resumes or marks the worker interrupted.

### Phase 3: directed dependent workflows

Status: partially implemented. The deterministic mention parser, sequential
dependency scheduling, bounded room transcript, agent handoff/question display,
and explicit human follow-up are present. Editable preview, typed artifacts,
stale propagation, and true pause-for-user semantics remain.

Deliver:

- deterministic mention-block parser;
- optional AI workflow proposal compiler;
- editable workflow preview;
- dependency scheduling;
- structured plan, implementation, review, and test artifacts; and
- waiting-for-user questions and notification state.

Acceptance:

- `Codex plan -> Claude implement -> Codex review -> final test` works against
  fake deterministic agents without timing assumptions;
- editing an upstream instruction marks affected downstream stages stale.

### Phase 4: worktree isolation and bounded review loops

Status: not implemented. The current scheduler instead allows at most two
active workflows and rejects concurrent workflows resolving to the same Git
repository.

Deliver:

- repository identity and locking;
- worktree/branch lifecycle;
- parallel read-only reviewers;
- verdict validation;
- correction feedback loops;
- candidate diff/branch UI; and
- conflict and cleanup handling.

Acceptance:

- two concurrent workflows cannot modify the same checkout;
- failed/rejected work remains inspectable and recoverable;
- no automatic merge or push occurs without configured authority.

### Phase 5: complete intervention and native handoff

Deliver:

- per-worker controller leases;
- `Take terminal control` and release behavior;
- local `termlinks work` TUI;
- one native workflow workspace rather than uncontrolled window creation;
- exact child attach; and
- browser/desktop handoff tests.

Acceptance:

- only one viewer writes raw PTY input at a time;
- moving phone -> desktop -> phone never creates a duplicate agent;
- every structured user intervention appears in workflow history.

### Phase 6: adapter expansion and reusable workflows

Deliver:

- Gemini CLI, OpenCode, and Aider adapters;
- versioned user-defined adapter manifests;
- optional safe `.termlinks/project.yaml` and workflow templates;
- adapter diagnostics; and
- configurable local scheduler limits.

Acceptance:

- unsupported capability requests fail explicitly rather than degrading
  silently;
- project configuration contains no runtime secrets or private global state.

### Phase 7: multi-user hardening, only if the product expands

Deliver:

- device pairing and scoped identities;
- revocation;
- per-project and per-action permissions;
- audit viewer; and
- optional multi-computer routing.

This phase is required before describing Termlinks AI workflows as safe for
shared accounts or teams.

## 19. Test strategy

### Unit

- workflow schema and cycle validation;
- state transitions and stale propagation;
- scheduler fairness and concurrency caps;
- adapter capability negotiation;
- mention parsing and target resolution;
- path/root validation;
- idempotency and event sequencing;
- controller lease expiry; and
- secret redaction.

### Integration

- deterministic fake ACP agent;
- deterministic fake PTY harness;
- plan/implement/review/fix/test pipeline;
- user-input and permission waits;
- adapter crash, timeout, malformed output, and retry;
- Git worktree isolation, conflicts, cleanup, and disk limits;
- database migration, backup, corruption detection, and restart recovery; and
- simultaneous browser/local viewers.

### End-to-end

- direct local portal;
- encrypted Cloudflare portal carrying only ciphertext;
- phone disconnect/reconnect during every stage state;
- duplicate submission under reconnect;
- raw-terminal control handoff;
- no regression to ordinary Termlinks terminals, attachments, desktop control,
  update, or existing active PTYs; and
- macOS and Linux release builds.

Real provider accounts are opt-in manual tests. Automated CI must use fake
agents and must not consume user subscriptions or require provider secrets.

## 20. Rollout

1. Ship behind a disabled-by-default `workflows.preview` setting.
2. Keep ordinary Termlinks terminal behavior unchanged.
3. Run database migrations before enabling the feature and retain a recoverable
   backup.
4. Mark every adapter and capability as preview until tested against supported
   versions.
5. Enable only for a single trusted owner during the first release.
6. Collect local diagnostics only when explicitly exported by the user; add no
   hosted telemetry requirement.

## 21. Go/no-go gates

Proceed only if Phase 2 proves exact live-worker continuity across phone and
desktop without duplicate processes. Do not proceed to autonomous correction
loops until worktree isolation, idempotency, and stage-state validation are
proven.

The project should stop or narrow scope if it becomes primarily a full IDE or a
clone of existing desktop agent managers. The differentiator must remain secure
remote continuity and precise human supervision of the user's real local agent
sessions.

## References

- [Introducing the Codex app](https://openai.com/index/introducing-the-codex-app/)
- [Claude Code agent teams](https://code.claude.com/docs/en/agent-teams)
- [Claude Code parallel agents and worktrees](https://code.claude.com/docs/en/agents)
- [Gemini CLI subagents](https://geminicli.com/docs/core/subagents/)
- [OpenCode agents](https://opencode.ai/docs/agents/)
- [Agent Client Protocol overview](https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/protocol/v1/overview.mdx)
- [AgentOS](https://github.com/saadnvd1/agent-os)
- [AGX](https://github.com/nashory/agx)
- [Daintree](https://github.com/daintreehq/daintree)
- [Hivemind](https://github.com/dip497/hivemind)
- [Codex Orchestrator](https://github.com/zm2231/codex-orchestrator)
