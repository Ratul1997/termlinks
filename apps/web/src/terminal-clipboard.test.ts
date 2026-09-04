import assert from "node:assert/strict";
import { insertClipboardText, terminalPasteInput } from "./terminal-clipboard";

assert.equal(terminalPasteInput("echo ready", false), "echo ready");
assert.equal(terminalPasteInput("one\ntwo\r\nthree", false), "one\rtwo\rthree");
assert.equal(
  terminalPasteInput("printf ready", true),
  "\u001b[200~printf ready\u001b[201~",
);
assert.equal(terminalPasteInput("", false), "");

assert.deepEqual(insertClipboardText("echo now", "hello", 5, 8), {
  value: "echo hello",
  caret: 10,
});
assert.deepEqual(insertClipboardText("abc", "x\ny", 99, 100), {
  value: "abcx\ny",
  caret: 6,
});

console.log("terminal clipboard behavior passed");
