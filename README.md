# Termlinks

Termlinks is an open-source, self-hosted bridge that keeps terminal work running on your computer and lets you view and control it from a phone browser. It is command-agnostic: Codex, Claude, development servers, import scripts, shells, and other terminal programs all use the same PTY bridge. A shell created from the portal also opens in a native terminal window on the computer, so both screens share the same PTY and history. Termlinks can carry an opt-in full Mac desktop or one selected macOS window and can transfer files from the encrypted portal to the computer.

No Termlinks-hosted account or service is required. Run it only on the local computer, reach it through SSH or a private VPN, expose it through an HTTPS tunnel provider, or deploy the included Cloudflare Pages + Workers relay. Cloudflare is the documented default public option, not a requirement.

This first version is designed for one trusted owner and supports macOS and Linux. It controls sessions started through Termlinks; it does not take over unrelated Terminal/iTerm windows.

## Quick start

Requirements: Go 1.24+ (the module selects the tested Go 1.26.8 toolchain), Node.js 20+, and npm.

```sh
git clone https://github.com/Ratul1997/termlinks.git
cd termlinks
npm ci
make install
termlinks token
termlinks codex
```

The first managed command starts the background daemon automatically. Open `http://127.0.0.1:8787` on the computer and log in with the token. The compiled, self-contained executable is `dist/termlinks`; its portal assets are embedded in the binary.

The portal is an installable PWA. On Android/desktop, use the browser's **Install app** action (or the in-app button when available). On iPhone/iPad, open the Share menu and select **Add to Home Screen**. The installed app still asks for the portal token and still requires the computer and connector to be online for terminal access.

## Open source

Termlinks is released under the permissive [MIT License](LICENSE). You may use, inspect, modify, redistribute, and self-host it, including with a network or hosting provider of your choice. The repository contains the Go CLI/daemon, TypeScript PWA, Cloudflare relay, build scripts, tests, architecture notes, and security documentation—there is no required proprietary Termlinks backend.

Third-party dependencies keep their own licenses. Contributions and security reports are welcome; read [SECURITY.md](SECURITY.md) before putting a terminal on any network.

## Choose how to connect

The terminal portal and its API are served by the local `termlinks` daemon. Pick the access method that fits your threat model:

1. **Same computer:** keep the default loopback listener and open `http://127.0.0.1:8787`. Nothing leaves the machine.
2. **SSH port forwarding:** keep Termlinks on loopback and forward it through a host you already trust:

   ```sh
   ssh -N -L 8787:127.0.0.1:8787 your-user@your-computer
   ```

   Then open `http://127.0.0.1:8787` on the SSH client device. A phone needs an SSH client capable of local port forwarding.
3. **Private VPN/overlay:** connect the computer and phone with Tailscale, WireGuard, or another private network, then bind Termlinks to that private address:

   ```sh
   termlinks daemon --listen <private-vpn-ip>:8787
   ```

4. **Any HTTPS tunnel or reverse proxy:** point the provider at `http://127.0.0.1:8787`, preserve WebSocket upgrades, and require HTTPS. This exposes the direct portal through that provider, so the provider terminates TLS and the portal token is the application login boundary. Add the provider's identity/MFA gate when available.
5. **Cloudflare Pages + Workers (default public setup):** deploy the included static PWA, Pages Function, Worker, and Durable Object. The computer makes an outbound connection; no router port is opened. Browser-to-computer terminal and desktop payloads are additionally encrypted with a key derived from the portal token, so the relay carries ciphertext.

The local, SSH, VPN, and direct-tunnel paths provide managed terminals. The native remote-desktop/window picker currently uses the included encrypted connector-relay protocol; use the Cloudflare adapter or implement the same protocol on another provider. See [docs/cloudflare.md](docs/cloudflare.md) for the default deployment and [docs/architecture.md](docs/architecture.md) for the data flow.

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

Update an installed copy to the newest compatible release:

```sh
termlinks update
```

That one command checks the official GitHub Releases feed, selects the build for the computer's operating system and CPU, verifies the published SHA-256 checksum, verifies the downloaded executable's reported version, and replaces the current executable atomically. If the cloud connector was online, Termlinks restarts only that connector. It deliberately does **not** restart the daemon or active terminal sessions, so work in progress stays alive; the running daemon adopts the new executable the next time it is safely restarted.

The command updates the exact executable that invoked it, whether it is `~/.local/bin/termlinks`, `/usr/local/bin/termlinks`, or a standalone copy. The containing directory must be writable by the current user. An administrator-owned installation may require running the same command with appropriate privileges. Source-only or unsupported-platform installations can still update with `git pull && make install`.

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
termlinks desktop permissions
termlinks desktop windows
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

## Complete configuration reference

