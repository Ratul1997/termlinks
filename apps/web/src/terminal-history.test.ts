import assert from "node:assert/strict";
import {
  decodeTerminalHistory,
  duplicateTerminalName,
  MAX_SAVED_TERMINALS,
  projectLabel,
  savedSessionID,
  visibleSavedGroups,
  type SavedTerminal,
} from "./terminal-history";

const base: SavedTerminal = {
  id: "00000000000000000000000000000001",
  sourceSessionId: "00000000000000000000000000000002",
  name: "Project shell",
  cwd: "/Volumes/MyWork/PH/termlinks",
  favorite: false,
  createdAt: "2026-09-03T10:00:00Z",
  updatedAt: "2026-09-03T10:00:00Z",
  lastOpenedAt: "2026-09-03T10:00:00Z",
  lastClosedAt: "2026-09-03T10:05:00Z",
};

assert.deepEqual(decodeTerminalHistory({ terminals: [base] }), [base]);
assert.throws(() => decodeTerminalHistory({ terminals: [{ ...base, cwd: "" }] }), /invalid terminal history/);
assert.throws(() => decodeTerminalHistory({ terminals: Array.from({ length: MAX_SAVED_TERMINALS + 1 }, () => base) }), /invalid terminal history/);

const active = { ...base, id: "00000000000000000000000000000003", activeSessionId: "00000000000000000000000000000004", favorite: true };
const favorite = { ...base, id: "00000000000000000000000000000005", sourceSessionId: "00000000000000000000000000000006", favorite: true };
const newer = { ...base, id: "00000000000000000000000000000007", sourceSessionId: "00000000000000000000000000000008", lastClosedAt: "2026-09-03T11:00:00Z" };
const groups = visibleSavedGroups([base, active, favorite, newer], new Set([active.activeSessionId]));
assert.deepEqual(groups.favorites.map((item) => item.id), [favorite.id]);
assert.deepEqual(groups.recent.map((item) => item.id), [newer.id, base.id]);
assert.equal(savedSessionID(active), active.activeSessionId);

assert.equal(projectLabel("/Volumes/MyWork/PH/termlinks/"), "termlinks");
assert.equal(projectLabel("C:\\Users\\ratul\\project\\"), "project");
assert.equal(projectLabel("C:\\"), "C:\\");
assert.equal(duplicateTerminalName("Project"), "Project Copy");
assert.equal(duplicateTerminalName("x".repeat(80)).length, 80);

console.log("terminal history UI logic passed");
