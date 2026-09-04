/** Match native terminal paste semantics without adding an implicit Enter. */
export function terminalPasteInput(value: string, bracketedPasteMode: boolean): string {
  const normalized = value.replace(/\r?\n/g, "\r");
  return bracketedPasteMode ? `\u001b[200~${normalized}\u001b[201~` : normalized;
}
