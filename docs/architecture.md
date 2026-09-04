# Architecture and data flow

## Components

### `termlinks` CLI

The same Go executable acts as the user command, local attach client, and daemon. The CLI sends commands through a private Unix socket rather than spawning the target as its own child. If the daemon is not available, the CLI starts a detached copy and waits for its health endpoint.

### Daemon and PTY manager

The daemon owns each pseudo-terminal and child process. A session stores identifiers and process metadata, a bounded output ring, current dimensions, subscribers, and final exit state. The manager retains at most 64 sessions and prunes the oldest completed entries as new sessions are created.

## Local AI workflow coordinator

The optional coordinator remains inside the local daemon. It discovers supported local harness executables, validates an explicitly selected existing directory, compiles ordered `@agent` mentions into deterministic stages, and starts each stage through the same PTY manager used by ordinary Termlinks sessions. Prompts are streamed through PTY input rather than interpolated into a shell command or placed in the child process argument list.

```text
phone AI-work UI
      │ authenticated request (E2E ciphertext on the cloud path)
      ▼
local coordinator ──► private SQLite state/events
      │
      ├──► PTY: Codex ── result ──┐
      ├──► PTY: Claude ◄──────────┤ bounded prior-stage context
      └──► PTY: OpenCode ◄────────┘
                 │
                 └── same live child session available to browser/on-demand native viewer
```

SQLite is the authoritative durable workflow state, while the PTY manager is authoritative for live process state. If the daemon restarts, SQLite marks previously active work `interrupted`; it does not manufacture a replacement or claim the old PTY survived. Concurrent work is capped globally and serialized per canonical Git root until isolated worktree support is implemented.

## Local terminal history

The daemon also owns a separate private SQLite database for terminal history and favorites. It stores bounded names, working directories, opaque session associations, favorite state, and timestamps. It intentionally does not store command arguments, typed input, scrollback, or output. Database files are restricted to mode `0600` inside the mode-`0700` Termlinks state directory.

```text
PTY exit with daemon timestamp
          │
          ▼
local terminal-history.db ── 10 Recent / 100 Favorites
          │
          │ authenticated API; E2E ciphertext on cloud path
          ▼
phone PWA / desktop browser
```

Reconciliation is idempotent and keyed by opaque session identity, so polling never rewrites a completed terminal's close time and two sessions with the same name and directory remain separate. Reopening a saved item updates its stable record with the new active session association. The browser keeps only the current response in memory; logout, another browser, or another authenticated device cannot create a second unsynchronized history database.

The daemon exposes two deliberately separate surfaces:

- Local control API over the `0600` Unix socket: create, list, attach, resize, input, show/hide a managed native viewer, and stop.
- Browser portal over TCP: authenticate, create an interactive shell, list, attach, resize, input, explicitly show/hide a managed native viewer, stop, and manage bounded local terminal history. Browser creation and saved-terminal reopening deliberately accept only a name and starting directory; commands are typed into the shell afterward.

### Browser portal

The portal is a mobile-first static TypeScript application embedded into the Go binary. xterm.js interprets ANSI terminal output and still captures direct keyboard input for full-screen applications. A persistent chat-style composer provides mobile-friendly multiline typing and paste, uses xterm's bracketed-paste path, and submits with Enter; an extra-key row provides Escape, Tab, Ctrl-C, Ctrl-D, arrows, and Enter. The encrypted cloud view also includes noVNC for an opt-in full Mac framebuffer plus a native ScreenCaptureKit picker for one on-screen application window. Both modes support touch/mouse control, keyboard input, and clipboard transfer.

The same app is installable as a PWA from the HTTPS Pages deployment or a trusted loopback origin. Its service worker uses network-first app-shell caching and explicitly bypasses API and WebSocket paths, so terminal/authentication data is never stored in Cache Storage. Native touch overflow provides mobile momentum scrolling; xterm's short animation handles wheel and trackpad scroll events.

### Hosted bridge

The reference public path consists of Cloudflare Pages, a Pages Function, a Worker with one Durable Object, and an outbound connector inside the same Go executable. Cloudflare is the included default adapter, not a daemon dependency. The browser and connector derive the same AES-256-GCM key from the 256-bit portal token. The Worker routes ciphertext by a random channel ID but never receives the key or plaintext.

The same static client auto-detects a directly served daemon through the public, data-free `/api/mode` endpoint. That lets SSH, private-VPN, and generic HTTPS reverse tunnels use the cookie-authenticated direct API without hostname-specific builds. Another provider can also replace Cloudflare while retaining application-layer encryption by implementing the channel relay described below.

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

An authenticated browser may instead send `{name, cwd}`. The connector forwards that request to the daemon's authenticated web API; the daemon resolves the user's normal shell, creates it on a PTY, and returns the new session ID. The browser immediately attaches, while the computer desktop stays unchanged. AI workflow stages follow the same headless-by-default rule.

When the user explicitly chooses **Open on computer**, the authenticated API asks the daemon to launch the platform terminal with a private `termlinks __viewer <session-id>` attachment. A viewer registry changes the session metadata from `hidden` to `opening` to `visible`, rejects late/unrequested viewer connections, and makes repeated show requests idempotent. On macOS the launcher captures the exact Terminal window ID returned by the same AppleScript event that creates it. **Hide on computer** closes only registered managed-viewer sockets and that recorded window, independent of Terminal's close-on-exit preference. Browser sockets and ordinary `termlinks attach` sockets are never registered as managed viewers, so Hide cannot stop the PTY or detach those clients. `Stop` remains a separate session operation. The connector forwards only the narrow validated show/hide routes and never launches a native window itself. `--headless` disables explicit platform launching while retaining browser-managed PTYs.

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
  │◄─ snapshot_start(N) ──│                              │
  │◄── N retained bytes ──│                              │
  │◄── snapshot_end ──────│                              │
  │                       │◄──── bytes from owned PTY ───│
  │◄──── binary bytes ────│                              │
  │──── keyboard bytes ──►│──── write to PTY ───────────►│
  │──── resize JSON ─────►│──── PTY window-size ioctl ──►│
  │◄──── exit status ─────│◄──── process exit ───────────│
