import type { Terminal } from "@xterm/xterm";

export function terminalBufferText(terminal: Terminal, first = 0, last = terminal.buffer.active.length): string {
  const buffer = terminal.buffer.active;
  let value = "";
  for (let index = Math.max(0, first); index < Math.min(last, buffer.length); index += 1) {
    const line = buffer.getLine(index);
    if (!line) continue;
    if (value && !line.isWrapped) value += "\n";
    value += line.translateToString(true);
  }
  return value;
}

export function terminalVisibleText(terminal: Terminal): string {
  const buffer = terminal.buffer.active;
  return terminalBufferText(terminal, buffer.viewportY, buffer.viewportY + terminal.rows);
}
