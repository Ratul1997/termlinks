# Changelog

All notable user-visible changes are recorded here. Termlinks uses semantic versioning while it is in developer preview.

## [Unreleased]

### Added

- A checksum-verifying macOS/Linux release installer and a shorter binary-first quick start.
- GitHub-native CI, release, and license badges for clearer project status.
- An isolated, non-executing **V2 Design** preview for a mobile text-stream terminal, command suggestions, wrapped/selectable output, and a raw-terminal escape hatch. The current terminal UI remains unchanged.

## [0.8.2] - 2026-09-04

### Fixed

- Terminal reconnects keep the previous xterm buffer readable, show a compact reconnect indicator, and pause input until replacement scrollback is ready.
- Explicit snapshot framing separates complete retained output from live terminal bytes, preventing blank reconnect gaps and duplicated history while remaining compatible with older daemons.
- Reconnect snapshot application preserves whether the user was at the live bottom or viewing earlier history.

## [0.8.1] - 2026-09-04

### Fixed

- Portal-created native terminal windows now close their dedicated attachment shell when the managed session finishes.
- Shell creation through the cloud connector now goes through the daemon, preserving the daemon's visible-window or headless policy.
- Connector tests no longer launch real native terminal windows.
- The default session list now shows running sessions; `termlinks list --all` includes retained completed entries.

### Added

- Pull-request CI for macOS and Linux, including Go race detection and dependency auditing.
- Contributor guidance, issue forms, a pull-request checklist, a support matrix, and a public roadmap.

## [0.8.0] - 2026-09-03

### Added

- Experimental local AI workflow coordination for installed Codex and Claude Code CLIs.
- Private SQLite workflow and terminal-history state.
- Browser-created terminal continuity with native terminal attachment.

[Unreleased]: https://github.com/Ratul1997/termlinks/compare/v0.8.2...HEAD
[0.8.2]: https://github.com/Ratul1997/termlinks/compare/v0.8.1...v0.8.2
[0.8.1]: https://github.com/Ratul1997/termlinks/compare/v0.8.0...v0.8.1
[0.8.0]: https://github.com/Ratul1997/termlinks/releases/tag/v0.8.0
