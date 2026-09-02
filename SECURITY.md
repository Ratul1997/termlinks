# Security model

Termlinks provides remote keyboard access to local processes and can optionally provide full GUI control. Treat access to the portal as access to your user account: a terminal can read files, use logged-in developer tools, and execute commands with your permissions, while remote desktop can interact with visible applications.

## Protections in this version

- The web listener defaults to `127.0.0.1:8787`.
- Wildcard binds such as `0.0.0.0` and `[::]` are refused unless `--allow-public-bind` is explicitly supplied.
- Local process creation is available only through a Unix-domain control socket inside a `0700` state directory; the socket is `0600`.
- The authenticated portal can create only a normal interactive shell with an optional name and starting directory. It cannot submit a separate executable, argument vector, or custom environment, although commands typed into the shell have the user's full permissions.
- A random 256-bit bearer token is stored in a `0600` file. The local portal exchanges it for a random, `HttpOnly`, `SameSite=Strict` browser cookie that expires after 12 hours.
- Login attempts are rate-limited by direct peer IP. Proxy forwarding headers are not trusted.
- State-changing browser requests and WebSocket upgrades require an exact same-origin request.
- Request/input sizes, terminal dimensions, scrollback, session count, and HTTP headers are bounded.
- The portal uses a restrictive Content Security Policy, refuses framing, disables sensitive browser features, and does not store the login token in browser storage.
- The PWA service worker caches only the static app shell, manifest, and icons. It bypasses `/api/`, `/ws/`, non-GET requests, cross-origin traffic, tokens, session metadata, terminal output, and keystrokes.
- Command names, arguments, paths, and terminal output are inserted into the page as text, not HTML.
- The optional cloud connector makes an outbound authenticated WSS connection; the local portal remains bound to loopback and no inbound port is opened.
- The public browser derives an AES-256-GCM key from the portal token and proves possession with an encrypted random challenge. The token itself never crosses the network or enters browser storage.
- Public API requests, responses, session metadata, terminal output, and keystrokes remain application-encrypted between the browser and local connector. Cloudflare relays opaque ciphertext and connection metadata only.
- The local connector decrypts and accepts only the exact logout, session-list, create-interactive-shell, stop, and terminal operations. It cannot proxy arbitrary localhost paths. Shell creation is forwarded over the private Unix control socket.
- Portal-created shells open a native terminal by passing only a validated opaque session ID to the installed Termlinks executable. macOS automation receives a strictly quoted `termlinks attach` command; browser input cannot choose a local executable or inject launcher arguments.
- Remote desktop is disabled by default, can be enabled or revoked only through the local CLI, and takes effect by restarting only the connector. The configured VNC target must be `localhost` or a loopback IP, each browser channel is limited to one desktop socket, and byte sizes are bounded.
- The remote desktop framebuffer, VNC authentication exchange, clipboard content, pointer events, and keystrokes use the same end-to-end encrypted channel. VNC credentials are neither stored nor sent to Cloudflare as plaintext.
- On macOS 14+, selected-window mode obtains its encrypted window list and bounded JPEG frames through ScreenCaptureKit. Viewing requires the operating system's Screen Recording permission; control additionally requires Accessibility. The connector accepts only an existing numeric window ID plus bounded dimensions and validated pointer, keyboard, text, or clipboard events.
- Selected-window capture is limited to one active capture per authenticated browser channel. Window titles, application names, frames, and input remain inside the end-to-end encrypted payload. The connector does not expose a generic macOS automation or screenshot API.
- File names and bytes use the same E2E channel. Uploads are size/chunk/concurrency bounded, ordered by exact byte offset, written with private permissions, finalized without overwriting, and cleaned up when incomplete. The fixed destination is `~/Downloads/Termlinks Uploads`; the browser cannot choose an arbitrary local path.
- The connector secret and browser portal token are independent random credentials. The connector secret is stored locally in a `0600` file and as a Cloudflare Worker secret.
- Public responses use HTTPS security headers, random AES-GCM nonces, direction/sequence/channel-bound authenticated encryption, bounded message sizes, a short unauthenticated timeout, and a Durable Object connection/viewer limit.
- A directly tunneled portal advertises `/api/mode` so the same frontend can select its cookie-authenticated local API instead of assuming a particular hosted domain. This endpoint contains no session or user data.
- `termlinks update` uses a fixed GitHub release API, accepts only HTTPS downloads from GitHub-controlled hosts, bounds metadata/archive/binary sizes, rejects unsafe archive entries and links, verifies the release SHA-256 entry, executes the staged binary only for an exact version check, and atomically replaces the invoking executable. It never restarts the daemon or active PTYs; only an already-running cloud connector is restarted.

