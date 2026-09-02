# Termlinks

Termlinks keeps terminal work running on your computer and lets you view and control it from a phone browser. It is command-agnostic: Codex, Claude, development servers, import scripts, shells, and other terminal programs all use the same PTY bridge.

This first version is for one person and supports macOS and Linux. It controls sessions started through Termlinks; it does not take over unrelated Terminal/iTerm windows.

## Quick start

Requirements: Go 1.22+, Node.js 20+, and npm.

```sh
npm install
make install
termlinks token
termlinks codex
```

The first managed command starts the background daemon automatically. Open `http://127.0.0.1:8787` on the computer and log in with the token. The compiled, self-contained executable is `dist/termlinks`; its portal assets are embedded in the binary.

## Important commands

Generate or display your private portal login token:

```sh
termlinks token
```

Start a managed command and keep it visible in the current terminal:

```sh
termlinks codex
termlinks claude
termlinks npm run dev
termlinks python import.py
termlinks bash
```

Give a session a recognizable name:

```sh
termlinks -n api -- npm run dev
```

Start a command in the background without attaching the current terminal:

```sh
termlinks -d -- python long_import.py
```

List all managed sessions and their IDs:

```sh
termlinks list
```

Attach a local terminal to an existing session:

```sh
termlinks attach <session-id>
```

Stop a managed session:

```sh
termlinks stop <session-id>
```

`termlinks list` shows both running and finished sessions. You can use the short ID displayed in that list with `attach` or `stop`.

Start the portal daemon manually in the foreground:

```sh
termlinks daemon
```

Check the daemon, listening address, state directory, and installed version:

```sh
termlinks doctor
termlinks version
termlinks help
```

Build, test, and install from source:

```sh
npm install
npm test
npm run build
make install
```

`make install` copies the executable to `~/.local/bin/termlinks`. Without installing, replace `termlinks` in the examples with `./dist/termlinks`.

## Managing sessions in the portal

After login, the portal dashboard automatically shows every managed terminal and its current state:

- The header shows the number of running and finished sessions.
- Each card shows the session name, command, directory, runtime, and status.
- Select **Open terminal** to view and type in that terminal.
- Select **Stop & close** to terminate a running command after confirmation.
- The terminal screen also has **Stop & close session** in its `•••` menu.
- Finished sessions remain available for viewing their retained output.

Stopping or closing a session terminates its command. Simply closing the browser or local Terminal window only disconnects that viewer; the managed command continues running.

## Phone access

The default portal listens only on the computer itself. For access away from home, connect the computer and phone to the same private VPN/tailnet, then bind Termlinks to the computer's private VPN address:

```sh
termlinks daemon --listen 100.x.y.z:8787
```

Open `http://100.x.y.z:8787` on the phone. Keep the daemon terminal open the first time; the address is saved, and later managed commands can auto-start the daemon with it.

Do not port-forward Termlinks from your router or expose it directly to the public internet. The application does not provide TLS or a cloud relay in this version. A Tailscale/WireGuard-style private network supplies encrypted transport and device access control.

## What happens when you run a command

```text
Local keyboard ─┐                         ┌─ local terminal display
                │                         │
termlinks CLI ──┴─ Unix socket ─► daemon ├─ WebSocket ─► phone terminal
                                      │   │
                                      ▼   │
                                  owned PTY
                                      │
                                      ▼
                         codex / claude / npm / bash / ...
```

1. `termlinks <command>` asks the local daemon to create a real pseudo-terminal (PTY).
2. The daemon starts and owns the child process. The launching terminal is only an attached client, so disconnecting it does not end the work.
3. PTY output is streamed to every attached local or browser client and retained in a bounded 2 MiB scrollback buffer.
4. Phone keystrokes travel as binary WebSocket messages into the same PTY. Terminal-size changes travel as small JSON control messages.
5. Closing the phone or losing network only detaches that viewer. Reopening the session replays retained output and resumes live streaming.

See [docs/architecture.md](docs/architecture.md) for the component and data-flow details.

## Monorepo layout

```text
apps/backend/   Go daemon, PTY manager, local CLI, HTTP/WebSocket portal
apps/web/       Mobile-first TypeScript portal using xterm.js
scripts/        Reproducible build/test orchestration
dist/           Generated standalone executable (gitignored)
```

Development commands:

```sh
npm run typecheck
npm test
npm run build
npm audit
```

## Current boundary

- Sessions must be launched with `termlinks …`. Existing arbitrary terminal windows cannot be attached after the fact because their PTYs belong to another process.
- This is a text terminal, not screen sharing. Full desktop/GUI control would be a separate remote-desktop subsystem.
- Sessions live in daemon memory and do not survive a daemon or computer restart.
- Browser command creation is deliberately disabled. A phone can attach, type, resize, and stop already-authorized sessions.

Read [SECURITY.md](SECURITY.md) before enabling network access.
