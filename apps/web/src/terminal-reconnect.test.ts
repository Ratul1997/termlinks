import assert from "node:assert/strict";
import {
  MAX_TERMINAL_SNAPSHOT_BYTES,
  TerminalStreamReconciler,
  terminalStreamControl,
} from "./terminal-reconnect";

const encoder = new TextEncoder();
const decoder = new TextDecoder();

const framed = new TerminalStreamReconciler();
assert.equal(framed.waitingForSnapshot, true);
assert.equal(framed.receiveControl({ type: "terminal_snapshot_start", bytes: 6 }), undefined);
assert.equal(framed.framedSnapshotStarted, true);
assert.equal(framed.receiveBinary(encoder.encode("abc")), undefined);
assert.equal(framed.receiveBinary(encoder.encode("def")), undefined);
const snapshot = framed.receiveControl({ type: "terminal_snapshot_end" });
assert.equal(snapshot?.kind, "snapshot");
assert.equal(decoder.decode(snapshot?.data), "abcdef");
assert.equal(framed.waitingForSnapshot, false);
const live = framed.receiveBinary(encoder.encode("live"));
assert.equal(live?.kind, "live");
assert.equal(decoder.decode(live?.data), "live");

const empty = new TerminalStreamReconciler();
empty.receiveControl({ type: "terminal_snapshot_start", bytes: 0 });
assert.deepEqual(empty.receiveControl({ type: "terminal_snapshot_end" }), { kind: "snapshot", data: new Uint8Array() });

const legacy = new TerminalStreamReconciler();
const legacySnapshot = legacy.receiveBinary(encoder.encode("old daemon scrollback"));
assert.equal(legacySnapshot?.kind, "snapshot");
assert.equal(decoder.decode(legacySnapshot?.data), "old daemon scrollback");
assert.equal(legacy.receiveBinary(encoder.encode("new output"))?.kind, "live");

assert.deepEqual(terminalStreamControl({ type: "terminal_snapshot_start", bytes: 12 }), { type: "terminal_snapshot_start", bytes: 12 });
assert.deepEqual(terminalStreamControl({ type: "terminal_snapshot_end" }), { type: "terminal_snapshot_end" });
assert.equal(terminalStreamControl({ type: "status", running: true }), undefined);

assert.throws(() => new TerminalStreamReconciler().receiveControl({ type: "terminal_snapshot_end" }), /Unexpected/);
assert.throws(() => new TerminalStreamReconciler().receiveControl({ type: "terminal_snapshot_start", bytes: -1 }), /Invalid/);
assert.throws(
  () => new TerminalStreamReconciler().receiveControl({ type: "terminal_snapshot_start", bytes: MAX_TERMINAL_SNAPSHOT_BYTES + 1 }),
  /Invalid/,
);
const incomplete = new TerminalStreamReconciler();
incomplete.receiveControl({ type: "terminal_snapshot_start", bytes: 2 });
incomplete.receiveBinary(encoder.encode("a"));
assert.throws(() => incomplete.receiveControl({ type: "terminal_snapshot_end" }), /Incomplete/);
const oversized = new TerminalStreamReconciler();
oversized.receiveControl({ type: "terminal_snapshot_start", bytes: 1 });
assert.throws(() => oversized.receiveBinary(encoder.encode("ab")), /exceeded/);

console.log("terminal reconnect stream logic passed");

