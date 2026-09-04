export type TextInsertion = {
  value: string;
  caret: number;
};

/** Match native terminal paste semantics without adding an implicit Enter. */
export function terminalPasteInput(value: string, bracketedPasteMode: boolean): string {
  const normalized = value.replace(/\r?\n/g, "\r");
  return bracketedPasteMode ? `\u001b[200~${normalized}\u001b[201~` : normalized;
}

/** Insert clipboard text exactly at a composer's current selection. */
export function insertClipboardText(
  value: string,
  clipboard: string,
  selectionStart: number,
  selectionEnd: number,
): TextInsertion {
  const start = Math.max(0, Math.min(selectionStart, value.length));
  const end = Math.max(start, Math.min(selectionEnd, value.length));
  return {
    value: `${value.slice(0, start)}${clipboard}${value.slice(end)}`,
    caret: start + clipboard.length,
  };
}
