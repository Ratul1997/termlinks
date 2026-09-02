import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import "./style.css";

type Session = {
  id: string;
  name: string;
  command: string[];
  cwd: string;
  startedAt: string;
  endedAt?: string;
  running: boolean;
  exitCode?: number;
  rows: number;
  cols: number;
};

type StatusMessage = {
  type: "status";
  running: boolean;
  exitCode?: number;
};

const app = document.querySelector<HTMLDivElement>("#app")!;

const state: {
  authenticated: boolean;
  sessions: Session[];
  selected?: string;
  socket?: WebSocket;
  terminal?: Terminal;
  fit?: FitAddon;
  polling?: number;
} = { authenticated: false, sessions: [] };

const el = <K extends keyof HTMLElementTagNameMap>(tag: K, className?: string, text?: string): HTMLElementTagNameMap[K] => {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
};

async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(path, {
    ...init,
    headers: { "Content-Type": "application/json", ...init.headers },
  });
  if (response.status === 401) {
    state.authenticated = false;
    closeConnection();
    renderLogin();
    throw new Error("Your portal session expired");
  }
  if (!response.ok) {
    const body = (await response.json().catch(() => ({}))) as { error?: string };
    throw new Error(body.error || `Request failed (${response.status})`);
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

async function boot(): Promise<void> {
  try {
    await api<{ authenticated: boolean }>("/api/me");
    state.authenticated = true;
    await loadSessions();
    renderSessions();
  } catch {
    if (!state.authenticated) renderLogin();
  }
}

function renderLogin(message = ""): void {
  stopPolling();
  app.replaceChildren();
  const page = el("main", "login-page");
  const panel = el("section", "login-panel");
  const brand = el("div", "brand");
  brand.append(el("span", "brand-mark", ">_"), el("span", "brand-name", "termlinks"));
  panel.append(
    brand,
    el("p", "eyebrow", "PRIVATE TERMINAL BRIDGE"),
    el("h1", "login-title", "Your terminal, still running."),
    el("p", "login-copy", "Enter the token from your computer to open its managed terminal sessions."),
  );

  const form = el("form", "login-form");
  const label = el("label", "field-label", "Portal token");
  label.htmlFor = "token";
  const input = el("input", "token-input");
  input.id = "token";
  input.type = "password";
  input.autocomplete = "current-password";
  input.spellcheck = false;
  input.placeholder = "Paste your private token";
  input.required = true;
  const submit = el("button", "primary-button", "Unlock portal");
  submit.type = "submit";
  const error = el("p", "form-error", message);
  error.setAttribute("role", "alert");
  form.append(label, input, submit, error);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    submit.disabled = true;
    submit.textContent = "Checking…";
    error.textContent = "";
    try {
      await api("/api/login", { method: "POST", body: JSON.stringify({ token: input.value }) });
      input.value = "";
      state.authenticated = true;
      await loadSessions();
      renderSessions();
    } catch (caught) {
      error.textContent = caught instanceof Error ? caught.message : "Login failed";
      submit.disabled = false;
      submit.textContent = "Unlock portal";
      input.focus();
    }
  });
  panel.append(form, el("p", "login-hint", "On your computer: termlinks token"));
  page.append(panel);
  app.append(page);
  input.focus();
}

async function loadSessions(): Promise<void> {
  const response = await api<{ sessions: Session[] }>("/api/sessions");
  state.sessions = response.sessions;
}