```

Only the initial login carries the long-lived token. Later API and WebSocket calls use the temporary browser cookie. Refreshing or reconnecting creates a new viewer of the same daemon-owned PTY; it does not create or restart the command.

The daemon explicitly brackets the complete bounded scrollback with `terminal_snapshot_start` (including its byte count) and `terminal_snapshot_end`; later binary frames are live output. During reconnect, the PWA leaves the old xterm buffer rendered and disables input behind a small status indicator. It assembles and validates the replacement snapshot, resets/replays xterm only when the full snapshot is locally available, restores the user's bottom-or-history position, then applies queued live output in order and re-enables input. This removes the multi-second blank gap and prevents duplicated scrollback. A rolling-upgrade fallback treats the first binary frame from older daemons as the snapshot, matching their existing wire behavior, so the hosted PWA can update before a daemon holding important PTYs is safely restarted.

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

## Encrypted browser-to-computer file flow

```text
Phone / browser             Cloudflare relay                 Local computer
      │ choose file                │                                │
      │── encrypted name/size ────►│── opaque ciphertext ─────────►│ validate + private temp file
      │◄── encrypted ready/ack ────│◄───────────────────────────────│
      │── encrypted 192 KiB chunks, one acknowledged at a time ───►│ ordered write
      │── encrypted finish ───────►│───────────────────────────────►│ fsync + no-overwrite finalize
      │◄── encrypted saved path ───│◄───────────────────────────────│ ~/Downloads/Termlinks Uploads
```

The relay has no upload endpoint and never receives plaintext file data. The connector permits two active uploads per browser channel and a maximum of 100 MiB per file. It rejects path separators, control characters, invalid UTF-8, oversize names, reordered offsets, oversize chunks, and bytes beyond the declared size. Temporary files use mode `0600`, completed names are reserved atomically without overwriting, and partial files are removed after cancellation, error, or channel disconnect.

## Encrypted remote desktop flow

```text
Phone / PWA                 Cloudflare relay                 Local Mac
    │                              │                             │
    │ noVNC RFB client             │                             │ macOS Screen Sharing
    │ desktop_open (AES-GCM) ─────►│── opaque ciphertext ──────►│ connector authorizes feature
    │◄── encrypted RFB bytes ──────│◄────────────────────────────│ TCP 127.0.0.1:5900
    │── encrypted touch/key bytes ►│────────────────────────────►│ mouse / keyboard control
```

Remote desktop reuses the authenticated channel and its direction/sequence/channel-bound AES-256-GCM packets. The relay has no RFB parser and sees no framebuffer, VNC credentials, clipboard text, pointer data, or keystrokes. The connector is not a generic TCP proxy: desktop access must be enabled in local configuration and the only permitted target is a loopback hostname or IP. Each browser channel is limited to one desktop connection.

The VNC authentication conversation remains inside the RFB stream, so its credentials exist only in the browser page and the local Screen Sharing server. Termlinks does not persist them. View-only is the portal's initial UI state, but the portal token remains the true authorization boundary because an authenticated client can request control-capable RFB access.

### Selected-window flow

```text
Phone / PWA                 Cloudflare relay                 Local Mac
    │                              │                             │
    │ encrypted source request ───►│── opaque ciphertext ──────►│ ScreenCaptureKit window list
    │◄── encrypted IDs/titles ─────│◄────────────────────────────│
    │ select one numeric ID ──────►│────────────────────────────►│ independent SCContentFilter
    │◄── encrypted JPEG frames ────│◄────────────────────────────│ bounded screenshots (~6 fps)
    │── encrypted pointer/keys ───►│────────────────────────────►│ CGEvent + Accessibility focus
```

The connector gates this path behind the same local desktop-enabled setting. macOS Screen Recording permission is required before the source list or pixels are available, while Accessibility is required before control events are accepted by the OS. The selected window must be a normal titled on-screen window when enumerated. Only one VNC desktop or one native window capture may be active in a browser channel at a time.

Frames are scaled to a bounded browser-requested maximum, JPEG encoded locally, capped before encryption, and never decoded by Cloudflare. Input validation restricts normalized pointer coordinates, buttons, scroll deltas, known-size browser key codes, UTF-8 text, and clipboard payloads. The native bridge cannot launch commands, choose arbitrary processes, read files, or open network addresses.

## Disconnect behavior

- Local terminal closes: its socket disappears; the daemon and PTY continue.
- Phone locks or changes network: its WebSocket disappears; the PTY continues.
- Phone reconnects: the old terminal stays readable with input paused; an explicitly framed retained-scrollback snapshot replaces it atomically, followed by live bytes. Older daemons use a compatible first-binary-frame fallback.
- Child exits: the daemon records the exit code and retained output remains viewable.
- Dashboard refreshes: completed sessions are omitted, so exited or explicitly stopped terminals do not clutter the portal list.
- Remote desktop viewer closes: its loopback VNC socket is closed; Screen Sharing remains governed by macOS and terminal sessions are unaffected.
- Selected-window viewer closes: its native capture object is released; macOS Screen Recording and Accessibility grants remain until revoked in System Settings.
- `termlinks desktop disable`: the connector restarts with GUI tunneling revoked; the terminal daemon and managed PTYs remain running.
- Daemon/computer stops: in-memory sessions end. Persistence across restart is outside this MVP.