Termlinks intentionally has a small configuration surface. There is no hidden application `.env` file: every runtime setting, persisted file, cloud binding, and test switch is listed below.

### Local CLI and daemon

| Configuration | Default | Meaning |
| --- | --- | --- |
| `termlinks [--name NAME] [--detach] [--] COMMAND [ARGS...]` | `$SHELL` when no command is supplied | Starts a managed PTY. `-n` aliases `--name`; `-d` aliases `--detach`; `--` ends Termlinks option parsing. |
| Current working directory | Directory where the CLI was invoked | Becomes the managed command's starting directory. |
| Process environment | Current environment; adds `TERM=xterm-256color` only when `TERM` is absent | Passed to locally started managed commands. Keep in mind that a child process can print environment secrets. |
| `SHELL` | `/bin/zsh` for a command-less local launch; `/bin/sh` for a portal-created shell when `SHELL` is absent or not absolute | Selects the interactive shell. |
| `TERMLINKS_STATE_DIR` | Operating-system user config directory plus `termlinks` | Overrides all local state paths. It must be an absolute path. Useful for isolated development/testing installations. |
| `termlinks daemon --listen ADDR` | `127.0.0.1:8787` | Sets and persists the browser portal listener in `settings.json`. Loopback, RFC1918 private addresses, and Tailscale's `100.64.0.0/10` range are accepted by default. |
| `termlinks daemon --allow-public-bind` | Off | Allows an unspecified/public bind such as `0.0.0.0`. This is dangerous and does not add TLS; prefer an SSH/VPN/tunnel setup. |
| Portal **New terminal → Name** | Empty/generated display name | Optional label, at most 80 characters. |
| Portal **New terminal → Starting directory** | Home directory | Accepts `~`, `~/path`, or an absolute accessible directory, at most 4096 characters. Browser creation always opens the configured shell; commands are typed afterward. |
| Portal-created native window | Enabled | A portal-created session also launches the platform terminal and runs `termlinks attach <opaque-session-id>`. There is currently no disable toggle; local CLI-created sessions keep their existing attach/detach behavior. |

`--listen` is a daemon option, so if the daemon is already running, stop that foreground daemon before changing it. Later automatic daemon starts reuse the saved address. `termlinks doctor` shows the effective listener, state directory, daemon status, and version without revealing tokens.

### Local state and secrets

The state directory is created with mode `0700`; sensitive files are forced to `0600`:

| File | Contents |
| --- | --- |
| `auth.token` | Random 256-bit portal login token created by `termlinks token` or the first daemon start. Treat it as full shell access. |
| `settings.json` | Persisted `listen` address. |
| `control.sock` | Private Unix socket used by the CLI and daemon; never expose or proxy it. |
| `daemon.log` | Output from an automatically detached daemon. |
| `cloud.json` | Relay URL, connector token, desktop-enabled flag, and loopback VNC address. It contains a secret. |
| `cloud.pid` | PID of the detached connector. |
| `cloud.log` | Detached connector diagnostics. It should not contain tokens, but still treat logs as private. |

On macOS the default directory is normally `~/Library/Application Support/termlinks`. On Linux it follows the user config directory, normally `$XDG_CONFIG_HOME/termlinks` or `~/.config/termlinks`. Do not commit this directory.

The portal token is not a configurable password string: Termlinks generates it, and `termlinks token` displays the same stored value. To rotate it, first stop the daemon so all in-memory sessions have ended, move `auth.token` to a private backup or trash, and start Termlinks to generate a new token. Existing browser sessions then stop authenticating after their current cookie expires or the daemon restarts.

The login form exposes standard username/current-password metadata so Safari, Chrome, an installed PWA, and the operating system password manager can offer to save and autofill the portal token. Save it under the generated `termlinks` username. Face ID or device-lock confirmation is controlled by iOS and the selected password manager; Termlinks never receives biometric data and cannot force the prompt.

In the hosted E2E portal, **Keep me signed in on this device** is enabled by default. After successful authentication, Termlinks stores the derived, non-exportable AES-GCM `CryptoKey` in origin-scoped IndexedDB—not the raw token. If iOS suspends or terminates the PWA, it uses that key to reconnect automatically and restores the previously open terminal when possible. **Log out** deletes the stored key. Clearing website data also deletes it. Uncheck the option on a shared or untrusted device.

### Direct portal authentication and network behavior

| Setting | Value |
| --- | --- |
| Local URL | `http://127.0.0.1:8787` unless `--listen` changed it. |
| Login | Portal token exchanged for a random `HttpOnly`, `SameSite=Strict` cookie. |
| Cookie lifetime | 12 hours. |
| Direct login rate limit | Five attempts per direct peer IP per minute. Proxy forwarding headers are intentionally ignored. |
| TLS | Not built into the daemon. Use loopback, SSH, an encrypted private network, or an HTTPS reverse tunnel/proxy. |
| WebSockets | A proxy must preserve Upgrade/Connection headers and long-lived WebSocket connections. |
| Portal mode detection | `GET /api/mode` returns `{"mode":"direct"}` from the daemon. A static hosted portal without that response uses the encrypted connector-relay mode. |

