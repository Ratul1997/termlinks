# Terminal History, Favorites, Rename, Duplicate, and Folder Labels

## Summary

Add browser-local saved terminal management to the web app: closed terminals go into a max-10 Recent history, favorites have no limit, terminal names can be edited, terminals can be duplicated/reopened quickly, and every terminal card shows a short folder/project label. Implement on branch `farzan` and open a PR into `main`.

## Key Changes

- Add a saved-terminal model in `apps/web/src/main.ts` persisted in browser storage:
  - Fields: stable saved id, source session id when known, name, cwd, command label, favorite flag, created/updated/lastOpened timestamps.
  - Recent history keeps max 10 non-favorite entries.
  - Favorites are unlimited and are never pruned by history cleanup.
- Detect closed/ended terminals from `/api/sessions` results and from Stop & close actions, then save them into Recent before removing from the running list.
- Dashboard UI:
  - Keep Running sessions.
  - Add separate Favorites and Recent sections.
  - Each terminal card shows a short folder/project label derived from `cwd`, using the last folder name such as `termlinks`; the full path stays available in the card metadata/title.
  - Each saved item supports Open/Reconnect, Rename, Favorite/Unfavorite, Duplicate, and Remove.
- Terminal page actions:
  - Add Rename, Favorite/Unfavorite, and Duplicate actions to the existing `...` menu.
  - Rename updates browser-saved records and the running session name.
- Backend API:
  - Add a narrow authenticated rename endpoint such as `PATCH /api/sessions/{id}` accepting `{ "name": "..." }`.
  - Add session manager support to update only the session name with the same validation limit already used for browser-created session names.
- Duplicate/Open behavior:
  - Create a new terminal through existing `POST /api/sessions`.
  - Use the saved/running terminal's cwd and name, with duplicate names defaulting to `Original Copy`.
  - Preserve the existing browser security contract: web duplication starts the default interactive shell in the same cwd, not arbitrary command replay.

## Test Plan

- Web typecheck: `npm run typecheck`.
- Backend tests: `npm run test:go`.
- Full test suite if time allows: `npm test`.
- Add focused Go tests for authenticated rename success, validation failure, missing session, and cross-origin rejection.
- Manually verify in the web UI:
  - Stop/close a terminal and see it appear in Recent.
  - Recent caps at 10 while Favorites remain unlimited.
  - Favorite/unfavorite works from dashboard and terminal menu.
  - Rename updates running card, terminal header, favorites/history records, and survives reload.
  - Duplicate creates a new running terminal using the same cwd with a copy-style name.
  - Terminal cards clearly show the short folder/project label.

## Branch and PR

- Start from clean/latest `main`, create branch `farzan`.
- Save this plan at `docs/terminal-history-favorites-plan.md`.
- Implement and verify.
- Commit all scoped changes.
- Push `farzan` to `origin`.
- Create PR with `gh pr create --base main --head farzan`, using a summary of the feature and test results.

## Assumptions

- Storage is browser-local for v1.
- Favorites have no limit.
- Recent history max is 10 non-favorite saved terminals.
- Rename applies to both saved records and currently running sessions.
- Favorites and Recent appear as separate dashboard sections.
- Short folder/project label is derived from the terminal working directory's last path segment.
