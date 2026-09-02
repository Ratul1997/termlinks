import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import RFB from "@novnc/novnc";
import "@xterm/xterm/css/xterm.css";
import { base64URLToBytes, bytesToBase64URL, decryptPacket, deriveEncryptionKey, encryptPacket } from "./e2e";
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

type BeforeInstallPromptEvent = Event & {
  prompt: () => Promise<void>;
  userChoice: Promise<{ outcome: "accepted" | "dismissed"; platform: string }>;
};

const app = document.querySelector<HTMLDivElement>("#app")!;

type TerminalLink = {
  readonly readyState: number;
  send(data: string | ArrayBuffer | ArrayBufferView): void;
  close(): void;
};

type TerminalCallbacks = {
  open: () => void;
  message: (data: string | ArrayBuffer) => void;
  close: (code: number) => void;
  error: () => void;
};

type RawDesktopChannel = {
  binaryType: BinaryType;
  onerror: ((event: Event) => void) | null;
  onmessage: ((event: MessageEvent<ArrayBuffer>) => void) | null;
  onopen: ((event: Event) => void) | null;
  onclose: ((event: CloseEvent) => void) | null;
  protocol: string;
  readyState: number;
  send(data: string | ArrayBuffer | ArrayBufferView): void;
  close(): void;
};

const state: {
  authenticated: boolean;
  sessions: Session[];
  selected?: string;
  socket?: TerminalLink;
  terminal?: Terminal;
  fit?: FitAddon;
  touchCleanup?: () => void;
  desktop?: RFB;
  desktopLink?: EncryptedDesktopLink;
  polling?: number;
  closedSessions: Set<string>;
} = { authenticated: false, sessions: [], closedSessions: new Set() };

const encryptedPortal = location.hostname === "termlinks.pages.dev" || location.hostname.endsWith(".termlinks.pages.dev");
let encryptedBridge: EncryptedBridge | undefined;
let installPrompt: BeforeInstallPromptEvent | undefined;

window.addEventListener("beforeinstallprompt", (event) => {
  event.preventDefault();
  installPrompt = event as BeforeInstallPromptEvent;
  syncInstallButtons();
});
window.addEventListener("appinstalled", () => {
  installPrompt = undefined;
  syncInstallButtons();
});

class EncryptedTerminalLink implements TerminalLink {
  readyState: number = WebSocket.CONNECTING;

  constructor(
    readonly id: string,
    private readonly bridge: EncryptedBridge,
    private readonly callbacks: TerminalCallbacks,
  ) {}

  markOpen(): void {
    if (this.readyState !== WebSocket.CONNECTING) return;
    this.readyState = WebSocket.OPEN;
    this.callbacks.open();
  }

  receive(data: string | ArrayBuffer): void {
    if (this.readyState === WebSocket.OPEN) this.callbacks.message(data);
  }

  remoteClose(code: number): void {
    if (this.readyState === WebSocket.CLOSED) return;
    this.readyState = WebSocket.CLOSED;
    this.callbacks.close(code);
  }

  fail(): void {
    if (this.readyState === WebSocket.CLOSED) return;
    this.callbacks.error();
    this.remoteClose(1011);
  }

  send(data: string | ArrayBuffer | ArrayBufferView): void {
    if (this.readyState !== WebSocket.OPEN) return;
    void this.bridge.sendTerminalData(this.id, data).catch(() => this.fail());
  }

  close(): void {
    if (this.readyState === WebSocket.CLOSED) return;
    this.readyState = WebSocket.CLOSING;
    void this.bridge.closeTerminal(this.id);
    this.readyState = WebSocket.CLOSED;
  }
}

class EncryptedDesktopLink implements RawDesktopChannel {
  binaryType: BinaryType = "arraybuffer";
  onerror: ((event: Event) => void) | null = null;
  onmessage: ((event: MessageEvent<ArrayBuffer>) => void) | null = null;
  onopen: ((event: Event) => void) | null = null;
  onclose: ((event: CloseEvent) => void) | null = null;
  protocol = "";
  readyState: number = WebSocket.CONNECTING;

  constructor(readonly id: string, private readonly bridge: EncryptedBridge) {}

  markOpen(): void {
    if (this.readyState !== WebSocket.CONNECTING) return;
    this.readyState = WebSocket.OPEN;
    this.onopen?.(new Event("open"));
  }

  receive(data: ArrayBuffer): void {
    if (this.readyState === WebSocket.OPEN) this.onmessage?.(new MessageEvent("message", { data }));
  }

  remoteClose(code: number, reason: string): void {
    if (this.readyState === WebSocket.CLOSED) return;
    this.readyState = WebSocket.CLOSED;
    this.onclose?.(new CloseEvent("close", { code, reason, wasClean: code === 1000 }));
  }

  fail(): void {
    if (this.readyState === WebSocket.CLOSED) return;
    this.onerror?.(new Event("error"));
    this.remoteClose(1011, "Encrypted desktop tunnel failed");
  }

  send(data: string | ArrayBuffer | ArrayBufferView): void {
    if (this.readyState !== WebSocket.OPEN) return;
    void this.bridge.sendDesktopData(this.id, data).catch(() => this.fail());
  }

