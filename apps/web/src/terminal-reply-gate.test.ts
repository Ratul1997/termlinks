import assert from "node:assert/strict";
import { TerminalReplyGate } from "./terminal-reply-gate";

const encoder = new TextEncoder();
const decoder = new TextDecoder();

const live = new TerminalReplyGate();
assert.equal(decoder.decode(live.receive(encoder.encode("live"))), "live");

const replay = new TerminalReplyGate();
const generation = replay.beginSnapshot();
for (let index = 0; index < 400_000; index += 1) {
  assert.equal(replay.receive(encoder.encode(`\u001b[?${index % 80 + 1};1R`)), undefined);
}
assert.equal(decoder.decode(replay.finishSnapshot(generation)), "\u001b[?80;1R");
assert.equal(decoder.decode(replay.receive(encoder.encode("next"))), "next");

const superseded = new TerminalReplyGate();
const oldGeneration = superseded.beginSnapshot();
superseded.receive(encoder.encode("old"));
const currentGeneration = superseded.beginSnapshot();
superseded.receive(encoder.encode("current"));
assert.equal(superseded.finishSnapshot(oldGeneration), undefined);
assert.equal(decoder.decode(superseded.finishSnapshot(currentGeneration)), "current");

const oversized = new TerminalReplyGate();
const oversizedGeneration = oversized.beginSnapshot();
oversized.receive(new Uint8Array(4097));
assert.equal(oversized.finishSnapshot(oversizedGeneration), undefined);

const cancelled = new TerminalReplyGate();
const cancelledGeneration = cancelled.beginSnapshot();
cancelled.receive(encoder.encode("stale"));
cancelled.reset();
assert.equal(cancelled.finishSnapshot(cancelledGeneration), undefined);

console.log("terminal snapshot reply gating passed");
