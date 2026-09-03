# Roadmap

Termlinks is a local-first continuity layer, not a hosted shell account. The computer remains the source of truth: its processes, files, AI-tool logins, and terminal sessions stay under the local operating-system user.

This roadmap describes direction, not a promise of dates. Security and session safety take priority over feature count.

## Current developer preview

- Managed interactive PTYs with reconnectable browser and native-terminal views.
- Local CLI/daemon and installable mobile PWA.
- Optional end-to-end encrypted Cloudflare relay with an outbound local connector.
- File uploads to a fixed local directory.
- Opt-in full-desktop VNC forwarding and macOS selected-window capture/control.
- Experimental local coordination of installed Codex and Claude Code CLIs.
- Native release artifacts for macOS and Linux, with pull-request CI on both systems.

The current trust model is one owner, one configured computer, and trusted client devices. See [SECURITY.md](SECURITY.md) for the complete limitations.

## Near-term priorities

1. Per-device pairing, named devices, revocation, session expiry controls, and a visible security activity log.
2. Multiple-computer registration and explicit routing without sharing one connector identity.
3. A durable session backend or controlled daemon handoff so upgrades and daemon restarts do not end managed PTYs.
4. Workflow retention controls, cancellation recovery, clearer agent approval boundaries, and isolated Git worktrees.
5. Fresh-machine installation tests and improved Linux desktop/window integration.
6. Independent security review and a documented threat-model review process.

## Platform expansion

- **Linux:** improve native-terminal detection and document tested VNC/desktop combinations. Selected-window capture needs a Wayland/X11-specific design before it can be supported safely.
- **Windows:** port the daemon control channel and PTY layer, add native terminal launching, define secure local state permissions, and publish signed release artifacts. Windows is not supported until these pieces have automated tests.
- **Providers:** keep the connector protocol provider-neutral while treating Cloudflare as the maintained reference adapter. Community adapters should preserve WebSockets, message bounds, and end-to-end encryption.

## Release-quality gates

Before calling Termlinks stable, the project should have:

- clean installation and upgrade coverage on every supported target;
- recovery tests for disconnects, sleep/wake, connector replacement, and interrupted updates;
- device-scoped authentication and revocation;
- a completed external security assessment with high-severity findings resolved;
- clear backup/migration rules for local state;
- no platform advertised as supported without CI and maintained release artifacts.

## Non-goals

- Becoming a public multi-tenant shell hosting service.
- Reading or centralizing AI-provider credentials.
- Silently running an AI agent or command without an explicit user-confirmed workflow.
- Requiring Cloudflare or any other hosted provider for local, SSH, VPN, or self-hosted use.

Contributions that advance these goals are welcome. Start with [CONTRIBUTING.md](CONTRIBUTING.md), and use a private GitHub security advisory for vulnerabilities.