### Cloud connector

Configure cloud access only after deploying a compatible relay:

```sh
printf '%s' '<random-connector-secret>' | \
  termlinks cloud configure \
    --url https://<your-relay-host> \
    --token-stdin
```

| Configuration | Requirements and behavior |
| --- | --- |
| `--url` | Required plain `https://` origin. Credentials, query strings, fragments, and non-root paths are rejected. The connector derives `/connector` and `/status` from it. |
| `--token-stdin` | Required safety switch. Reads the connector credential from standard input so it need not appear in shell history; maximum input is 4096 bytes and the trimmed secret must be at least 32 characters. |
| `termlinks cloud start` | Starts a detached outbound connector after ensuring the local daemon is running. |
| `termlinks cloud status` | Reports configured relay, connector process, and remote online state. |
| `termlinks cloud stop` | Stops only the connector; managed PTYs remain running. |
| `termlinks cloud connect` | Internal foreground entry point used by `cloud start`; normally do not invoke it manually. |

The connector token authenticates the computer to the relay. The portal token authenticates and derives the browser E2E key. They are deliberately different and must never be substituted for each other.

### Remote desktop and selected windows

| Configuration | Default | Meaning |
| --- | --- | --- |
| `termlinks desktop enable [--address HOST:PORT]` | Disabled; `127.0.0.1:5900` | Enables GUI messages in the encrypted connector. The VNC target must be `localhost` or a loopback IP and port `1–65535`. |
| `termlinks desktop disable` | — | Revokes connector-side GUI access and restarts only the connector when it was running. It does not disable macOS Screen Sharing itself. |
| `termlinks desktop status` | — | Shows tunnel, VNC reachability, window-picker support, Screen Recording, and Accessibility status. |
| `termlinks desktop permissions` | — | On macOS, requests Screen Recording for viewing and Accessibility for control. |
| `termlinks desktop windows` | — | Lists the normal titled on-screen windows that the portal can offer. |
| Portal control toggle | View-only | **Enable control** must be selected before the UI sends pointer or keyboard input. It is an accident-prevention control, not another authentication layer. |
| Portal touch input | View-only until explicitly enabled | Tap the full-screen **Tap to enable touch control** shield, then use one finger to click or drag. Mouse and hardware keyboard input continue to work. |
| Portal **Send file** | Up to 100 MiB per file | Sends any browser-selected file over the authenticated AES-GCM channel and saves it to `~/Downloads/Termlinks Uploads`. Existing names are never overwritten. |

Selected-window capture requires macOS 14+ and a cgo-enabled build. Full-desktop mode additionally requires a loopback VNC/Screen Sharing server. These GUI modes currently travel through the encrypted connector-relay protocol, not the direct local portal.

### Cloudflare Worker and Pages settings

These are used only by the included default Cloudflare adapter:

| Name | Where | Purpose |
| --- | --- | --- |
| `CONNECTOR_TOKEN` | Worker secret | Must exactly match the value supplied to local `cloud configure`; minimum 32 characters. |
| `RELAY` | Worker Durable Object binding | Bound to class `TermlinksRelay`; the included config provisions it with SQLite storage. |
| `RELAY_ORIGIN` | Pages Function environment variable/secret | Plain HTTPS origin of the deployed relay Worker. The Pages Function refuses `/ws/bridge` with `503` when absent or invalid. |
| `CLOUDFLARE_API_TOKEN` | Wrangler process environment | Optional non-interactive Cloudflare API credential. Never commit it. Interactive `wrangler login` is the alternative. |
| `CLOUDFLARE_ACCOUNT_ID` | Wrangler process environment | Selects the Cloudflare account for non-interactive deployment. |
| Worker `name` | `apps/relay/wrangler.jsonc` or Wrangler `--name` | Choose a unique relay Worker name for each deployment/computer. |
| Pages project name | Wrangler `--project-name` | Choose a Pages project name; its generated `.pages.dev` URL becomes the portal URL. |

The checked-in Worker configuration also sets `main` to `src/index.ts`, enables `workers.dev`, uses compatibility date `2026-09-02` with `nodejs_compat`, creates the `TermlinksRelay` SQLite Durable Object as migration `v1`, enables logs at sampling rate `1`, and enables traces at `0.01`. The reference Worker routes one computer through Durable Object key `personal-computer` with location hint `apac`; a fork serving multiple devices must replace that fixed key with authenticated device routing. Change these values in `apps/relay/wrangler.jsonc` or `apps/relay/src/index.ts` if a fork needs different Cloudflare behavior.

