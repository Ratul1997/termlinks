export type TerminalInputMode = "compose" | "direct";

export function parseTerminalInputMode(value: string | null | undefined): TerminalInputMode {
  return value === "direct" ? "direct" : "compose";
}

export function nextTerminalInputMode(value: TerminalInputMode): TerminalInputMode {
  return value === "direct" ? "compose" : "direct";
}

export function terminalInputModeForAttachment(_value: TerminalInputMode): TerminalInputMode {
  return "compose";
}
