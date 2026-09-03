# Terminal History, Favorites, Rename, Duplicate, and Folder Labels

## Summary

Add private, computer-local saved terminal management to the web app: closed terminals go into a max-10 Recent history, Favorites are capped at 100, terminal names can be edited, terminals can be duplicated/reopened quickly, and every terminal card shows a short folder/project label.

## Key Changes

- Add a saved-terminal model persisted in a mode-`0600` local SQLite database:
  - Fields: stable saved id, source/active session associations, name, cwd, favorite flag, and created/updated/last-opened/last-closed timestamps.
  - Do not persist command arguments, terminal input, output, or scrollback.
  - Expose the model only through authenticated, same-origin endpoints included in the encrypted connector allowlist.
  - Recent history keeps max 10 non-favorite entries.
  - Favorites keep max 100 entries and are never pruned by Recent cleanup.
- Reconcile closed terminals idempotently from the daemon's authoritative session IDs and `endedAt` timestamps. Polling must not rewrite timestamps or merge sessions that happen to share a name and directory.
- Dashboard UI:
  - Keep Running sessions.
  - Add separate Favorites and Recent sections.
  - Each terminal card shows a short folder/project label derived from `cwd`, using the last folder name such as `termlinks`; the full path stays available in the card metadata/title.
  - Each saved item supports Open new shell, Rename, Favorite/Unfavorite, New copy shell, and Remove.
- Terminal page actions:
  - Add Rename, Favorite/Unfavorite, and Duplicate actions to the existing `...` menu.
  - Rename updates local persisted records and the running session name.
- Backend API:
  - Add a narrow authenticated rename endpoint such as `PATCH /api/sessions/{id}` accepting `{ "name": "..." }`.
  - Add session manager support to update only the session name with the same validation limit already used for browser-created session names.
- Duplicate/Open behavior:
  - Create a new terminal through existing `POST /api/sessions`.
  - Use the saved/running terminal's cwd and name, with duplicate names defaulting to `Original Copy`.
  - Preserve the existing browser security contract: web duplication starts the default interactive shell in the same cwd, not arbitrary command replay.
  - Open the same PTY in a visible native terminal window on the computer.
  - Serialize saved-terminal open requests so two fast taps cannot create orphaned duplicate shells.

## Test Plan

- Web typecheck and saved-history logic tests: `npm run typecheck` and `npm run test:web`.
- Backend tests: `npm run test:go`.
- Full test suite if time allows: `npm test`.
- Add focused Go tests for authenticated rename success, validation failure, missing session, and cross-origin rejection.
- Manually verify in the web UI:
  - Stop/close a terminal and see it appear in Recent.
  - Recent caps at 10 while Favorites cap at 100.
  - Favorite/unfavorite works from dashboard and terminal menu.
  - Rename updates running card, terminal header, favorites/history records, and survives reload.
  - Duplicate creates a new running terminal using the same cwd with a copy-style name.
  - Terminal cards clearly show the short folder/project label.

## Implementation status

Implemented for PR #1 with local persistence, bounded storage, E2E route restrictions, backend integration coverage, frontend helper tests, and an isolated-daemon API smoke test.

## Assumptions

- Storage is local to the connected computer and shared across authenticated clients through E2E APIs.
- Browser storage never contains terminal history metadata.
- Favorites have a limit of 100.
- Recent history max is 10 non-favorite saved terminals.
- Rename applies to both saved records and currently running sessions.
- Favorites and Recent appear as separate dashboard sections.
- Short folder/project label is derived from the terminal working directory's last path segment.
