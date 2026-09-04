import assert from "node:assert/strict";
import { nextTerminalInputMode, parseTerminalInputMode, terminalInputModeForAttachment } from "./terminal-input-mode";

assert.equal(parseTerminalInputMode("direct"), "direct");
assert.equal(parseTerminalInputMode("compose"), "compose");
assert.equal(parseTerminalInputMode("unexpected"), "compose");
assert.equal(parseTerminalInputMode(null), "compose");
assert.equal(nextTerminalInputMode("compose"), "direct");
assert.equal(nextTerminalInputMode("direct"), "compose");
assert.equal(terminalInputModeForAttachment("compose"), "compose");
assert.equal(terminalInputModeForAttachment("direct"), "compose");

console.log("terminal input mode logic passed");
