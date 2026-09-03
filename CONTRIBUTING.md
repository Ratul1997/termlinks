# Contributing to Termlinks

Termlinks welcomes human-written and AI-assisted contributions. The standard is the same for both: the submitter must understand the change, verify its behavior, and take responsibility for its security and maintenance impact.

## Before starting

For a small fix, open a focused pull request. For a new protocol, permission, persistence model, platform integration, or substantial UI direction, open a feature issue first so the threat model and scope can be agreed before implementation.

Never include portal tokens, connector credentials, cookies, private terminal output, uploaded files, local databases, or machine-specific state in an issue, test fixture, commit, screenshot, or CI log. Follow [SECURITY.md](SECURITY.md) for vulnerability reports.

## Development setup

Requirements:

- Go 1.25+; the module selects the tested Go 1.26.8 toolchain
- Node.js 20+ and npm
- macOS or Linux

```sh
git clone https://github.com/Ratul1997/termlinks.git
cd termlinks
npm ci
npm test
npm run build
```

The generated executable is `dist/termlinks`. Use a temporary state directory for development so tests cannot affect personal sessions:

```sh
TERMLINKS_STATE_DIR="$(mktemp -d)" ./dist/termlinks daemon --headless
```

Do not point manual tests at a developer's real daemon, real cloud connector, coding-agent session, or home-directory state. Production-like E2E tests must create a clearly named disposable session, stop it, and verify cleanup.

## Required checks

Run these before opening a pull request:

```sh
npm test
go vet ./apps/backend/...
go -C apps/backend test -race ./...
npm run build
npm audit --audit-level=high
git diff --check
```

Pull requests run the normal suite on macOS and Linux plus the Go race detector on Linux. Platform-specific changes should include tests that compile or execute on every affected target.

## Design boundaries

- Keep terminal execution, history, AI-agent state, and uploaded files local by default.
- Treat the relay as untrusted for terminal and desktop plaintext.
- Preserve explicit user authorization for shell creation, session stopping, file transfer, desktop control, and external side effects.
- Browser reconnects must attach to an existing PTY, not create a replacement.
- The daemon owns local process and native-window lifecycle. Connectors transport authenticated requests and encrypted bytes.
- Fail closed at authentication, origin, path, size, sequence, and permission boundaries.
- Never claim a command, session, workflow, or security property succeeded without observable evidence.

Changes to authentication, cryptography, WebSocket framing, relay routing, self-update, PTY creation, file upload, or desktop input require a short threat-model section in the pull request and negative regression tests.

## Pull-request style

- Keep one user-facing purpose per pull request.
- Explain the problem before the implementation.
- Add or update tests and documentation with behavior changes.
- Preserve unrelated work and avoid generated formatting churn.
- Use clear commit messages such as `fix: prevent duplicate terminal creation`.

By contributing, you agree that your contribution is licensed under the repository's [MIT License](LICENSE).