function renderSessions(): void {
  closeConnection();
  state.selected = undefined;
  app.replaceChildren();
  const page = el("main", "dashboard");
  const header = el("header", "topbar");
  const brand = el("button", "brand brand-button");
  brand.type = "button";
  brand.append(el("span", "brand-mark", ">_"), el("span", "brand-name", "termlinks"));
  const status = el("div", "computer-status");
  status.id = "computer-status";
  status.append(el("span", "online-dot"), el("span", "computer-status-label", "Computer online"));
  const logout = el("button", "ghost-button", "Log out");
  logout.type = "button";
  logout.addEventListener("click", async () => {
    try {
      await api("/api/logout", { method: "POST" });
    } finally {
      state.authenticated = false;
      renderLogin();
    }
  });
  header.append(brand, status, logout);

  const heading = el("div", "dashboard-heading");
  const titleGroup = el("div");
  const summary = el("p", "session-summary", "Checking sessions…");
  summary.id = "session-summary";
  titleGroup.append(el("p", "eyebrow", "YOUR COMPUTER"), el("h1", "dashboard-title", "Terminal sessions"), summary);
  const refresh = el("button", "icon-button", "↻");
  refresh.type = "button";
  refresh.title = "Refresh sessions";
  refresh.setAttribute("aria-label", "Refresh sessions");
  refresh.addEventListener("click", () => refreshSessions(true));
  heading.append(titleGroup, refresh);

  const list = el("section", "session-list");
  list.id = "session-list";
  renderSessionCards(list);
  page.append(header, heading, list, renderStartHint());
  app.append(page);
  updateSessionSummary();
  startPolling();
}

function renderStartHint(): HTMLElement {
  const hint = el("aside", "start-hint");
  const icon = el("span", "hint-icon", "+");
  const copy = el("div");
  copy.append(
    el("strong", "hint-title", "Start sessions from your computer"),
    el("p", "hint-copy", "Use termlinks <command>. Remote command creation is disabled for safety."),
    el("code", "hint-command", "termlinks list  ·  termlinks stop <id>"),
  );
  hint.append(icon, copy);
  return hint;
}

function renderSessionCards(container: HTMLElement): void {
  container.replaceChildren();
  if (state.sessions.length === 0) {
    const empty = el("div", "empty-state");
    empty.append(el("span", "empty-prompt", "$_"), el("h2", "empty-title", "Nothing running yet"), el("p", "empty-copy", "Start a managed command on your computer and it will appear here."));
    container.append(empty);
    return;
  }
  for (const session of state.sessions) {
    const card = el("article", "session-card");
    const open = el("button", "session-open");
    open.type = "button";
    open.setAttribute("aria-label", `Open ${session.name} terminal`);
    open.addEventListener("click", () => renderTerminal(session.id));
    const row = el("div", "session-card-row");
    const identity = el("div", "session-identity");
    identity.append(el("span", `session-dot ${session.running ? "live" : "ended"}`), el("h2", "session-name", session.name));
    const badge = el("span", `status-badge ${session.running ? "running" : "finished"}`, session.running ? "RUNNING" : exitLabel(session));
    row.append(identity, badge);
    const command = el("code", "session-command", `$ ${session.command.join(" ")}`);
    const meta = el("div", "session-meta");
    meta.append(el("span", "cwd", compactPath(session.cwd)), el("span", "session-age", ageLabel(session)), el("span", "card-arrow", "›"));
    open.append(row, command, meta);

    const controls = el("div", "card-controls");
    const openAction = el("button", "card-action", "Open terminal");
    openAction.type = "button";
    openAction.addEventListener("click", () => renderTerminal(session.id));
    controls.append(openAction);
    if (session.running) {
      const stopAction = el("button", "card-action danger", "Stop & close");
      stopAction.type = "button";
      stopAction.addEventListener("click", () => stopFromDashboard(session, stopAction));
      controls.append(stopAction);
    }
    card.append(open, controls);
    container.append(card);
  }
}

async function stopFromDashboard(session: Session, button: HTMLButtonElement): Promise<void> {
  if (!window.confirm(`Stop and close “${session.name}”? The running command will be terminated.`)) return;
  button.disabled = true;
  button.textContent = "Stopping…";
  try {
    await api(`/api/sessions/${encodeURIComponent(session.id)}/stop`, { method: "POST" });
    window.setTimeout(() => refreshSessions(), 500);
  } catch (caught) {
    button.disabled = false;
    button.textContent = caught instanceof Error ? caught.message : "Could not stop";
  }
}

function updateSessionSummary(): void {
  const running = state.sessions.filter((session) => session.running).length;
  const finished = state.sessions.length - running;
  const summary = document.querySelector<HTMLElement>("#session-summary");
  if (summary) summary.textContent = `${running} running · ${finished} finished`;
  const status = document.querySelector<HTMLElement>("#computer-status .computer-status-label");
  if (status) status.textContent = `Computer online · ${running} running`;
}