  close(): void {
    if (this.readyState === WebSocket.CLOSED) return;
    this.readyState = WebSocket.CLOSING;
    void this.bridge.closeDesktop(this.id);
    this.readyState = WebSocket.CLOSED;
  }
}

class EncryptedBridge {
  private socket?: WebSocket;
  private key?: CryptoKey;
  private channel = "";
  private sendChain: Promise<void> = Promise.resolve();
  private receiveChain: Promise<void> = Promise.resolve();
  private sendSequence = 0;
  private receiveSequence = 0;
  private readonly requests = new Map<string, { resolve: (value: { status: number; body: string }) => void; reject: (error: Error) => void; timeout: number }>();
  private readonly terminals = new Map<string, EncryptedTerminalLink>();
  private readonly desktops = new Map<string, EncryptedDesktopLink>();
  private authResolve?: () => void;
  private authReject?: (error: Error) => void;
  private challenge = "";
  private failed = false;

  async connect(token: string): Promise<void> {
    this.key = await deriveEncryptionKey(token);
    const scheme = location.protocol === "https:" ? "wss:" : "ws:";
    const socket = new WebSocket(`${scheme}//${location.host}/ws/bridge`);
    this.socket = socket;
    const ready = await waitForBridge(socket);
    this.channel = ready.id;
    socket.addEventListener("message", (event) => {
      if (typeof event.data !== "string") return this.fail(new Error("The encrypted relay returned invalid data"));
      this.receiveChain = this.receiveChain.then(() => this.receive(event.data)).catch((error: unknown) => {
        this.fail(error instanceof Error ? error : new Error("Could not decrypt relay data"));
      });
    });
    socket.addEventListener("close", (event) => {
      const reason = event.code === 1008 ? "Invalid portal token" : "The computer connection closed";
      this.fail(new Error(reason));
    });
    socket.addEventListener("error", () => this.fail(new Error("Could not connect to your computer")));

    const challengeBytes = new Uint8Array(24);
    crypto.getRandomValues(challengeBytes);
    this.challenge = bytesToBase64URL(challengeBytes);
    const authenticated = new Promise<void>((resolve, reject) => {
      const timeout = window.setTimeout(() => reject(new Error("Encrypted login timed out")), 15_000);
      this.authResolve = () => { window.clearTimeout(timeout); resolve(); };
      this.authReject = (error) => { window.clearTimeout(timeout); reject(error); };
    });
    await this.sendEncrypted({ v: 1, type: "authenticate", challenge: this.challenge });
    await authenticated;
  }

  async request(method: string, path: string, body = ""): Promise<{ status: number; body: string }> {
    const id = crypto.randomUUID();
    const response = new Promise<{ status: number; body: string }>((resolve, reject) => {
      const timeout = window.setTimeout(() => {
        this.requests.delete(id);
        reject(new Error("Your computer did not respond"));
      }, 15_000);
      this.requests.set(id, { resolve, reject, timeout });
    });
    await this.sendEncrypted({ v: 1, type: "http_request", id, method, path, body });
    return response;
  }

  openTerminal(sessionId: string, callbacks: TerminalCallbacks): EncryptedTerminalLink {
    const link = new EncryptedTerminalLink(crypto.randomUUID(), this, callbacks);
    this.terminals.set(link.id, link);
    void this.sendEncrypted({ v: 1, type: "terminal_open", id: link.id, sessionId }).catch(() => link.fail());
    return link;
  }

  openDesktop(): EncryptedDesktopLink {
    const link = new EncryptedDesktopLink(crypto.randomUUID(), this);
    this.desktops.set(link.id, link);
    void this.sendEncrypted({ v: 1, type: "desktop_open", id: link.id }).catch(() => link.fail());
    return link;
  }

  async sendDesktopData(id: string, data: string | ArrayBuffer | ArrayBufferView): Promise<void> {
    if (typeof data === "string") throw new Error("Remote desktop data must be binary");
    const bytes = data instanceof ArrayBuffer
      ? new Uint8Array(data)
      : new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
    await this.sendEncrypted({ v: 1, type: "desktop_data", id, data: bytesToBase64URL(bytes) });
  }

  async closeDesktop(id: string): Promise<void> {
    this.desktops.delete(id);
    if (this.socket?.readyState === WebSocket.OPEN) {
      await this.sendEncrypted({ v: 1, type: "desktop_close", id, code: 1000, reason: "Viewer closed" });
    }
  }

  async sendTerminalData(id: string, data: string | ArrayBuffer | ArrayBufferView): Promise<void> {
    if (typeof data === "string") {
      await this.sendEncrypted({ v: 1, type: "terminal_data", id, binary: false, data });
      return;
    }
    const bytes = data instanceof ArrayBuffer
      ? new Uint8Array(data)
      : new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
    await this.sendEncrypted({ v: 1, type: "terminal_data", id, binary: true, data: bytesToBase64URL(bytes) });
  }

  async closeTerminal(id: string): Promise<void> {
    this.terminals.delete(id);
    if (this.socket?.readyState === WebSocket.OPEN) {
      await this.sendEncrypted({ v: 1, type: "terminal_close", id, code: 1000, reason: "Viewer closed" });
    }
  }

  close(): void {
    this.socket?.close(1000, "Portal closed");
    this.socket = undefined;
    this.fail(new Error("Portal closed"));
  }