For local Worker development, copy `apps/relay/.dev.vars.example` to the gitignored `apps/relay/.dev.vars` and replace its placeholder `CONNECTOR_TOKEN`. The `TERMLINKS_RELAY_NAME`, `TERMLINKS_PAGES_PROJECT`, `TERMLINKS_RELAY_URL`, `TERMLINKS_PORTAL_URL`, and `TERMLINKS_CONNECTOR_SECRET_FILE` names used in [docs/cloudflare.md](docs/cloudflare.md) are shell convenience variables for the documented commands; Termlinks itself does not read them.

### Build, install, and development

| Command or setting | Effect |
| --- | --- |
| `npm ci` | Reproducibly installs the locked root/web dependencies. |
| `npm run typecheck` | Checks the portal TypeScript. |
| `npm run typecheck:relay` | Checks the Cloudflare Worker TypeScript. |
| `npm test` | Runs both typechecks and all Go tests. |
| `npm run build:web` | Builds static portal/PWA assets into `apps/web/dist`. |
| `npm run sync:web` | Copies built assets into the Go embed tree. |
| `npm run build:backend` | Produces stripped `dist/termlinks`; macOS uses external linking and ad-hoc signs identifier `dev.termlinks.cli`. |
| `npm run build` / `make build` | Runs web build, embed sync, and backend build in order. |
| `make install` | Builds and installs to `$HOME/.local/bin/termlinks`. The Makefile currently has no `PREFIX` override. |
| `termlinks update` | Installs the newest compatible GitHub release after HTTPS, host, archive, version, and SHA-256 validation. It restarts an active cloud connector but preserves the daemon and PTYs. |
| `npm run dev --workspace @termlinks/web` | Rebuilds on change and serves static UI assets on `127.0.0.1:5173`; it does not proxy the daemon API. |
| `npm run types:relay` | Regenerates Cloudflare Worker types using `.dev.vars.example`. |
| `npm run deploy:relay` | Convenience deployment using the checked-in Worker name. Use the explicit commands in the Cloudflare guide for a custom name. |
| `npm run deploy:pages` | Convenience deployment to a Pages project named `termlinks`. Use the explicit guide for a custom project name. |
| `npm audit` | Audits npm dependencies. |

Standard Go build variables such as `GOOS`, `GOARCH`, and `CGO_ENABLED` are honored by Go. The native macOS selected-window module is compiled only on Darwin with cgo; unsupported builds retain terminal features and report the window picker as unavailable.

Official release archives are currently built natively for macOS ARM64/AMD64 and Linux ARM64/AMD64 by `.github/workflows/release.yml`. A maintainer creates a stable release by updating the checked-in version, committing it, and pushing a matching `vX.Y.Z` tag. The workflow refuses a mismatched tag, builds the embedded portal and native executable, creates `checksums.txt`, and publishes the artifacts to GitHub Releases.

### Optional integration-test environment variables

These variables are for maintainers and CI; normal users do not need them:

| Variable | Required/value | Effect |
| --- | --- | --- |
| `TERMLINKS_E2E_PORTAL` | HTTPS portal URL | Required by the public encrypted-bridge smoke tests. |
| `TERMLINKS_E2E_TOKEN` | Portal token, at least 32 characters | Required with the portal URL. Do not place it in committed scripts or CI logs. |
| `TERMLINKS_E2E_SESSION_NAME` | Exact session name | Selects a safe existing session; otherwise the first listed session is used. |
| `TERMLINKS_E2E_SEND` | Text | Sends the text plus Enter and waits to see that text in terminal output. Use only against a disposable echo/test session. |
| `TERMLINKS_E2E_CREATE_SHELL=1` | Boolean switch | Creates an isolated `portal-shell-smoke` in `/tmp`, verifies input/output, and stops it. |
| `TERMLINKS_E2E_DESKTOP_DISABLED=1` | Boolean switch | Verifies that a disabled connector rejects desktop access. |
| `TERMLINKS_E2E_DESKTOP_BRIDGE=1` | Boolean switch | Verifies an enabled test VNC/RFB byte bridge. |
| `TERMLINKS_E2E_WINDOW_CAPTURE=1` | Boolean switch | Lists macOS windows, opens one source, and verifies a JPEG frame. |
| `TERMLINKS_E2E_WINDOW_MATCH` | Case-insensitive substring | Chooses a window by combined application/title instead of the first source. |
| `TERMLINKS_E2E_WINDOW_TEXT` | Text | Injects text into the deliberately selected test window. |
| `TERMLINKS_E2E_WINDOW_SAVE=1` | Boolean switch | Sends Command-S after window text; use only with a disposable document. |
| `TERMLINKS_E2E_FILE_UPLOAD=1` | Boolean switch | Sends a small uniquely named text file through the encrypted upload protocol. Remove the resulting file from `~/Downloads/Termlinks Uploads` after a public smoke test. |
| `TERMLINKS_E2E_UPLOAD_NAME` | Safe basename | Overrides the smoke-test upload filename; it must not contain a path separator. |
| `TERMLINKS_WINDOW_INTEGRATION=1` | Boolean switch | Enables the native ScreenCaptureKit Go integration test, which is skipped by default. |

