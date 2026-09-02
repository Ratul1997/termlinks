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
- On a desktop browser, drag across terminal text normally to select and copy it. On iPhone/iPad or another touch device, tap **Select text** in the bottom toolbar. Termlinks opens the retained terminal history in a normal read-only text panel, where you can long-press to select exact text and tap **Copy selection**, or use **Copy visible screen**.
- To paste reliably from a phone or installed PWA, tap **Paste** in the bottom toolbar, long-press in the text box, choose the system **Paste** action, and tap **Send to terminal**. Pasted text is sent to the active PTY exactly like keyboard input.
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

Remote desktop is disabled by default. It has two independent switches: macOS **Screen Sharing** produces the desktop framebuffer, and the Termlinks desktop tunnel carries it through the encrypted portal. Both must be enabled locally on the Mac.

### 1. Enable macOS Screen Sharing

The recommended method is **System Settings → General → Sharing → Screen Sharing**. Open its details, restrict access to only the intended macOS users, and configure strong credentials. Apple documents the current controls in its [Screen Sharing setup guide](https://support.apple.com/guide/mac-help/turn-screen-sharing-on-or-off-mh11848/mac).

Alternatively, this command opens the native macOS administrator-authorization dialog and enables the system Screen Sharing service:

```sh
osascript -e 'do shell script "/bin/launchctl load -w /System/Library/LaunchDaemons/com.apple.screensharing.plist" with administrator privileges'
```

The password is entered into the macOS dialog; Termlinks does not receive it. Review the allowed users afterward in System Settings. If ordinary macOS account authentication does not work with the browser viewer, configure the VNC-viewer password option shown by macOS and use a strong, unique password.

### 2. Enable the Termlinks tunnel

```sh
termlinks desktop enable --address 127.0.0.1:5900
termlinks desktop status
termlinks cloud status
```

The expected desktop status is `Tunnel: enabled` and `VNC server: reachable`. The cloud status should say the connector is running and the computer is online.

### 3. Connect from the portal

1. Open **https://termlinks.pages.dev**, enter the portal token, and select **Remote desktop**.
2. Enter the credentials requested by the Mac. They are used for that connection and are not written to Termlinks configuration or browser storage.
3. The page starts in view-only mode. Tap **Enable control** before sending touch, mouse, or keyboard input.
4. Use the toolbar for fullscreen, scaling, special keys, the mobile keyboard, and clipboard text sent to the Mac.

The default target is `127.0.0.1:5900`. A different local VNC server can be selected with `termlinks desktop enable --address 127.0.0.1:<port>`. Non-loopback targets are rejected. The VNC password is entered into the browser only when requested and is never written to Termlinks configuration or browser storage.

The viewer supports full-viewport scaling, fullscreen where the browser permits it, touch gestures, mouse input, hardware keyboards, an on-screen text/special-key panel, and clipboard text sent to the Mac. On iPhone/iPad, installing the PWA from **Share → Add to Home Screen** gives the largest persistent app view.

The remote desktop is a video-like canvas of pixels, so its displayed text cannot be selected as normal webpage text. Select text inside the remote Mac application and use the remote clipboard controls where supported. This is separate from managed terminal pages, whose **Select text** panel provides reliable browser/PWA copying of retained terminal output.

This version tunnels the framebuffer exposed by the local VNC server. It does not yet provide a Termlinks-native picker for one physical monitor or one application window. Which display layout is exposed depends on the local Screen Sharing/VNC server.

### Disable or revoke remote desktop

Disable the Internet-facing Termlinks tunnel without affecting terminal sessions:

```sh
termlinks desktop disable
```

That command restarts only the cloud connector; managed terminal sessions and the terminal daemon continue running. It does **not** turn off macOS Screen Sharing, which may still be reachable from the local network.

To also turn off the macOS Screen Sharing service, use System Settings or run:

```sh
osascript -e 'do shell script "/bin/launchctl unload -w /System/Library/LaunchDaemons/com.apple.screensharing.plist" with administrator privileges'
```

### Remote desktop troubleshooting

- **Tunnel disabled:** run `termlinks desktop enable --address 127.0.0.1:5900`.
- **VNC server unreachable:** enable macOS Screen Sharing, then check the local port with `nc -z 127.0.0.1 5900` and rerun `termlinks desktop status`.
- **Computer offline:** run `termlinks cloud start` and check `termlinks cloud status`. The connector is outbound-only; do not open or forward port 5900 on the router.
- **PWA appears outdated:** fully close and reopen it. If necessary, reload `https://termlinks.pages.dev` in the browser or remove and add the Home Screen app again.
- **Connection stops while away:** the Mac must remain powered on, awake, and online. Terminal processes and desktop access are unavailable while it sleeps.

### Remote desktop security notes

- Anyone with the portal token has full control of managed terminals and, while the desktop tunnel is enabled, can attempt to access the GUI. Treat the token like an administrator password and do not send it in chat, screenshots, source code, or logs.
- Termlinks restricts its VNC destination to a loopback address, but enabling macOS Screen Sharing can also expose the service to the Mac's local network. Restrict allowed macOS users, use strong credentials, keep the firewall enabled, and disable Screen Sharing when it is not needed.
- The portal token derives the AES-256-GCM bridge key in the browser. Cloudflare carries encrypted terminal and desktop payloads and cannot read their contents, although normal connection metadata remains visible.
- VNC credentials are supplied directly to the in-browser VNC client for the live connection. Termlinks does not save them.
- View-only mode prevents accidental input in the UI; it is a safety control, not an authentication boundary.

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
