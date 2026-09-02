# Security model

Termlinks provides remote keyboard access to local processes. Treat access to the portal as access to your user account: a terminal can read files, use logged-in developer tools, and execute commands with your permissions.

## Protections in this version

- The web listener defaults to `127.0.0.1:8787`.
- Wildcard binds such as `0.0.0.0` and `[::]` are refused unless `--allow-public-bind` is explicitly supplied.
- Local process creation is available only through a Unix-domain control socket inside a `0700` state directory; the socket is `0600`.
- The portal has no endpoint that creates a command or shell.
- A random 256-bit bearer token is stored in a `0600` file. It is exchanged for a random, `HttpOnly`, `SameSite=Strict` browser cookie that expires after 12 hours.
- Login attempts are rate-limited by direct peer IP. Proxy forwarding headers are not trusted.
- State-changing browser requests and WebSocket upgrades require an exact same-origin request.
- Request/input sizes, terminal dimensions, scrollback, session count, and HTTP headers are bounded.
- The portal uses a restrictive Content Security Policy, refuses framing, disables sensitive browser features, and does not store the login token in browser storage.
- Command names, arguments, paths, and terminal output are inserted into the page as text, not HTML.

## Safe deployment

1. Use a private encrypted overlay network such as Tailscale or WireGuard on both devices.
2. Bind to that network interface's specific IP, never to a public or wildcard address.
3. Keep host firewall and VPN device approval enabled.
4. Never publish the port through a router, tunnel, public reverse proxy, or public cloud load balancer.
5. Keep the token private. Do not paste it into chat, logs, shell history, or any page except your own Termlinks portal.
6. Stop the daemon before rotating the token. Remove the token file from the state directory, then restart to generate a new one.

The state directory is shown by `termlinks doctor`. On macOS it normally lives under `~/Library/Application Support/termlinks`; on Linux it follows the user config directory.

## Important limitations

- There is no built-in TLS. HTTP is acceptable only over localhost or an encrypted private network.
- There is no multi-user authorization, per-command permission system, audit log, or device revocation UI.
- Any authenticated browser can send arbitrary bytes to every managed running terminal and request that a session stop.
- Terminal output and command arguments may themselves contain secrets. The most recent 2 MiB per session remains in daemon memory for reconnects.
- Termlinks inherits the launching environment for the child command. Environment variables are passed only over the private local socket and are not returned by the web API, but the child may print them.
- Security depends on the operating-system user account, the private-network account, the phone, and the token remaining uncompromised.

For these reasons, this MVP is appropriate for personal use on trusted devices, not shared hosts or a public SaaS service.