  private async receive(packet: string): Promise<void> {
    const value = await this.decrypt(packet);
    if (!isRecord(value) || value.v !== 1 || typeof value.type !== "string") throw new Error("Invalid encrypted message");
    if (value.type === "authenticated") {
      if (value.challenge !== this.challenge) throw new Error("Encrypted login challenge did not match");
      this.authResolve?.();
      this.authResolve = undefined;
      this.authReject = undefined;
      return;
    }
    if (value.type === "http_response") {
      if (typeof value.id !== "string" || typeof value.status !== "number" || typeof value.body !== "string") throw new Error("Invalid encrypted API response");
      const pending = this.requests.get(value.id);
      if (!pending) return;
      window.clearTimeout(pending.timeout);
      this.requests.delete(value.id);
      pending.resolve({ status: value.status, body: value.body });
      return;
    }
    if (typeof value.id !== "string") throw new Error("Invalid encrypted stream response");
    const desktop = this.desktops.get(value.id);
    if (desktop) {
      if (value.type === "desktop_opened") {
        desktop.markOpen();
        return;
      }
      if (value.type === "desktop_data") {
        if (typeof value.data !== "string") throw new Error("Invalid encrypted desktop data");
        desktop.receive(Uint8Array.from(base64URLToBytes(value.data)).buffer);
        return;
      }
      if (value.type === "desktop_close") {
        this.desktops.delete(value.id);
        desktop.remoteClose(typeof value.code === "number" ? value.code : 1000, typeof value.reason === "string" ? value.reason : "Remote desktop closed");
        return;
      }
      throw new Error("Unsupported encrypted desktop message");
    }
    const terminal = this.terminals.get(value.id);
    if (!terminal) return;
    if (value.type === "terminal_opened") {
      terminal.markOpen();
      return;
    }
    if (value.type === "terminal_data") {
      if (typeof value.binary !== "boolean" || typeof value.data !== "string") throw new Error("Invalid encrypted terminal data");
      terminal.receive(value.binary ? Uint8Array.from(base64URLToBytes(value.data)).buffer : value.data);
      return;
    }
    if (value.type === "terminal_close") {
      this.terminals.delete(value.id);
      terminal.remoteClose(typeof value.code === "number" ? value.code : 1000);
      return;
    }
    throw new Error("Unsupported encrypted message");
  }

  private sendEncrypted(value: Record<string, unknown>): Promise<void> {
    this.sendChain = this.sendChain.then(async () => {
      if (!this.key || !this.channel || this.socket?.readyState !== WebSocket.OPEN) throw new Error("Encrypted portal is disconnected");
      const sequence = this.sendSequence;
      this.sendSequence += 1;
      this.socket.send(await encryptPacket(this.key, this.channel, "browser", sequence, value));
    });
    return this.sendChain;
  }

  private async decrypt(packet: string): Promise<unknown> {
    if (!this.key) throw new Error("Encryption key is unavailable");
    const value = await decryptPacket(this.key, this.channel, "connector", this.receiveSequence, packet);
    this.receiveSequence += 1;
    return value;
  }

  private fail(error: Error): void {
    if (this.failed) return;
    this.failed = true;
    if (this.socket && this.socket.readyState < WebSocket.CLOSING) this.socket.close(1008, "Encrypted connection failed");
    this.authReject?.(error);
    this.authResolve = undefined;
    this.authReject = undefined;
    for (const [id, pending] of this.requests) {
      window.clearTimeout(pending.timeout);
      pending.reject(error);
      this.requests.delete(id);
    }
    for (const terminal of this.terminals.values()) terminal.remoteClose(1012);
    this.terminals.clear();
    for (const desktop of this.desktops.values()) desktop.remoteClose(1012, error.message);
    this.desktops.clear();
    if (encryptedBridge === this && state.authenticated) {
      encryptedBridge = undefined;
      state.authenticated = false;
      window.queueMicrotask(() => renderLogin(error.message));
    }
  }
}

