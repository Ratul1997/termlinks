import assert from "node:assert/strict";
import { terminalPasteInput } from "./terminal-clipboard";

assert.equal(terminalPasteInput("echo ready", false), "echo ready");
assert.equal(terminalPasteInput("one\ntwo\r\nthree", false), "one\rtwo\rthree");
assert.equal(
  terminalPasteInput("printf ready", true),
  "\u001b[200~printf ready\u001b[201~",
);
assert.equal(terminalPasteInput("", false), "");

console.log("terminal clipboard behavior passed");
