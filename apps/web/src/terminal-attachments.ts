export type AttachmentInsertion = {
  value: string;
  caret: number;
};

export function shellQuotePath(path: string): string {
  return `'${path.replaceAll("'", `'\\''`)}'`;
}

export function insertAttachmentPath(
  value: string,
  path: string,
  selectionStart: number,
  selectionEnd: number,
): AttachmentInsertion {
  const start = Math.max(0, Math.min(selectionStart, value.length));
  const end = Math.max(start, Math.min(selectionEnd, value.length));
  const prefix = start > 0 && !/\s$/.test(value.slice(0, start)) ? " " : "";
  const suffix = end < value.length && !/^\s/.test(value.slice(end)) ? " " : "";
  const inserted = `${prefix}${shellQuotePath(path)}${suffix}`;
  return {
    value: `${value.slice(0, start)}${inserted}${value.slice(end)}`,
    caret: start + inserted.length,
  };
}
