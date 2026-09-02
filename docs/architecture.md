# Architecture and data flow

## Components

### `termlinks` CLI

The same Go executable acts as the user command, local attach client, and daemon. The CLI sends commands through a private Unix socket rather than spawning the target as its own child. If the daemon is not available, the CLI starts a detached copy and waits for its health endpoint.

### Daemon and PTY manager

The daemon owns each pseudo-terminal and child process. A session stores identifiers and process metadata, a bounded output ring, current dimensions, subscribers, and final exit state. The manager retains at most 64 sessions and prunes the oldest completed entries as new sessions are created.

The daemon exposes two deliberately separate surfaces:

- Local control API over the `0600` Unix socket: create, list, attach, resize, input, and stop.
- Browser portal over TCP: authenticate, list, attach, resize, input, and stop. It cannot create sessions.

### Browser portal

The portal is a mobile-first static TypeScript application embedded into the Go binary. xterm.js interprets ANSI terminal output and captures keyboard input. Extra mobile keys provide Escape, Tab, Ctrl-C, Ctrl-D, arrows, and Enter.

## Session creation flow

```text
User shell
   │  termlinks npm run dev
   ▼
CLI validates cwd, argv, environment, terminal size
   │  POST /v1/sessions over private Unix socket
   ▼
Daemon creates PTY ─► starts child process in requested cwd
   │
   ├─ returns opaque session ID
   └─ begins output capture and bounded scrollback
```

The local CLI then attaches through a WebSocket on the same Unix socket unless `--detach` was supplied.

## Browser attachment flow

```text
Phone                   Daemon                         Child
  │ POST /api/login       │                              │
  │ token + Origin ──────►│ constant-time check          │
  │◄──── HttpOnly cookie ─│                              │
  │                       │                              │
  │ GET /api/sessions ───►│                              │
  │◄──── safe metadata ───│                              │
  │                       │                              │
  │ WS + cookie + Origin ►│                              │
  │◄── retained output ───│                              │
  │                       │◄──── bytes from owned PTY ───│
  │◄──── binary bytes ────│                              │
  │──── keyboard bytes ──►│──── write to PTY ───────────►│
  │──── resize JSON ─────►│──── PTY window-size ioctl ──►│
  │◄──── exit status ─────│◄──── process exit ───────────│
```

Only the initial login carries the long-lived token. Later API and WebSocket calls use the temporary browser cookie. Refreshing or reconnecting creates a new viewer of the same daemon-owned PTY; it does not create or restart the command.

## Disconnect behavior

- Local terminal closes: its socket disappears; the daemon and PTY continue.
- Phone locks or changes network: its WebSocket disappears; the PTY continues.
- Phone reconnects: retained scrollback is sent first, followed by live bytes.
- Child exits: the daemon records the exit code and retained output remains viewable.
- Daemon/computer stops: in-memory sessions end. Persistence across restart is outside this MVP.