async function waitForBridge(socket: WebSocket): Promise<{ id: string }> {
  return new Promise((resolve, reject) => {
    const timeout = window.setTimeout(() => reject(new Error("Computer connection timed out")), 15_000);
    const cleanup = (): void => {
      window.clearTimeout(timeout);
      socket.removeEventListener("message", onMessage);
      socket.removeEventListener("close", onClose);
      socket.removeEventListener("error", onError);
    };
    const onMessage = (event: MessageEvent): void => {
      if (typeof event.data !== "string") return;
      try {
        const value: unknown = JSON.parse(event.data);
        if (!isRecord(value) || value.type !== "bridge_ready" || value.protocol !== "e2e-v1" || typeof value.id !== "string") return;
        cleanup();
        resolve({ id: value.id });
      } catch { /* Wait for a valid bridge greeting. */ }
    };
    const onClose = (): void => { cleanup(); reject(new Error("Your computer is offline")); };
    const onError = (): void => { cleanup(); reject(new Error("Could not reach your computer")); };
    socket.addEventListener("message", onMessage);
    socket.addEventListener("close", onClose);
    socket.addEventListener("error", onError);
  });
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function createInstallButton(): HTMLButtonElement {
  const button = el("button", "ghost-button install-button", "Install app");
  button.type = "button";
  button.hidden = !installPrompt;
  button.addEventListener("click", async () => {
    const prompt = installPrompt;
    if (!prompt) return;
    try {
      await prompt.prompt();
      await prompt.userChoice;
    } finally {
      installPrompt = undefined;
      syncInstallButtons();
    }
  });
  return button;
}

function syncInstallButtons(): void {
  for (const button of document.querySelectorAll<HTMLButtonElement>(".install-button")) {
    button.hidden = !installPrompt;
  }
}

const el = <K extends keyof HTMLElementTagNameMap>(tag: K, className?: string, text?: string): HTMLElementTagNameMap[K] => {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
};

async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  if (encryptedPortal) {
    if (!encryptedBridge) throw new Error("Encrypted portal is not connected");
    const method = init.method ?? "GET";
    const body = typeof init.body === "string" ? init.body : "";
    const response = await encryptedBridge.request(method, path, body);
    if (response.status === 401) {
      state.authenticated = false;
      closeConnection();
      const bridge = encryptedBridge;
      encryptedBridge = undefined;
      bridge?.close();
      renderLogin();
      throw new Error("Your portal session expired");
    }
    if (response.status < 200 || response.status >= 300) {
      let message = `Request failed (${response.status})`;
      try {
        const decoded: unknown = JSON.parse(response.body);
        if (isRecord(decoded) && typeof decoded.error === "string") message = decoded.error;
      } catch { /* Keep the HTTP status message. */ }
      throw new Error(message);
    }
    if (response.status === 204) return undefined as T;
    return JSON.parse(response.body) as T;
  }
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
  if (encryptedPortal) {
    renderLogin();
    return;
  }
  try {
    await api<{ authenticated: boolean }>("/api/me");
    state.authenticated = true;
    await loadSessions();
    renderSessions();
  } catch (caught) {
    if (!state.authenticated) {
      const message = caught instanceof Error && caught.message.includes("offline")
        ? "Your computer is offline. Start it with: termlinks cloud start"
        : "";
      renderLogin(message);
    }
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
    el("p", "eyebrow", encryptedPortal ? "END-TO-END ENCRYPTED BRIDGE" : "PRIVATE TERMINAL BRIDGE"),
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
      if (encryptedPortal) {
        encryptedBridge?.close();
        const bridge = new EncryptedBridge();
        await bridge.connect(input.value);
        encryptedBridge = bridge;
      } else {
        await api("/api/login", { method: "POST", body: JSON.stringify({ token: input.value }) });
      }
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
  panel.append(form, createInstallButton(), el("p", "login-hint", "On your computer: termlinks token · Install from your browser menu on iPhone/iPad"));
  page.append(panel);
  app.append(page);
  input.focus();
}

async function loadSessions(): Promise<void> {
  const response = await api<{ sessions: Session[] }>("/api/sessions");
  state.sessions = response.sessions.filter((session) => session.running && !state.closedSessions.has(session.id));
}

function renderSessions(): void {
  closeConnection();
  state.sessions = state.sessions.filter((session) => session.running && !state.closedSessions.has(session.id));
  state.selected = undefined;
  app.replaceChildren();
  const page = el("main", "dashboard");
  const header = el("header", "topbar");
  const brand = el("button", "brand brand-button");
  brand.type = "button";
  brand.append(el("span", "brand-mark", ">_"), el("span", "brand-name", "termlinks"));
  const status = el("div", "computer-status");
  status.id = "computer-status";
  status.append(el("span", "online-dot"), el("span", "computer-status-label", encryptedPortal ? "E2E · Computer online" : "Computer online"));
  const logout = el("button", "ghost-button", "Log out");
  logout.type = "button";
  logout.addEventListener("click", async () => {
    try {
      await api("/api/logout", { method: "POST" });
    } finally {
      state.authenticated = false;
      encryptedBridge?.close();
      encryptedBridge = undefined;
      renderLogin();
    }
  });
  header.append(brand, status, createInstallButton(), logout);

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
  const create = el("button", "new-terminal-button", "+ New terminal");
  create.type = "button";
  create.setAttribute("aria-expanded", "false");
  create.setAttribute("aria-controls", "create-terminal-panel");
  const headingActions = el("div", "dashboard-actions");
  if (encryptedPortal) {
    const desktop = el("button", "desktop-button", "▣ Remote desktop");
    desktop.type = "button";
    desktop.addEventListener("click", renderDesktop);
    headingActions.append(desktop);
  }
  headingActions.append(create, refresh);
  heading.append(titleGroup, headingActions);

  const createPanel = renderCreatePanel(() => {
    createPanel.hidden = true;
    create.setAttribute("aria-expanded", "false");
    create.focus();
  });
  create.addEventListener("click", () => {
    createPanel.hidden = !createPanel.hidden;
    create.setAttribute("aria-expanded", String(!createPanel.hidden));
    if (!createPanel.hidden) createPanel.querySelector<HTMLInputElement>("#new-session-name")?.focus();
  });
  const list = el("section", "session-list");
  list.id = "session-list";
  renderSessionCards(list);
  page.append(header, heading, createPanel, list, renderStartHint());
  app.append(page);
  updateSessionSummary();
  startPolling();
}

function renderDesktop(): void {
  stopPolling();
  closeConnection();
  if (!encryptedPortal || !encryptedBridge) {
    renderLogin("Remote desktop requires the encrypted cloud portal");
    return;
  }

  app.replaceChildren();
  const page = el("main", "desktop-page");
  const header = el("header", "desktop-header");
  const back = el("button", "back-button", "‹");
  back.type = "button";
  back.setAttribute("aria-label", "Back to sessions");
  back.addEventListener("click", renderSessions);
  const identity = el("div", "terminal-identity");
  identity.append(el("strong", "terminal-name", "Remote desktop"), el("span", "terminal-subtitle", "Full Mac screen · E2E tunnel"));
  const fullscreen = el("button", "desktop-header-action", "Full screen");
  fullscreen.type = "button";
  fullscreen.addEventListener("click", async () => {
    try {
      const target = page as HTMLElement & { webkitRequestFullscreen?: () => Promise<void> | void };
      if (target.requestFullscreen) await target.requestFullscreen();
      else if (target.webkitRequestFullscreen) await target.webkitRequestFullscreen();
      else setConnectionState("Already using the largest view available on this browser", "warning");
    } catch {
      setConnectionState("Full screen was blocked; install the PWA for the largest iPhone view", "warning");
    }
  });
  header.append(back, identity, fullscreen);

  const connection = el("div", "connection-bar");
  connection.id = "connection-state";
  connection.append(el("span", "connection-dot"), el("span", "connection-label", "Opening encrypted desktop tunnel…"));
  const frame = el("section", "desktop-frame");
  const mount = el("div", "desktop-mount");
  mount.id = "remote-desktop";
  const empty = el("div", "desktop-waiting");
  empty.append(el("span", "desktop-waiting-icon", "▣"), el("span", "desktop-waiting-copy", "Waiting for your Mac…"));
  mount.append(empty);
  frame.append(mount);

  const controls = el("div", "desktop-controls");
  const control = el("button", "desktop-control primary-control", "Enable control");
  control.type = "button";
  const keyboard = el("button", "desktop-control", "Keyboard");
  keyboard.type = "button";
  const clipboard = el("button", "desktop-control", "Clipboard");
  clipboard.type = "button";
  controls.append(control, keyboard, clipboard);

  const typingPanel = el("section", "desktop-typing");
  typingPanel.hidden = true;
  const typingForm = el("form", "desktop-typing-form");
  const typingInput = el("input", "desktop-typing-input");
  typingInput.type = "text";
  typingInput.placeholder = "Type text on the Mac";
  typingInput.autocomplete = "off";
  typingInput.autocapitalize = "off";
  typingInput.spellcheck = false;
  typingInput.disabled = true;
  const sendText = el("button", "desktop-type-send", "Send");
  sendText.type = "submit";
  sendText.disabled = true;
  typingForm.append(typingInput, sendText);
  const keys = el("div", "desktop-keys");
  const specialKeys: Array<[string, number, string]> = [
    ["Esc", 0xff1b, "Escape"], ["Tab", 0xff09, "Tab"], ["⌫", 0xff08, "Backspace"],
    ["←", 0xff51, "ArrowLeft"], ["↑", 0xff52, "ArrowUp"], ["↓", 0xff54, "ArrowDown"], ["→", 0xff53, "ArrowRight"], ["Enter", 0xff0d, "Enter"],
  ];
  for (const [label, keysym, code] of specialKeys) {
    const button = el("button", "desktop-key", label);
    button.type = "button";
    button.disabled = true;
    button.addEventListener("click", () => state.desktop?.sendKey(keysym, code));
    keys.append(button);
  }
  typingPanel.append(typingForm, keys);

  const credentials = el("section", "desktop-credentials");
  credentials.hidden = true;
  const credentialForm = el("form", "desktop-credential-card");
  credentialForm.append(el("h2", "desktop-credential-title", "Mac Screen Sharing login"), el("p", "desktop-credential-copy", "Enter the VNC or Mac credentials requested by your Mac. They stay in this page and are not saved."));
  const username = el("input", "desktop-credential-input");
  username.name = "username";
  username.placeholder = "Mac username (if requested)";
  username.autocomplete = "username";
  const password = el("input", "desktop-credential-input");
  password.name = "password";
  password.type = "password";
  password.placeholder = "Screen Sharing password";
  password.autocomplete = "current-password";
  password.required = true;
  const credentialSubmit = el("button", "desktop-credential-submit", "Connect securely");
  credentialSubmit.type = "submit";
  credentialForm.append(username, password, credentialSubmit);
  credentials.append(credentialForm);
  frame.append(credentials);
  page.append(header, connection, frame, controls, typingPanel);
  app.append(page);

  let controlEnabled = false;
  control.addEventListener("click", () => {
    const rfb = state.desktop;
    if (!rfb) return;
    controlEnabled = !controlEnabled;
    rfb.viewOnly = !controlEnabled;
    control.textContent = controlEnabled ? "Control enabled" : "Enable control";
    control.classList.toggle("enabled", controlEnabled);
    typingInput.disabled = !controlEnabled;
    sendText.disabled = !controlEnabled;
    for (const key of keys.querySelectorAll<HTMLButtonElement>("button")) key.disabled = !controlEnabled;
    setConnectionState(controlEnabled ? "E2E · Live · touch and keyboard control enabled" : "E2E · Live · view only", "online");
  });
  keyboard.addEventListener("click", () => {
    typingPanel.hidden = !typingPanel.hidden;
    if (!typingPanel.hidden) typingInput.focus();
  });
  clipboard.addEventListener("click", () => {
    if (!controlEnabled || !state.desktop) {
      setConnectionState("Enable control before sending clipboard text", "warning");
      return;
    }
    const text = window.prompt("Text to copy into the Mac clipboard");
    if (text !== null) state.desktop.clipboardPasteFrom(text);
  });
  typingForm.addEventListener("submit", (event) => {
    event.preventDefault();
    if (!controlEnabled || !state.desktop || !typingInput.value) return;
    sendDesktopText(state.desktop, typingInput.value);
    typingInput.value = "";
    typingInput.focus();
  });

  const link = encryptedBridge.openDesktop();
  state.desktopLink = link;
  try {
    const rfb = new RFB(mount, link, { shared: true });
    state.desktop = rfb;
    rfb.viewOnly = true;
    rfb.scaleViewport = true;
    rfb.resizeSession = false;
    rfb.clipViewport = false;
    rfb.qualityLevel = 6;
    rfb.compressionLevel = 6;
    rfb.addEventListener("connect", () => {
      if (state.desktop !== rfb) return;
      empty.remove();
      credentials.hidden = true;
      setConnectionState("E2E · Live · view only", "online");
    });
    rfb.addEventListener("credentialsrequired", (event) => {
      if (state.desktop !== rfb) return;
      const detail = (event as CustomEvent<{ types?: string[] }>).detail;
      username.hidden = !detail?.types?.includes("username");
      password.required = detail?.types?.includes("password") ?? true;
      credentials.hidden = false;
      setConnectionState("Mac authentication required", "warning");
      (username.hidden ? password : username).focus();
    });
    rfb.addEventListener("securityfailure", (event) => {
      const detail = (event as CustomEvent<{ reason?: string }>).detail;
      password.value = "";
      credentials.hidden = false;
      setConnectionState(detail?.reason || "Screen Sharing rejected those credentials", "offline");
    });
    rfb.addEventListener("disconnect", (event) => {
      if (state.desktop !== rfb) return;
      const clean = (event as CustomEvent<{ clean?: boolean }>).detail?.clean;
      setConnectionState(clean ? "Remote desktop closed" : "Remote desktop disconnected", "offline");
    });
    credentialForm.addEventListener("submit", (event) => {
      event.preventDefault();
      credentialSubmit.disabled = true;
      rfb.sendCredentials({ username: username.value, password: password.value });
      password.value = "";
      credentials.hidden = true;
      credentialSubmit.disabled = false;
      setConnectionState("Authenticating with your Mac…", "connecting");
    });
  } catch (caught) {
    link.close();
    setConnectionState(caught instanceof Error ? caught.message : "Could not start remote desktop", "offline");
  }
}

function sendDesktopText(rfb: RFB, text: string): void {
  for (const character of text) {
    const codePoint = character.codePointAt(0);
    if (codePoint === undefined) continue;
    const keysym = codePoint <= 0xff ? codePoint : 0x01000000 | codePoint;
    rfb.sendKey(keysym, null);
  }
}

function renderCreatePanel(close: () => void): HTMLElement {
  const panel = el("section", "create-panel");
  panel.id = "create-terminal-panel";
  panel.hidden = true;
  const intro = el("div", "create-intro");
  intro.append(
    el("h2", "create-title", "Open a new shell"),
    el("p", "create-copy", "This creates a normal interactive terminal. Once open, type cd, ls, codex, npm, or any other command."),
  );
  const form = el("form", "create-form");
  form.autocomplete = "off";

  const nameField = el("label", "create-field");
  nameField.append(el("span", "field-label", "Name (optional)"));
  const name = el("input", "create-input");
  name.id = "new-session-name";
  name.name = "name";
  name.maxLength = 80;
  name.placeholder = "e.g. project shell";
  nameField.append(name);

  const cwdField = el("label", "create-field");
  cwdField.append(el("span", "field-label", "Starting directory (optional)"));
  const cwd = el("input", "create-input");
  cwd.name = "cwd";
  cwd.maxLength = 4096;
  cwd.placeholder = "~ or /Volumes/MyWork/PH";
  cwd.spellcheck = false;
  cwdField.append(cwd);

  const error = el("p", "form-error");
  error.setAttribute("role", "alert");
  const actions = el("div", "create-actions");
  const cancel = el("button", "secondary-button", "Cancel");
  cancel.type = "button";
  cancel.addEventListener("click", close);
  const submit = el("button", "create-submit", "Create & open");
  submit.type = "submit";
  actions.append(cancel, submit);
  form.append(nameField, cwdField, error, actions);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    error.textContent = "";
    submit.disabled = true;
    submit.textContent = "Creating…";
    try {
      const created = await api<Session>("/api/sessions", {
        method: "POST",
        body: JSON.stringify({ name: name.value.trim(), cwd: cwd.value.trim() }),
      });
      state.sessions = [created, ...state.sessions.filter((item) => item.id !== created.id)];
      renderTerminal(created.id);
    } catch (caught) {
      error.textContent = caught instanceof Error ? caught.message : "Could not create the terminal";
      submit.disabled = false;
      submit.textContent = "Create & open";
    }
  });
  panel.append(intro, form);
  return panel;
}

