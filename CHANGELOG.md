# Changelog

All notable user-visible changes are recorded here. Termlinks uses semantic versioning while it is in developer preview.

## [Unreleased]

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

[Unreleased]: https://github.com/Ratul1997/termlinks/compare/v0.8.1...HEAD
[0.8.1]: https://github.com/Ratul1997/termlinks/compare/v0.8.0...v0.8.1
[0.8.0]: https://github.com/Ratul1997/termlinks/releases/tag/v0.8.0