async function refreshSessions(showMotion = false): Promise<void> {
  try {
    await loadSessions();
    const container = document.querySelector<HTMLElement>("#session-list");
    if (container) renderSessionCards(container);
    updateSessionSummary();
    if (showMotion) {
      document.querySelector<HTMLElement>(".icon-button")?.animate(
        [{ transform: "rotate(0)" }, { transform: "rotate(360deg)" }],
        { duration: 350 },
      );
    }
  } catch {
    // api() handles expired sessions; transient polling errors keep the current view.
  }
}

function startPolling(): void {
  stopPolling();
  state.polling = window.setInterval(() => refreshSessions(), 2500);
}

function stopPolling(): void {
  if (state.polling !== undefined) window.clearInterval(state.polling);
  state.polling = undefined;
}

function renderTerminal(id: string): void {
  stopPolling();
  closeConnection();
  const session = state.sessions.find((item) => item.id === id);
  if (!session) return renderSessions();
  state.selected = id;
  app.replaceChildren();
  const page = el("main", "terminal-page");
  const header = el("header", "terminal-header");
  const back = el("button", "back-button", "‹");
  back.type = "button";
  back.setAttribute("aria-label", "Back to sessions");
  back.addEventListener("click", renderSessions);
  const identity = el("div", "terminal-identity");
  identity.append(el("strong", "terminal-name", session.name), el("span", "terminal-subtitle", shortCommand(session.command)));
  const menu = el("button", "icon-button", "•••");
  menu.type = "button";
  menu.title = "Session actions";
  menu.addEventListener("click", () => actions.classList.toggle("open"));
  header.append(back, identity, menu);

  const actions = el("div", "actions-menu");
  const reconnect = el("button", "menu-button", "Reconnect");
  reconnect.type = "button";
  reconnect.addEventListener("click", () => connectTerminal(session));
  const stop = el("button", "menu-button danger", "Stop & close session");
  stop.type = "button";
  stop.disabled = !session.running;
  stop.addEventListener("click", async () => {
    actions.classList.remove("open");
    if (!window.confirm(`Stop “${session.name}”?`)) return;
    try {
      await api(`/api/sessions/${encodeURIComponent(session.id)}/stop`, { method: "POST" });
      setConnectionState("Stopping…", "warning");
    } catch (caught) {
      setConnectionState(caught instanceof Error ? caught.message : "Could not stop", "offline");
    }
  });
  actions.append(reconnect, stop);

  const connection = el("div", "connection-bar");
  connection.id = "connection-state";
  connection.append(el("span", "connection-dot"), el("span", "connection-label", "Connecting…"));
  const frame = el("section", "terminal-frame");
  const mount = el("div", "terminal-mount");
  mount.id = "terminal";
  frame.append(mount);
  page.append(header, actions, connection, frame, renderExtraKeys());
  app.append(page);

  const terminal = new Terminal({
    cursorBlink: true,
    cursorStyle: "bar",
    fontFamily: '"SFMono-Regular", "Cascadia Code", "Liberation Mono", monospace',
    fontSize: window.innerWidth < 600 ? 13 : 14,
    lineHeight: 1.18,
    scrollback: 10_000,
    theme: {
      background: "#070a09", foreground: "#d8e2dc", cursor: "#9fffb9", cursorAccent: "#070a09",
      selectionBackground: "#335c4166", black: "#111715", brightBlack: "#66716b",
      green: "#74e497", brightGreen: "#9fffb9", cyan: "#79d7c7",
    },
  });
  const fit = new FitAddon();
  terminal.loadAddon(fit);
  terminal.open(mount);
  state.terminal = terminal;
  state.fit = fit;
  fitTerminal();
  terminal.focus();
  terminal.onData((data) => {
    if (state.socket?.readyState === WebSocket.OPEN) state.socket.send(new TextEncoder().encode(data));
  });
  connectTerminal(session);
  const resize = new ResizeObserver(fitTerminal);
  resize.observe(frame);
  window.addEventListener("orientationchange", fitTerminal, { once: true });
}

