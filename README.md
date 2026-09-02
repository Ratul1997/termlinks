# Termlinks

Termlinks keeps terminal work running on your computer and lets you view and control it from a phone browser. It is command-agnostic: Codex, Claude, development servers, import scripts, shells, and other terminal programs all use the same PTY bridge. It can also carry an opt-in full Mac desktop through the encrypted portal.

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

This personal deployment is also available at **https://termlinks.pages.dev** while the computer and cloud connector are online.

The portal is an installable PWA. On Android/desktop, use the browser's **Install app** action (or the in-app button when available). On iPhone/iPad, open the Share menu and select **Add to Home Screen**. The installed app still asks for the portal token and still requires the computer and connector to be online for terminal access.

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

Control the outbound cloud connection:

```sh
termlinks cloud start
termlinks cloud status
termlinks cloud stop
```

Control the optional remote desktop tunnel:

```sh
termlinks desktop enable
termlinks desktop status
termlinks desktop disable
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

- Select **New terminal** to create and immediately open a normal interactive shell. Its optional starting directory may be `~`, `~/path`, or an absolute path.
- Inside that shell, type `cd`, `ls`, `codex`, `npm run dev`, or any other command exactly as in a desktop terminal.
- Terminal history uses native touch momentum on mobile and short smooth scrolling for mouse wheels and trackpads.
- The header shows the number of running sessions. Finished and explicitly closed sessions are removed from the dashboard automatically.
- Each card shows the session name, command, directory, runtime, and status.
- Select **Open terminal** to view and type in that terminal.
- Select **Stop & close** to terminate a running command after confirmation.
- The terminal screen also has **Stop & close session** in its `•••` menu.
- While a terminal is open, its final output remains visible after the process exits. Returning to the dashboard clears that finished card.

Stopping or closing a session terminates its command. Simply closing the browser or local Terminal window only disconnects that viewer; the managed command continues running.

## Phone access

For this personal deployment:

1. Run `termlinks cloud start` once on the computer.
2. Open **https://termlinks.pages.dev** on the phone or another computer.
3. Enter the token printed by `termlinks token`.
4. Select an existing session, or tap **New terminal** to create an interactive shell, and type with the device keyboard.

Nothing needs to be installed on the viewing device. Giving another person the portal URL and portal token gives them the same full terminal access, so share it only with someone you completely trust. The connector secret is separate and must never be shared.

If the computer sleeps, shuts down, loses internet access, or runs `termlinks cloud stop`, the public portal reports the computer as offline. Running terminal processes also pause while macOS is asleep.

## Remote desktop (macOS-first)

Remote desktop is disabled by default and must be enabled locally on the Mac. Termlinks does not change macOS privacy or sharing settings for you.

1. On the Mac, open **System Settings → General → Sharing** and enable **Screen Sharing**.
2. Open the Screen Sharing details, restrict allowed users, and enable VNC viewer access with a strong, unique password if macOS offers that option. Apple documents the current controls in its [Screen Sharing setup guide](https://support.apple.com/guide/mac-help/turn-screen-sharing-on-or-off-mh11848/mac).
3. Enable the Termlinks loopback tunnel:

   ```sh
   termlinks desktop enable
   termlinks desktop status
   ```

4. Open **Remote desktop** in the deployed portal. Enter the Screen Sharing credentials requested by the Mac. The page starts in view-only mode; tap **Enable control** to allow touch, mouse, and keyboard input.

The default target is `127.0.0.1:5900`. A different local VNC server can be selected with `termlinks desktop enable --address 127.0.0.1:<port>`. Non-loopback targets are rejected. The VNC password is entered into the browser only when requested and is never written to Termlinks configuration or browser storage.

The viewer supports full-viewport scaling, fullscreen where the browser permits it, touch gestures, mouse input, hardware keyboards, an on-screen text/special-key panel, and clipboard text sent to the Mac. On iPhone/iPad, installing the PWA from **Share → Add to Home Screen** gives the largest persistent app view.

This version tunnels the framebuffer exposed by the local VNC server. It does not yet provide a Termlinks-native picker for one physical monitor or one application window. Which display layout is exposed depends on the local Screen Sharing/VNC server.

To revoke GUI access immediately:

```sh
termlinks desktop disable
```

That setting restarts only the cloud connector; managed terminal sessions and the terminal daemon continue running.

### Private-network alternative

The default portal listens only on the computer itself. For access away from home, connect the computer and phone to the same private VPN/tailnet, then bind Termlinks to the computer's private VPN address:

```sh
termlinks daemon --listen 100.x.y.z:8787
```

Open `http://100.x.y.z:8787` on the phone. Keep the daemon terminal open the first time; the address is saved, and later managed commands can auto-start the daemon with it.

Do not port-forward Termlinks from your router or expose its local port directly to the public internet. The Cloudflare connector makes an authenticated outbound TLS connection; a Tailscale/WireGuard-style private network remains the lower-complexity alternative.

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

For public phone access, the browser derives an AES-256-GCM key from the portal token and opens one encrypted bridge through `termlinks.pages.dev`. Pages and the Worker relay only ciphertext; the local connector decrypts approved requests and terminal data, then talks to `127.0.0.1:8787`. The portal token itself never crosses the network, and the local port is never publicly bound.

Remote desktop uses the same bridge. Browser-side noVNC speaks the RFB/VNC protocol through an in-memory WebSocket-like channel; Termlinks encrypts each byte chunk and the connector forwards it only to the configured loopback VNC address. There is no public VNC port or generic TCP proxy.

1. `termlinks <command>` asks the local daemon to create a real pseudo-terminal (PTY).
   The authenticated portal can also request a new interactive shell; it cannot submit a hidden one-shot command or custom environment.
2. The daemon starts and owns the child process. The launching terminal is only an attached client, so disconnecting it does not end the work.
3. PTY output is streamed to every attached local or browser client and retained in a bounded 2 MiB scrollback buffer.
4. Phone keystrokes travel as binary WebSocket messages into the same PTY. Terminal-size changes travel as small JSON control messages.
5. Closing the phone or losing network only detaches that viewer. Reopening the session replays retained output and resumes live streaming.

See [docs/architecture.md](docs/architecture.md) for the component and data-flow details.

## Monorepo layout

```text
apps/backend/   Go daemon, PTY manager, local CLI, HTTP/WebSocket portal
apps/relay/     Cloudflare Worker + Durable Object outbound relay
apps/web/       Mobile-first TypeScript portal using xterm.js and noVNC
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

See [docs/cloudflare.md](docs/cloudflare.md) for deployment, connector-secret rotation, and public smoke-test commands.

## Current boundary

- Only sessions managed by Termlinks can be opened. Launch them with `termlinks …` or create an interactive shell from the portal; unrelated Terminal/iTerm windows cannot be attached after the fact because their PTYs belong to another process.
- Remote desktop is an explicitly enabled, macOS-first subsystem. The terminal features remain usable without it.
- The current desktop viewer shows the framebuffer supplied by the VNC server; native per-monitor and per-window selection are not implemented yet.
- Sessions live in daemon memory and do not survive a daemon or computer restart.
- An authenticated browser can create a normal interactive shell, then type arbitrary commands into it. Treat the portal token as full access to your operating-system user account.
- This personal version uses one shared portal identity. It does not yet provide per-person accounts, per-session permissions, or an audit trail.
- The deployed relay represents one configured computer. Connecting a friend's machine safely requires a separate deployment today; shared multi-device pairing and device selection are not implemented.

Read [SECURITY.md](SECURITY.md) before enabling network access.
