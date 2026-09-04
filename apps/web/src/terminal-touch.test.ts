import assert from "node:assert/strict";
import { binaryStringToBytes, consumeTouchWheel } from "./terminal-touch";

const upward = consumeTouchWheel(200, 130, 0, 20);
assert.deepEqual(upward.directions, [1, 1, 1]);
assert.equal(upward.remainder, 10);

const downward = consumeTouchWheel(100, 145, 0, 20);
assert.deepEqual(downward.directions, [-1, -1]);
assert.equal(downward.remainder, -5);

const partial = consumeTouchWheel(100, 92, 0, 20);
assert.deepEqual(partial.directions, []);
assert.equal(partial.remainder, 8);
const completed = consumeTouchWheel(92, 78, partial.remainder, 20);
assert.deepEqual(completed.directions, [1]);
assert.equal(completed.remainder, 2);

const bounded = consumeTouchWheel(0, -1e12, 0, 20);
assert.equal(bounded.directions.length, 12);
assert.equal(bounded.remainder, 0);

assert.deepEqual(consumeTouchWheel(0, Number.NaN, 10, 20), { directions: [], remainder: 0 });

assert.deepEqual([...binaryStringToBytes("\u0000\u007f\u0080\u00ff")], [0, 127, 128, 255]);

console.log("terminal TUI touch navigation logic passed");
