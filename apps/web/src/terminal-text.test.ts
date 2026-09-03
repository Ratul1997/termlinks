import { strict as assert } from "node:assert";
import { Terminal } from "@xterm/xterm";
import { terminalBufferText } from "./terminal-text";

Object.defineProperty(globalThis, "self", { value: globalThis, configurable: true });

const terminal = new Terminal({ cols: 24, rows: 4, scrollback: 20 });
await new Promise<void>((resolve) => {
  terminal.write("\u001b[32mreal output\u001b[0m\r\nworking\rcomplete\r\n$ ", resolve);
});

const text = terminalBufferText(terminal).replace(/[ \t]+$/gm, "").replace(/\n+$/, "");
assert.equal(text, "real output\ncomplete\n$");
assert.equal(terminal.cols, 24);
assert.equal(terminal.rows, 4);
terminal.dispose();

console.log("wrapped terminal text projection passed");