function renderStartHint(): HTMLElement {
  const hint = el("aside", "start-hint");
  const icon = el("span", "hint-icon", "+");
  const copy = el("div");
  copy.append(
    el("strong", "hint-title", "Your shells stay managed"),
    el("p", "hint-copy", "Leaving this page only disconnects the viewer. A terminal disappears from this list after it exits or you stop it."),
    el("code", "hint-command", "termlinks list  ·  termlinks stop <id>"),
  );
  hint.append(icon, copy);
  return hint;
}

function renderSessionCards(container: HTMLElement): void {
  container.replaceChildren();
  if (state.sessions.length === 0) {
    const empty = el("div", "empty-state");
    empty.append(el("span", "empty-prompt", "$_"), el("h2", "empty-title", "Nothing running yet"), el("p", "empty-copy", "Tap New terminal to open an interactive shell on your computer."));
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
    identity.append(el("span", "session-dot live"), el("h2", "session-name", session.name));
    const badge = el("span", "status-badge running", "RUNNING");
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
    const stopAction = el("button", "card-action danger", "Stop & close");
    stopAction.type = "button";
    stopAction.addEventListener("click", () => stopFromDashboard(session, stopAction));
    controls.append(stopAction);
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
    state.closedSessions.add(session.id);
    state.sessions = state.sessions.filter((item) => item.id !== session.id);
    const container = document.querySelector<HTMLElement>("#session-list");
    if (container) renderSessionCards(container);
    updateSessionSummary();
  } catch (caught) {
    button.disabled = false;
    button.textContent = caught instanceof Error ? caught.message : "Could not stop";
  }
}