function renderExtraKeys(): HTMLElement {
  const bar = el("div", "extra-keys");
  const keys: Array<[string, string]> = [
    ["\u001b", "Esc"], ["\t", "Tab"], ["\u0003", "Ctrl C"], ["\u0004", "Ctrl D"],
    ["\u001b[A", "↑"], ["\u001b[B", "↓"], ["\u001b[D", "←"], ["\u001b[C", "→"], ["\r", "Enter"],
  ];
  for (const [value, label] of keys) {
    const button = el("button", "key-button", label);
    button.type = "button";
    button.addEventListener("pointerdown", (event) => event.preventDefault());
    button.addEventListener("click", () => {
      if (state.socket?.readyState === WebSocket.OPEN) state.socket.send(new TextEncoder().encode(value));
      state.terminal?.focus();
    });
    bar.append(button);
  }
  return bar;
}

function connectTerminal(session: Session): void {
  state.socket?.close();
  state.terminal?.reset();
  setConnectionState("Connecting…", "connecting");
  const scheme = location.protocol === "https:" ? "wss:" : "ws:";
  const socket = new WebSocket(`${scheme}//${location.host}/ws/sessions/${encodeURIComponent(session.id)}`);
  socket.binaryType = "arraybuffer";
  state.socket = socket;
  socket.addEventListener("open", () => {
    if (state.socket !== socket) return;
    setConnectionState(session.running ? "Live · input enabled" : "Session output", session.running ? "online" : "offline");
    fitTerminal();
  });
  socket.addEventListener("message", async (event) => {
    if (state.socket !== socket) return;
    if (typeof event.data === "string") {
      try {
        const message = JSON.parse(event.data) as StatusMessage;
        if (message.type === "status" && !message.running) {
          session.running = false;
          session.exitCode = message.exitCode;
          setConnectionState(message.exitCode === 0 ? "Exited successfully" : `Exited · code ${message.exitCode ?? "?"}`, "offline");
        }
      } catch { /* Ignore unknown text control messages. */ }
      return;
    }
    const data = event.data instanceof Blob ? await event.data.arrayBuffer() : event.data as ArrayBuffer;
    state.terminal?.write(new Uint8Array(data));
  });
  socket.addEventListener("close", (event) => {
    if (state.socket !== socket) return;
    if (event.code === 1008) {
      state.authenticated = false;
      renderLogin("Your portal session expired");
      return;
    }
    if (session.running) setConnectionState("Disconnected · tap ••• to reconnect", "offline");
  });
  socket.addEventListener("error", () => {
    if (state.socket === socket) setConnectionState("Connection failed", "offline");
  });
}

function fitTerminal(): void {
  if (!state.fit || !state.terminal) return;
  try {
    state.fit.fit();
    if (state.socket?.readyState === WebSocket.OPEN) {
      state.socket.send(JSON.stringify({ type: "resize", cols: state.terminal.cols, rows: state.terminal.rows }));
    }
  } catch { /* A resize may be queued while changing views. */ }
}

function setConnectionState(label: string, kind: "connecting" | "online" | "offline" | "warning"): void {
  const bar = document.querySelector<HTMLElement>("#connection-state");
  if (!bar) return;
  bar.className = `connection-bar ${kind}`;
  const text = bar.querySelector<HTMLElement>(".connection-label");
  if (text) text.textContent = label;
}

function closeConnection(): void {
  if (state.socket) {
    state.socket.onclose = null;
    state.socket.close();
  }
  state.socket = undefined;
  state.terminal?.dispose();
  state.terminal = undefined;
  state.fit = undefined;
}

function compactPath(value: string): string {
  const parts = value.split("/").filter(Boolean);
  return parts.length <= 2 ? value : `…/${parts.slice(-2).join("/")}`;
}

function shortCommand(command: string[]): string {
  const joined = command.join(" ");
  return joined.length > 45 ? `${joined.slice(0, 44)}…` : joined;
}

function ageLabel(session: Session): string {
  const start = new Date(session.startedAt).getTime();
  const end = session.endedAt ? new Date(session.endedAt).getTime() : Date.now();
  const seconds = Math.max(0, Math.floor((end - start) / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ${minutes % 60}m`;
  return `${Math.floor(hours / 24)}d ${hours % 24}h`;
}

function exitLabel(session: Session): string {
  return session.exitCode === 0 ? "DONE" : `EXIT ${session.exitCode ?? "?"}`;
}

void boot();