## Safe deployment

1. Prefer a private encrypted overlay network such as Tailscale or WireGuard when its device approval model is sufficient.
2. For the included Cloudflare deployment, keep the local listener on loopback and use `termlinks cloud start`; never port-forward the local port.
3. If using another reverse-tunnel or HTTPS provider, preserve WebSocket upgrades, enable its identity/MFA gate, and understand that direct mode is protected by transport TLS rather than Termlinks' connector-relay E2E layer.
4. Keep the hosting-provider account protected with MFA and narrowly scoped deployment tokens.
5. Keep both Termlinks tokens private. Enter the portal token only on your portal and never share the connector secret.
6. Treat anyone given the portal token as having full access to your managed terminals.
7. Stop the daemon before rotating the browser portal token. Remove the token file from the state directory, then restart to generate a new one.
8. If full-desktop mode is enabled, use a strong unique Screen Sharing/VNC password and limit Screen Sharing to specific macOS users. macOS Screen Sharing itself may be reachable from the local network depending on system firewall and sharing settings; Termlinks' loopback restriction does not change the operating system service's LAN exposure.
9. Grant Screen Recording and Accessibility only to the installed Termlinks binary. Revoke them in macOS Privacy & Security and run `termlinks desktop disable` when selected-window access is no longer needed.

The state directory is shown by `termlinks doctor`. On macOS it normally lives under `~/Library/Application Support/termlinks`; on Linux it follows the user config directory.

## Important limitations

- The local listener has no built-in TLS and is safe only on localhost, an SSH tunnel, or an encrypted private network. A public reverse tunnel must provide HTTPS/WSS and should add its own identity gate.
- There is no multi-user authorization, per-command permission system, audit log, or device revocation UI.
- One deployment represents one configured computer. Reusing its connector credential on a second computer is unsupported and can cause connector replacement; multi-device pairing and routing are not implemented.
- Any authenticated browser can create an interactive shell, send arbitrary bytes to every managed running terminal, and request that a session stop.
- An authenticated browser can upload arbitrary file content (up to 100 MiB per file) into the fixed uploads directory and can cause a visible terminal window to open in the logged-in desktop session.
- Once remote desktop is locally enabled, any authenticated portal client must be treated as capable of full RFB control. The view-only toggle is a safety affordance, not an authorization boundary.
- Full-desktop mode relies on the local Screen Sharing/VNC server for capture, authentication, display layout, and input permissions. Selected-window mode uses native ScreenCaptureKit but currently lists only normal titled on-screen windows; it has no separate physical-display picker or minimized-window capture.
- Selected-window control raises and interacts with the real local window. Accessibility permission is process-wide, and this single-owner version does not provide independent per-window or per-viewer OS permissions.
- Terminal output and command arguments may themselves contain secrets. The most recent 2 MiB per session remains in daemon memory for reconnects.
- Termlinks inherits the launching environment for the child command. Environment variables are passed only over the private local socket and are not returned by the web API, but the child may print them.
- Security depends on the operating-system user account, the private-network account, the phone, and the token remaining uncompromised.
- Cloud access also depends on the chosen provider account, deployed frontend, browser supply chain, and any connector credential remaining uncompromised.
- Public attackers can attempt connections or denial-of-service traffic. The 256-bit portal token makes successful guessing impractical, but timeouts and connection caps do not eliminate availability attacks.
- The default Cloudflare relay still observes IP addresses, connection timing, ciphertext sizes, and whether the computer is online. E2E encryption does not conceal this metadata.
- Hosted frontend JavaScript is delivered by the selected provider. If that account or deployment is compromised, altered JavaScript could capture a portal token when it is typed; E2E encryption cannot protect against a malicious client build.
- Update checksums detect corruption and mismatched release files, but they are published in the same GitHub release as the binaries. They do not protect against compromise of the project repository, maintainer account, release workflow, GitHub, or the build dependencies. Review and build from source when that trust boundary is not acceptable.
- Offline PWA launch provides only the cached interface. Terminal access still requires an online computer and connector, and the portal token must be entered again after a fresh app process starts.

For these reasons, this MVP is appropriate for personal use on trusted devices, not shared hosts or a public SaaS service.