function updateSessionSummary(): void {
  const running = state.sessions.length;
  const summary = document.querySelector<HTMLElement>("#session-summary");
  if (summary) summary.textContent = `${running} running`;
  const status = document.querySelector<HTMLElement>("#computer-status .computer-status-label");
  if (status) status.textContent = `${encryptedPortal ? "E2E · " : ""}Computer online · ${running} running`;
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
      state.closedSessions.add(session.id);
      state.sessions = state.sessions.filter((item) => item.id !== session.id);
      renderSessions();
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
    scrollOnUserInput: true,
    scrollSensitivity: 1.15,
    // xterm's animated wheel scrolling starts a new animation for every input
    // event. Rapid trackpad/touch input can build up enough work to stutter on
    // mobile Safari/WebKit, so touch momentum is handled below once per frame.
    smoothScrollDuration: 0,
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
  state.touchCleanup = enableTouchScroll(terminal);
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

function enableTouchScroll(terminal: Terminal): () => void {
  const root = terminal.element;
  const hasTouchInput = navigator.maxTouchPoints > 0 || window.matchMedia("(pointer: coarse)").matches;
  if (!root || !hasTouchInput) return () => undefined;

  let activeTouch: number | undefined;
  let lastY = 0;
  let lastTime = 0;
  let velocity = 0;
  let pendingDelta = 0;
  let pixelRemainder = 0;
  let scrollFrame = 0;
  let momentumFrame = 0;

  const cancelMomentum = (): void => {
    if (momentumFrame) window.cancelAnimationFrame(momentumFrame);
    momentumFrame = 0;
  };
  const applyPendingScroll = (): boolean | undefined => {
    scrollFrame = 0;
    if (!pendingDelta) return undefined;
    const rowHeight = root.clientHeight / terminal.rows;
    if (!Number.isFinite(rowHeight) || rowHeight <= 0) return undefined;
    const pixels = pixelRemainder + pendingDelta;
    pendingDelta = 0;
    const lines = pixels < 0 ? Math.ceil(pixels / rowHeight) : Math.floor(pixels / rowHeight);
    pixelRemainder = pixels - lines * rowHeight;
    if (!lines) return undefined;
    const before = terminal.buffer.active.viewportY;
    terminal.scrollLines(lines);
    const moved = terminal.buffer.active.viewportY !== before;
    if (!moved) pixelRemainder = 0;
    return moved;
  };
  const findTouch = (touches: TouchList): Touch | undefined => {
    if (activeTouch === undefined) return undefined;
    for (let index = 0; index < touches.length; index += 1) {
      const touch = touches.item(index);
      if (touch?.identifier === activeTouch) return touch;
    }
    return undefined;
  };
  const onTouchStart = (event: TouchEvent): void => {
    if (event.touches.length !== 1) return;
    const touch = event.touches.item(0);
    if (!touch) return;
    cancelMomentum();
    activeTouch = touch.identifier;
    lastY = touch.clientY;
    lastTime = performance.now();
    velocity = 0;
    pendingDelta = 0;
    pixelRemainder = 0;
    // Capture the gesture and translate pixels to terminal rows once per frame.
    // This avoids relying on xterm's private viewport DOM, which changed in v6.
    event.stopImmediatePropagation();
  };
  const onTouchMove = (event: TouchEvent): void => {
    const touch = findTouch(event.touches);
    if (!touch) return;
    const now = performance.now();
    const elapsed = Math.max(1, now - lastTime);
    const delta = lastY - touch.clientY;
    lastY = touch.clientY;
    lastTime = now;
    velocity = Math.max(-3, Math.min(3, velocity * 0.72 + (delta / elapsed) * 0.28));
    pendingDelta += delta;
    if (!scrollFrame) scrollFrame = window.requestAnimationFrame(applyPendingScroll);
    if (event.cancelable) event.preventDefault();
    event.stopImmediatePropagation();
  };
  const startMomentum = (): void => {
    if (Math.abs(velocity) < 0.03) return;
    let previous = performance.now();
    const step = (now: number): void => {
      const elapsed = Math.min(32, now - previous);
      previous = now;
      pendingDelta += velocity * elapsed;
      const moved = applyPendingScroll();
      velocity *= Math.pow(0.95, elapsed / 16.67);
      if (moved !== false && Math.abs(velocity) >= 0.03) {
        momentumFrame = window.requestAnimationFrame(step);
      } else {
        momentumFrame = 0;
      }
    };
    momentumFrame = window.requestAnimationFrame(step);
  };
  const onTouchEnd = (event: TouchEvent): void => {
    if (!findTouch(event.changedTouches)) return;
    if (scrollFrame) {
      window.cancelAnimationFrame(scrollFrame);
      applyPendingScroll();
    }
    activeTouch = undefined;
    startMomentum();
  };
  const onTouchCancel = (): void => {
    activeTouch = undefined;
    pendingDelta = 0;
    pixelRemainder = 0;
    velocity = 0;
    if (scrollFrame) window.cancelAnimationFrame(scrollFrame);
    scrollFrame = 0;
    cancelMomentum();
  };

  root.classList.add("touch-scroll");
  root.addEventListener("touchstart", onTouchStart, { capture: true, passive: true });
  root.addEventListener("touchmove", onTouchMove, { capture: true, passive: false });
  root.addEventListener("touchend", onTouchEnd, { capture: true, passive: true });
  root.addEventListener("touchcancel", onTouchCancel, { capture: true, passive: true });
  return () => {
    onTouchCancel();
    root.classList.remove("touch-scroll");
    root.removeEventListener("touchstart", onTouchStart, true);
    root.removeEventListener("touchmove", onTouchMove, true);
    root.removeEventListener("touchend", onTouchEnd, true);
    root.removeEventListener("touchcancel", onTouchCancel, true);
  };
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
  const opened = (): void => {
    if (state.socket !== socket) return;
    const prefix = encryptedPortal ? "E2E · " : "";
    setConnectionState(session.running ? `${prefix}Live · input enabled` : `${prefix}Session output`, session.running ? "online" : "offline");
    fitTerminal();
  };
  const received = async (data: string | ArrayBuffer | Blob): Promise<void> => {
    if (state.socket !== socket) return;
    if (typeof data === "string") {
      try {
        const message = JSON.parse(data) as StatusMessage;
        if (message.type === "status" && !message.running) {
          session.running = false;
          session.exitCode = message.exitCode;
          setConnectionState(message.exitCode === 0 ? "Exited successfully" : `Exited · code ${message.exitCode ?? "?"}`, "offline");
        }
      } catch { /* Ignore unknown text control messages. */ }
      return;
    }
    const bytes = data instanceof Blob ? await data.arrayBuffer() : data;
    state.terminal?.write(new Uint8Array(bytes));
  };
  const closed = (code: number): void => {
    if (state.socket !== socket) return;
    if (code === 1008) {
      state.authenticated = false;
      renderLogin("Your portal session expired");
      return;
    }
    if (session.running) setConnectionState("Disconnected · tap ••• to reconnect", "offline");
  };
  const failed = (): void => {
    if (state.socket === socket) setConnectionState("Connection failed", "offline");
  };

  let socket: TerminalLink;
  if (encryptedPortal) {
    if (!encryptedBridge) {
      renderLogin("Encrypted portal is disconnected");
      return;
    }
    socket = encryptedBridge.openTerminal(session.id, {
      open: opened,
      message: (data) => { void received(data); },
      close: closed,
      error: failed,
    });
  } else {
    const scheme = location.protocol === "https:" ? "wss:" : "ws:";
    const nativeSocket = new WebSocket(`${scheme}//${location.host}/ws/sessions/${encodeURIComponent(session.id)}`);
    nativeSocket.binaryType = "arraybuffer";
    nativeSocket.addEventListener("open", opened);
    nativeSocket.addEventListener("message", (event) => { void received(event.data as string | ArrayBuffer | Blob); });
    nativeSocket.addEventListener("close", (event) => closed(event.code));
    nativeSocket.addEventListener("error", failed);
    socket = nativeSocket;
  }
  state.socket = socket;
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
  if (state.desktop) {
    try { state.desktop.disconnect(); } catch { /* The stream may already be closed. */ }
  }
  state.desktop = undefined;
  state.desktopLink?.close();
  state.desktopLink = undefined;
  if (state.socket) {
    state.socket.close();
  }
  state.socket = undefined;
  state.touchCleanup?.();
  state.touchCleanup = undefined;
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

if ("serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    void navigator.serviceWorker.register("/sw.js", { scope: "/", updateViaCache: "none" });
  }, { once: true });
}

void boot();
