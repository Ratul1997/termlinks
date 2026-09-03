export const MAX_TERMINAL_NAME_LENGTH = 80;
export const MAX_TERMINAL_CWD_LENGTH = 4096;
export const MAX_SAVED_TERMINALS = 110;

export type SavedTerminal = {
  id: string;
  sourceSessionId: string;
  activeSessionId?: string;
  name: string;
  cwd: string;
  favorite: boolean;
  createdAt: string;
  updatedAt: string;
  lastOpenedAt: string;
  lastClosedAt?: string;
};

export function decodeTerminalHistory(value: unknown): SavedTerminal[] {
  if (!isRecord(value) || !Array.isArray(value.terminals) || value.terminals.length > MAX_SAVED_TERMINALS) {
    throw new Error("Your computer returned invalid terminal history");
  }
  return value.terminals.map((item) => {
    if (!isRecord(item) || !validID(item.id) || !validID(item.sourceSessionId) ||
      (item.activeSessionId !== undefined && !validID(item.activeSessionId)) ||
      typeof item.name !== "string" || item.name.length < 1 || item.name.length > MAX_TERMINAL_NAME_LENGTH ||
      typeof item.cwd !== "string" || item.cwd.length < 1 || item.cwd.length > MAX_TERMINAL_CWD_LENGTH ||
      typeof item.favorite !== "boolean" || !validDate(item.createdAt) || !validDate(item.updatedAt) ||
      !validDate(item.lastOpenedAt) || (item.lastClosedAt !== undefined && !validDate(item.lastClosedAt))) {
      throw new Error("Your computer returned invalid terminal history");
    }
    const decoded: SavedTerminal = {
      id: item.id,
      sourceSessionId: item.sourceSessionId,
      name: item.name,
      cwd: item.cwd,
      favorite: item.favorite,
      createdAt: item.createdAt,
      updatedAt: item.updatedAt,
      lastOpenedAt: item.lastOpenedAt,
    };
    if (item.activeSessionId !== undefined) decoded.activeSessionId = item.activeSessionId;
    if (item.lastClosedAt !== undefined) decoded.lastClosedAt = item.lastClosedAt;
    return decoded;
  });
}

export function savedSessionID(saved: SavedTerminal): string {
  return saved.activeSessionId || saved.sourceSessionId;
}

export function savedActivityTime(saved: SavedTerminal): number {
  return Math.max(
    Date.parse(saved.createdAt) || 0,
    Date.parse(saved.updatedAt) || 0,
    Date.parse(saved.lastOpenedAt) || 0,
    saved.lastClosedAt ? Date.parse(saved.lastClosedAt) || 0 : 0,
  );
}

export function visibleSavedGroups(items: SavedTerminal[], runningSessionIDs: Set<string>): { favorites: SavedTerminal[]; recent: SavedTerminal[] } {
  const visible = items.filter((item) => !runningSessionIDs.has(savedSessionID(item)));
  const byActivity = (left: SavedTerminal, right: SavedTerminal): number => savedActivityTime(right) - savedActivityTime(left);
  return {
    favorites: visible.filter((item) => item.favorite).sort(byActivity),
    recent: visible.filter((item) => !item.favorite).sort(byActivity),
  };
}

export function duplicateTerminalName(name: string): string {
  const base = name.trim() || "Terminal";
  const suffix = " Copy";
  if (base.length + suffix.length <= MAX_TERMINAL_NAME_LENGTH) return `${base}${suffix}`;
  return `${base.slice(0, MAX_TERMINAL_NAME_LENGTH - suffix.length).trimEnd()}${suffix}`;
}

export function projectLabel(cwd: string): string {
  const trimmed = cwd.trim().replace(/[\\/]+$/, "");
  if (!trimmed || trimmed === "/") return "/";
  if (/^[a-z]:$/i.test(trimmed)) return `${trimmed}\\`;
  return trimmed.split(/[\\/]/).filter(Boolean).pop() ?? trimmed;
}

function validID(value: unknown): value is string {
  return typeof value === "string" && /^[0-9a-f]{32}$/.test(value);
}

function validDate(value: unknown): value is string {
  return typeof value === "string" && Number.isFinite(Date.parse(value));
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