### Fixed safety and capacity limits

These limits and UI tuning values are compiled into this release rather than runtime-configurable:

- 64 retained sessions; the oldest completed entry is pruned when room is needed, while 64 simultaneously running sessions reject another start.
- PTYs default to 100 columns × 30 rows when no valid attached-terminal size is available, accept sizes from 20–500 columns and 5–300 rows, retain 2 MiB backend scrollback per session, and accept terminal input messages up to 64 KiB.
- The browser terminal retains 10,000 rendered rows, uses 13 px text below 600 px viewport width and 14 px otherwise, and polls the dashboard every 2.5 seconds.
- The relay permits eight simultaneous browser sockets and 5 MiB encrypted packets. Each browser channel permits either one VNC desktop or one selected-window stream.
- A browser may transfer at most two files concurrently; each file is limited to 100 MiB and is sent as acknowledged 192 KiB chunks. Partial files are deleted after errors or disconnects.
- noVNC uses viewport scaling, view-only startup, shared mode, and quality/compression level 6; it does not request remote framebuffer resizing.
- Selected-window capture accepts bounds from 320×240 through 2560×1800, captures approximately every 150 ms, JPEG-encodes locally at quality `0.62`, caps a frame at 2 MiB, and caps text/clipboard messages at 16 KiB.

Forks may change the corresponding source constants, but should reassess memory, bandwidth, latency, and denial-of-service exposure first.

## Managing sessions in the portal

After login, the portal dashboard automatically shows every managed terminal and its current state:

- Select **New terminal** to create one normal interactive shell. Termlinks immediately attaches the portal and opens a native Terminal window on the computer to the same PTY. Its optional starting directory may be `~`, `~/path`, or an absolute path.
- Inside that shell, type `cd`, `ls`, `codex`, `npm run dev`, or any other command exactly as in a desktop terminal.
- Terminal history uses native touch momentum on mobile and short smooth scrolling for mouse wheels and trackpads.
- Use the persistent three-line hybrid composer below the terminal to type or paste commands and agent messages. Press **Enter** or the arrow button to send; Termlinks appends the terminal's Enter byte, submits immediately, and dismisses the software keyboard after the confirmed send. Tap the composer to open the keyboard again. Press **Shift+Enter** to add another line before sending. Multiline content uses xterm's bracketed-paste behavior when the active terminal program supports it.
- Use the bottom terminal bar like tmux or browser tabs: swipe anywhere across the rail for direct momentum scrolling, then tap a named tab to switch. A quick swipe starting on the six-dot grip scrolls normally; press and hold that grip first, then drag left or right to reposition the tab. The order is remembered separately by that browser/PWA. Keyboard users can focus a tab and press **Alt+Left/Right**. Tap **☷** for the full session dashboard or **+** to open the New terminal form. Switching or reordering tabs never restarts the daemon-owned command.
- The terminal workspace uses a Termius-inspired dark navy shell with compact session metadata, numbered tmux-style tabs, a persistent E2E/local badge, and a blue command dock. This is Termlinks' own interface and does not copy Termius branding or assets.
- Tap **+** in the composer to choose an image, screenshot, or PDF. The file is E2E-encrypted, saved under `~/Downloads/Termlinks Uploads` on the connected computer, shown as an attachment chip, and its shell-quoted local path is inserted at the cursor so Codex, Claude, a script, or another terminal program can open it. Nothing is sent to the terminal until you press **Enter** or the send arrow.
- On iPhone/iPad, the terminal refits to the visual viewport when the software keyboard opens or closes, keeping the page width locked to the visible screen and the composer above the keyboard. Focusing the composer follows the live bottom through the keyboard animation. If the composer is not focused, an intentionally opened history position stays in history through ordinary resizing.
- The composer stays at a fixed height while typing so it cannot repeatedly resize the terminal. A terminal-native **Enter** control appears first above the composer, followed by Escape, Tab, Ctrl-C, Ctrl-D, and arrow controls. Clicking the xterm screen still enables direct keyboard input for editors and other full-screen programs.
- If iOS suspends the connection while opening Photos or Files, the open terminal channel reconnects automatically. A successfully uploaded attachment stays in the composer, and its Send arrow becomes available again as soon as the terminal is live.
- Terminal text stays in the terminal—there is no copy popup. Press and hold rendered output to use the browser's native text selection and Copy action. **Copy visible terminal output** remains available in the terminal's `•••` menu instead of occupying the keyboard-control strip.
- The header shows the number of running sessions. Finished and explicitly closed sessions are removed from the dashboard automatically.
- Each card shows the session name, command, directory, runtime, and status.
- Select **Open terminal** to view and type in that terminal.
- Select **Send file** to transfer images, PDFs, archives, or other files to `~/Downloads/Termlinks Uploads` on the computer. Transfers are E2E encrypted in cloud mode, filenames are validated, and duplicate names receive ` (1)`, ` (2)`, and so on.
- Select **Stop & close** to terminate a running command after confirmation.
- The terminal screen also has **Stop & close session** in its `•••` menu.
- While a terminal is open, its final output remains visible after the process exits. Returning to the dashboard clears that finished card.

