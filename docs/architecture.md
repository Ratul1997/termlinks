# Architecture and data flow

## Components

### `termlinks` CLI

The same Go executable acts as the user command, local attach client, and daemon. The CLI sends commands through a private Unix socket rather than spawning the target as its own child. If the daemon is not available, the CLI starts a detached copy and waits for its health endpoint.

### Daemon and PTY manager

The daemon owns each pseudo-terminal and child process. A session stores identifiers and process metadata, a bounded output ring, current dimensions, subscribers, and final exit state. The manager retains at most 64 sessions and prunes the oldest completed entries as new sessions are created.

The daemon exposes two deliberately separate surfaces:

- Local control API over the `0600` Unix socket: create, list, attach, resize, input, and stop.
- Browser portal over TCP: authenticate, create an interactive shell, list, attach, resize, input, and stop. Browser creation deliberately accepts only a name and starting directory; commands are typed into the shell afterward.

### Browser portal

The portal is a mobile-first static TypeScript application embedded into the Go binary. xterm.js interprets ANSI terminal output and captures keyboard input. Extra mobile keys provide Escape, Tab, Ctrl-C, Ctrl-D, arrows, and Enter.

### Cloud bridge

The optional public path consists of Cloudflare Pages, a Pages Function, a Worker with one Durable Object, and an outbound connector inside the same Go executable. The browser and connector derive the same AES-256-GCM key from the 256-bit portal token. The Worker routes ciphertext by a random channel ID but never receives the key or plaintext.

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

An authenticated browser may instead send `{name, cwd}`. The daemon resolves the user's normal shell, creates it on a PTY through the private Unix control socket, and returns the new session ID. The browser immediately attaches, after which `cd`, `ls`, and all other commands are ordinary terminal keystrokes. Closing the page does not terminate that shell.

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

## Encrypted public attachment flow

```text
Phone browser                 Cloudflare                    Local computer
     │                            │                               │
     │ derive key from token      │                               │ derive same key from token file
     │ WSS /ws/bridge ───────────►│ Pages → Worker → Durable Obj  │
     │◄── random channel ID ──────│                               │
     │                            │── channel-open metadata ─────►│
     │── AES-GCM(auth challenge) ─┼── opaque ciphertext ─────────►│ decrypt + local login
     │◄─ AES-GCM(auth proof) ─────┼── opaque ciphertext ──────────│
     │                            │                               │
     │── encrypted create/list/input ────────────────────────────►│ approved request / Unix socket / PTY write
     │◄─ encrypted metadata/output┼───────────────────────────────│
```

The AES-GCM additional authenticated data includes the random relay channel ID, sender direction, and monotonic sequence number, preventing ciphertext from being moved between connections, reflected, reordered, or replayed. Every packet also uses a new 96-bit random nonce. Cloudflare can see endpoints, IP addresses, timing, packet sizes, channel IDs, and online state, but not the portal token, cookies, commands, session metadata, terminal output, or keystrokes.

The public flow asks for the portal token after each page load and retains only a non-extractable Web Crypto key in page memory. The connector performs the actual local cookie login using its local token file. A wrong token cannot decrypt the connector's challenge response.

## Disconnect behavior

- Local terminal closes: its socket disappears; the daemon and PTY continue.
- Phone locks or changes network: its WebSocket disappears; the PTY continues.
- Phone reconnects: retained scrollback is sent first, followed by live bytes.
- Child exits: the daemon records the exit code and retained output remains viewable.
- Daemon/computer stops: in-memory sessions end. Persistence across restart is outside this MVP.