Stopping or closing a session terminates its command. Simply closing the browser or local Terminal window only disconnects that viewer; the managed command continues running.

## Phone access

For the default Cloudflare deployment:

1. Deploy your own relay and portal by following [docs/cloudflare.md](docs/cloudflare.md).
2. Run `termlinks cloud start` on the computer.
3. Open your Pages URL on the phone or another computer.
4. Enter the token printed by `termlinks token`.
5. Select an existing session, or tap **New terminal** to create an interactive shell. A native terminal window opens on the computer and the phone attaches to the same shell.
6. Use **Send file** on the dashboard or remote-desktop toolbar to copy a file from the phone/browser to the computer.

Nothing needs to be installed on the viewing device. Giving another person the portal URL and portal token gives them the same full terminal access, so share it only with someone you completely trust. The connector secret is separate and must never be shared.

If the computer sleeps, shuts down, loses internet access, or runs `termlinks cloud stop`, the public portal reports the computer as offline. Running terminal processes also pause while macOS is asleep.

## Remote desktop (macOS-first)

Remote desktop is disabled by default. Termlinks 0.5 offers two source modes in the portal:

- **Full Mac desktop** uses macOS Screen Sharing/VNC and shows the complete framebuffer.
- **Selected window** uses Apple's ScreenCaptureKit to show a live list of on-screen application windows and streams only the one you choose. Screen Sharing does not need to be enabled for this mode.

Both modes use the same locally enabled, end-to-end encrypted Termlinks tunnel.

### 1. Enable the Termlinks tunnel

```sh
termlinks desktop enable --address 127.0.0.1:5900
termlinks desktop status
termlinks cloud status
```

The expected status is `Tunnel: enabled`. `termlinks desktop status` reports the full-desktop VNC server and the selected-window permission state independently.

### 2. Configure selected-window viewing and control

Selected-window mode requires macOS 14 or newer. Run this locally once:

```sh
termlinks desktop permissions
```

Approve **Screen & System Audio Recording** to view selected windows and **Accessibility** to control them. Restart only the connector after changing either permission:

```sh
termlinks cloud stop
termlinks cloud start
termlinks desktop status
```

You can verify exactly what the portal will offer without opening it:

```sh
termlinks desktop windows
```

The command prints each shareable window's ID, application, dimensions, and title. The same list appears under **Remote desktop → Open windows** in the portal. Tap **Refresh** after opening, closing, minimizing, or renaming a window. Selecting a window starts an independent capture of only that window; other desktop content is not included. The window must be on screen when the list is loaded.

Viewing requires Screen Recording permission. **Enable control** additionally requires Accessibility permission and lets Termlinks raise the real window and send pointer, scrolling, keyboard, text, shortcut, and clipboard input to the Mac. These actions affect the actual local application, not a copy.

### 3. Configure the full Mac desktop

The recommended method is **System Settings → General → Sharing → Screen Sharing**. Open its details, restrict access to only the intended macOS users, and configure strong credentials. Apple documents the current controls in its [Screen Sharing setup guide](https://support.apple.com/guide/mac-help/turn-screen-sharing-on-or-off-mh11848/mac).

Alternatively, this command opens the native macOS administrator-authorization dialog and enables the system Screen Sharing service:

```sh
osascript -e 'do shell script "/bin/launchctl load -w /System/Library/LaunchDaemons/com.apple.screensharing.plist" with administrator privileges'
```

The password is entered into the macOS dialog; Termlinks does not receive it. Review the allowed users afterward in System Settings. If ordinary macOS account authentication does not work with the browser viewer, configure the VNC-viewer password option shown by macOS and use a strong, unique password. The expected full-desktop status is `VNC server: reachable`.

### 4. Connect from the portal

1. Open your deployed HTTPS portal, enter the portal token, and select **Remote desktop**.
2. Choose **Full Mac desktop** or choose an entry from the encrypted **Open windows** list.
3. Full-desktop mode asks for Screen Sharing credentials; selected-window mode relies on the Mac's local privacy permissions and does not request a VNC password.
4. The page starts in view-only mode. Tap the large **Tap to enable touch control** shield (or the toolbar toggle) before sending touch, mouse, or keyboard input.
5. Use the toolbar for fullscreen, scaling, special keys, the mobile keyboard, and clipboard text sent to the Mac.

The default target is `127.0.0.1:5900`. A different local VNC server can be selected with `termlinks desktop enable --address 127.0.0.1:<port>`. Non-loopback targets are rejected. The VNC password is entered into the browser only when requested and is never written to Termlinks configuration or browser storage.

The viewer supports full-viewport scaling, fullscreen where the browser permits it, touch gestures, mouse input, hardware keyboards, an on-screen text/special-key panel, and clipboard text sent to the Mac. On iPhone/iPad, installing the PWA from **Share → Add to Home Screen** gives the largest persistent app view.

The remote desktop and selected-window views are video-like canvases of pixels, so their displayed text cannot be selected as normal webpage text. Select text inside the remote Mac application and use the remote clipboard controls where supported. This is separate from managed terminal pages, where rendered text supports the browser's native selection and **Copy screen** copies the visible output.

The selected-window picker lists individual on-screen application windows. It does not currently offer a separate physical-display picker, capture minimized windows, capture menus or transient popovers as separate sources, or control a window without bringing it into the real Mac's active UI. Full-desktop display layout still depends on the local Screen Sharing/VNC server.

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

Selected-window access also stops immediately when `termlinks desktop disable` disconnects the tunnel. To revoke its local OS permissions too, turn Termlinks off under **System Settings → Privacy & Security → Screen & System Audio Recording** and **Accessibility**.

### Remote desktop troubleshooting

- **Tunnel disabled:** run `termlinks desktop enable --address 127.0.0.1:5900`.
- **VNC server unreachable:** enable macOS Screen Sharing, then check the local port with `nc -z 127.0.0.1 5900` and rerun `termlinks desktop status`.
- **Window list says permission is missing:** run `termlinks desktop permissions`, approve Termlinks under **System Settings → Privacy & Security → Screen & System Audio Recording**, then restart the connector.
- **Window is visible but cannot be controlled:** allow Termlinks under **System Settings → Privacy & Security → Accessibility**, restart the connector, then tap the large **Tap to enable touch control** shield. One-finger touch maps directly to click and drag on iPhone/iPad.
- **Native terminal did not appear after New terminal:** keep the Mac user logged into the desktop. On first use, macOS may ask whether Termlinks/osascript may control Terminal; approve it under **System Settings → Privacy & Security → Automation**. The managed shell still exists and can be opened from the portal if window launch fails.
- **File transfer fails:** keep the connector online, use a file no larger than 100 MiB, and confirm that `~/Downloads` is writable. Successful files appear under `~/Downloads/Termlinks Uploads`.
- **Window is missing from the list:** make sure it is open, on screen, not minimized, and has a normal title, then tap **Refresh**.
- **Computer offline:** run `termlinks cloud start` and check `termlinks cloud status`. The connector is outbound-only; do not open or forward port 5900 on the router.
- **PWA appears outdated:** fully close and reopen it. If necessary, reload your portal URL in the browser or remove and add the Home Screen app again.
- **PWA asks for the token after every app switch:** sign in once with **Keep me signed in on this device** checked. A normal iOS WebSocket suspension then reconnects automatically. Explicit logout, clearing website data, private-browsing storage restrictions, or rotating the portal token requires login again.
- **Connection stops while away:** the Mac must remain powered on, awake, and online. Terminal processes and desktop access are unavailable while it sleeps.

### Remote desktop security notes

- Anyone with the portal token has full control of managed terminals and, while the desktop tunnel is enabled, can attempt to access the GUI. Treat the token like an administrator password and do not send it in chat, screenshots, source code, or logs.
- Termlinks restricts its VNC destination to a loopback address, but enabling macOS Screen Sharing can also expose the service to the Mac's local network. Restrict allowed macOS users, use strong credentials, keep the firewall enabled, and disable Screen Sharing when it is not needed.
- The portal token derives the AES-256-GCM bridge key in the browser. Cloudflare carries encrypted terminal and desktop payloads and cannot read their contents, although normal connection metadata remains visible.
- VNC credentials are supplied directly to the in-browser VNC client for the live connection. Termlinks does not save them.
- Selected-window titles, application names, captured frames, and control events are encrypted inside the same bridge. Cloudflare receives ciphertext, sizes, timing, and ordinary connection metadata only.
- Uploaded filenames and file bytes are chunked inside that same authenticated AES-256-GCM bridge. Cloudflare can observe transfer timing and ciphertext sizes but not names or contents.
- A remembered hosted-portal login stores a non-exportable derived bridge key in that Pages origin's IndexedDB. It avoids storing the raw token, but it is still an authentication capability: anyone able to use the unlocked PWA can control the connected computer. Use explicit **Log out** before sharing the phone and do not enable remembering on an untrusted device.
- macOS Screen Recording and Accessibility permissions apply to the installed Termlinks executable. Replacing it with an unsigned/differently signed build may require approval again; official local builds use the stable `dev.termlinks.cli` ad-hoc identifier.
- View-only mode prevents accidental input in the UI; it is a safety control, not an authentication boundary.

### Private-network access

The default portal listens only on the computer itself. For access away from home, connect the computer and phone to the same private VPN/tailnet, then bind Termlinks to the computer's private VPN address:

```sh
termlinks daemon --listen 100.x.y.z:8787
```

Open `http://100.x.y.z:8787` on the phone. Keep the daemon terminal open the first time; the address is saved, and later managed commands can auto-start the daemon with it.

Do not port-forward Termlinks from your router or expose its local port directly to the public internet. A private VPN or SSH tunnel is the lower-complexity private option; the included Cloudflare connector is the default public option and makes an authenticated outbound TLS connection.

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

For the default Cloudflare public path, the browser derives an AES-256-GCM key from the portal token and opens one encrypted bridge through the owner's Pages deployment. Pages and the Worker relay only ciphertext; the local connector decrypts approved requests and terminal data, then talks to `127.0.0.1:8787`. The portal token itself never crosses the network, and the local port is never publicly bound.

Remote desktop uses the same bridge. For a full desktop, browser-side noVNC speaks RFB/VNC through an in-memory WebSocket-like channel; Termlinks encrypts each byte chunk and the connector forwards it only to the configured loopback VNC address. For a selected window, the connector asks macOS ScreenCaptureKit for encrypted source metadata and bounded JPEG frames, then accepts only validated pointer, key, text, and clipboard messages. There is no public VNC port or generic TCP proxy.

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

The portal currently includes a small **TermAds — Coming soon** teaser linking to [termads.dev](https://termads.dev/). It is a normal external link only: Termlinks does not load TermAds scripts, ads, tracking, or network requests inside terminal sessions.

## Related open-source projects

Termlinks is not the only open-source way to reach a shell or desktop remotely. The closest options solve overlapping but different problems:

- [ttyd](https://github.com/tsl0922/ttyd) is a small, mature tool for sharing one command-line program in a browser.
- [Ptylon](https://github.com/alexfrmn/ptylon) is a self-hosted browser workspace with persistent PTYs, files, an editor, and server-side browser sessions, aimed at coding agents.
- [ShellHub](https://github.com/shellhub-io/shellhub) focuses on centrally managed SSH access to devices.
- [Apache Guacamole](https://guacamole.apache.org/) is a clientless gateway for established protocols such as SSH, VNC, and RDP.
- [MeshCentral](https://github.com/Ylianst/MeshCentral) is a broader device-management platform with browser-based terminal, desktop, and file access.
- [RustDesk](https://github.com/rustdesk/rustdesk) is a self-hostable AnyDesk/TeamViewer-style remote-desktop system.

Termlinks' particular focus is a lightweight local executable that owns named, reconnectable PTYs for arbitrary commands, has a mobile/PWA interface, can create shells from the portal, and carries both terminal and opt-in macOS window/desktop control through the same application-encrypted outbound bridge. Choose an established project above if its narrower or broader model fits better.

## Current boundary

- Only sessions managed by Termlinks can be opened. Launch them with `termlinks …` or create an interactive shell from the portal; unrelated Terminal/iTerm windows cannot be attached after the fact because their PTYs belong to another process.
- Remote desktop is an explicitly enabled, macOS-first subsystem. The terminal features remain usable without it.
- The portal supports the complete VNC framebuffer or one selected on-screen macOS window. A separate physical-display picker and minimized-window capture are not implemented yet.
- Sessions live in daemon memory and do not survive a daemon or computer restart.
- An authenticated browser can create a normal interactive shell, then type arbitrary commands into it. Treat the portal token as full access to your operating-system user account.
- One Termlinks installation uses one shared portal identity. It does not yet provide per-person accounts, per-session permissions, or an audit trail.
- One relay deployment represents one configured computer. Each additional computer needs its own relay/portal deployment and secrets until multi-device pairing and device selection are implemented.

Read [SECURITY.md](SECURITY.md) before enabling network access.
