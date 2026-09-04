import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import RFB from "@novnc/novnc";
import "@xterm/xterm/css/xterm.css";
import { base64URLToBytes, bytesToBase64URL, decryptPacket, deriveEncryptionKey, encryptPacket } from "./e2e";
import {
  decodeTerminalHistory,
  duplicateTerminalName,
  MAX_TERMINAL_NAME_LENGTH,
  projectLabel,
  savedActivityTime,
  savedSessionID,
  visibleSavedGroups,
  type SavedTerminal,
} from "./terminal-history";
import { TerminalStreamReconciler, terminalStreamControl } from "./terminal-reconnect";
import { nextTerminalInputMode, parseTerminalInputMode, type TerminalInputMode } from "./terminal-input-mode";
import { TerminalReplyGate } from "./terminal-reply-gate";
import { binaryStringToBytes, consumeTouchWheel } from "./terminal-touch";
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
  signal?: string;
  rows: number;
  cols: number;
  viewer?: "hidden" | "opening" | "visible" | "unsupported";
};

type LocalAgent = {
  id: string;
  name: string;
  command: string;
  version?: string;
  available: boolean;
  runnable: boolean;
  authStatus: string;
  transport: string;
  detectedAt: string;
};

type WorkflowStage = {
  id: string;
  workflowId: string;
  position: number;
  agentId: string;
  title: string;
  prompt: string;
  status: string;
  sessionId?: string;
  output?: string;
  error?: string;
  startedAt?: string;
  endedAt?: string;
};

type AIWorkflow = {
  id: string;
  request: string;
  cwd: string;
  status: string;
  createdAt: string;
  updatedAt: string;
  stages: WorkflowStage[];
};

type WorkflowDraft = { request: string; cwd: string; stages: WorkflowStage[] };

type WorkspaceSuggestion = { path: string; name: string; lastUsedAt: string };

type TerminalTemplate = {
  name: string;
  cwd: string;
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

type WindowSource = {
  id: number;
  title: string;
  application: string;
  bundleId?: string;
  width: number;
  height: number;
};

type WindowPermissions = {
  supported: boolean;
  screenRecording: boolean;
  accessibility: boolean;
};

type WindowSourcesResponse = {
  permissions: WindowPermissions;
  sources: WindowSource[];
  error?: string;
};

type WindowCallbacks = {
  open: () => void;
  frame: (data: Uint8Array, width: number, height: number) => void;
  notice: (reason: string) => void;
  close: (code: number, reason: string) => void;
  error: () => void;
};

type FileUploadReply = {
  type: "file_upload_ready" | "file_upload_progress" | "file_upload_complete";
  received: number;
  total: number;
  path?: string;
};

type PortalView = "sessions" | "terminal" | "workflows" | "workflow" | "desktop";

function readLastPortalView(): { view: PortalView; selected?: string; selectedWorkflow?: string } {
  try {
    const value: unknown = JSON.parse(localStorage.getItem("termlinks-last-view-v1") || "null");
    if (!isRecord(value) || !["sessions", "terminal", "workflows", "workflow", "desktop"].includes(String(value.view))) return { view: "sessions" };
    return {
      view: value.view as PortalView,
      selected: typeof value.selected === "string" ? value.selected : undefined,
      selectedWorkflow: typeof value.selectedWorkflow === "string" ? value.selectedWorkflow : undefined,
    };
  } catch { return { view: "sessions" }; }
}

const lastPortalView = readLastPortalView();

const state: {
  authenticated: boolean;
  sessions: Session[];
  savedTerminals: SavedTerminal[];
  terminalHistoryAvailable: boolean;
  viewerControlAvailable: boolean;
  selected?: string;
  socket?: TerminalLink;
  terminal?: Terminal;
  terminalSessionID?: string;
  terminalSnapshotApplied: boolean;
  terminalReplyGate?: TerminalReplyGate;
  fit?: FitAddon;
  touchCleanup?: () => void;
  touchSync?: () => void;
  layoutCleanup?: () => void;
  resizeTimer?: number;
  lastResize?: string;
  terminalReconnectTimer?: number;
  terminalReconnectAttempts: number;
  desktop?: RFB;
  desktopLink?: EncryptedDesktopLink;
  windowLink?: EncryptedWindowLink;
  polling?: number;
  closedSessions: Set<string>;
  view: PortalView;
  selectedWorkflow?: string;
} = {
  authenticated: false, sessions: [], savedTerminals: [], terminalHistoryAvailable: true, viewerControlAvailable: false, terminalSnapshotApplied: false,
  terminalReconnectAttempts: 0, closedSessions: new Set(),
  view: lastPortalView.view, selected: lastPortalView.selected, selectedWorkflow: lastPortalView.selectedWorkflow,
};

let encryptedPortal = true;
let encryptedBridge: EncryptedBridge | undefined;
let installPrompt: BeforeInstallPromptEvent | undefined;
let portalResumeKey: CryptoKey | undefined;
let portalReconnect: Promise<void> | undefined;
let portalReconnectTimer = 0;

const PORTAL_KEY_DATABASE = "termlinks-secure-session";
const PORTAL_KEY_STORE = "keys";
const PORTAL_KEY_ID = "portal-e2e-key";
const TERMINAL_TAB_ORDER_KEY = "termlinks-terminal-tab-order-v1";
const TERMINAL_INPUT_MODE_KEY = "termlinks-terminal-input-mode-v1";
const MAX_PERSISTED_TERMINAL_TABS = 64;

function readTerminalInputMode(): TerminalInputMode {
  try { return parseTerminalInputMode(localStorage.getItem(TERMINAL_INPUT_MODE_KEY)); }
  catch { return "compose"; }
}

function saveTerminalInputMode(mode: TerminalInputMode): void {
  try { localStorage.setItem(TERMINAL_INPUT_MODE_KEY, mode); }
  catch { /* Storage can be unavailable in private browsing. */ }
}

// Remove metadata written by the unmerged browser-local history prototype.
// Persistent history now lives only in the user's local Termlinks database.
try { localStorage.removeItem("termlinks-saved-terminals-v1"); } catch { /* Storage can be unavailable in private browsing. */ }

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
  lastReason = "";

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
    this.lastReason = reason;
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

class EncryptedWindowLink {
  readyState: number = WebSocket.CONNECTING;

  constructor(readonly id: string, private readonly bridge: EncryptedBridge, private readonly callbacks: WindowCallbacks) {}

  markOpen(): void {
    if (this.readyState !== WebSocket.CONNECTING) return;
    this.readyState = WebSocket.OPEN;
    this.callbacks.open();
  }

  receive(data: Uint8Array, width: number, height: number): void {
    if (this.readyState === WebSocket.OPEN) this.callbacks.frame(data, width, height);
  }

  notice(reason: string): void {
    if (this.readyState === WebSocket.OPEN) this.callbacks.notice(reason);
  }

  remoteClose(code: number, reason: string): void {
    if (this.readyState === WebSocket.CLOSED) return;
    this.readyState = WebSocket.CLOSED;
    this.callbacks.close(code, reason);
  }

  fail(): void {
    if (this.readyState === WebSocket.CLOSED) return;
    this.callbacks.error();
    this.remoteClose(1011, "Encrypted selected-window stream failed");
  }

  send(value: Record<string, unknown>): void {
    if (this.readyState !== WebSocket.OPEN) return;
    void this.bridge.sendWindowInput(this.id, value).catch(() => this.fail());
  }

  close(): void {
    if (this.readyState === WebSocket.CLOSED) return;
    this.readyState = WebSocket.CLOSING;
    void this.bridge.closeWindow(this.id);
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
  private readonly windows = new Map<string, EncryptedWindowLink>();
  private readonly windowLists = new Map<string, { resolve: (value: WindowSourcesResponse) => void; reject: (error: Error) => void; timeout: number }>();
  private readonly uploads = new Map<string, { expected: FileUploadReply["type"]; resolve: (value: FileUploadReply) => void; reject: (error: Error) => void; timeout: number }>();
  private authResolve?: () => void;
  private authReject?: (error: Error) => void;
  private challenge = "";
  private failed = false;

  async connect(token: string): Promise<void> {
    await this.connectWithKey(await deriveEncryptionKey(token));
  }

  async connectWithKey(key: CryptoKey): Promise<void> {
    this.key = key;
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

  isReady(): boolean {
    return !this.failed && this.socket?.readyState === WebSocket.OPEN;
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

  async listWindows(): Promise<WindowSourcesResponse> {
    const id = crypto.randomUUID();
    const response = new Promise<WindowSourcesResponse>((resolve, reject) => {
      const timeout = window.setTimeout(() => {
        this.windowLists.delete(id);
        reject(new Error("Your Mac did not return its window list"));
      }, 15_000);
      this.windowLists.set(id, { resolve, reject, timeout });
    });
    await this.sendEncrypted({ v: 1, type: "window_sources_request", id });
    return response;
  }

  openWindow(windowId: number, maxWidth: number, maxHeight: number, callbacks: WindowCallbacks): EncryptedWindowLink {
    const link = new EncryptedWindowLink(crypto.randomUUID(), this, callbacks);
    this.windows.set(link.id, link);
    void this.sendEncrypted({ v: 1, type: "window_open", id: link.id, windowId, maxWidth, maxHeight }).catch(() => link.fail());
    return link;
  }

  async sendWindowInput(id: string, value: Record<string, unknown>): Promise<void> {
    await this.sendEncrypted({ v: 1, type: "window_input", id, ...value });
  }

  async closeWindow(id: string): Promise<void> {
    this.windows.delete(id);
    if (this.socket?.readyState === WebSocket.OPEN) {
      await this.sendEncrypted({ v: 1, type: "window_close", id, code: 1000, reason: "Viewer closed" });
    }
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

  async uploadFile(file: File, onProgress: (received: number, total: number) => void): Promise<string> {
    const nameBytes = new TextEncoder().encode(file.name);
    if (!file.name || nameBytes.byteLength > 240 || file.name.includes("/") || file.name.includes("\\")) {
      throw new Error("That file name is not supported");
    }
    if (file.size > 100 * 1024 * 1024) throw new Error("Files must be 100 MiB or smaller");
    const id = crypto.randomUUID();
    try {
      await this.uploadExchange(id, "file_upload_ready", { v: 1, type: "file_upload_start", id, name: file.name, size: file.size });
      const chunkSize = 192 * 1024;
      for (let offset = 0; offset < file.size; offset += chunkSize) {
        const bytes = new Uint8Array(await file.slice(offset, Math.min(file.size, offset + chunkSize)).arrayBuffer());
        const reply = await this.uploadExchange(id, "file_upload_progress", {
          v: 1, type: "file_upload_chunk", id, offset, data: bytesToBase64URL(bytes),
        });
        onProgress(reply.received, reply.total);
      }
      const complete = await this.uploadExchange(id, "file_upload_complete", { v: 1, type: "file_upload_finish", id });
      onProgress(file.size, file.size);
      return complete.path || file.name;
    } catch (error) {
      if (this.socket?.readyState === WebSocket.OPEN) {
        void this.sendEncrypted({ v: 1, type: "file_upload_cancel", id }).catch(() => undefined);
      }
      throw error;
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
    if (value.type.startsWith("file_upload_")) {
      const pending = this.uploads.get(value.id);
      if (!pending) return;
      if (value.type === "file_upload_error") {
        window.clearTimeout(pending.timeout);
        this.uploads.delete(value.id);
        pending.reject(new Error(typeof value.reason === "string" ? value.reason : "The computer rejected the file"));
        return;
      }
      if (value.type !== pending.expected || typeof value.received !== "number" || typeof value.total !== "number") {
        throw new Error("Invalid encrypted file upload response");
      }
      window.clearTimeout(pending.timeout);
      this.uploads.delete(value.id);
      pending.resolve({
        type: value.type as FileUploadReply["type"], received: value.received, total: value.total,
        path: typeof value.path === "string" ? value.path : undefined,
      });
      return;
    }
    if (value.type === "window_sources") {
      const pending = this.windowLists.get(value.id);
      if (!pending) return;
      if (!isRecord(value.permissions) || !Array.isArray(value.sources)) throw new Error("Invalid encrypted window list");
      const sources = value.sources.filter(isWindowSource);
      if (sources.length !== value.sources.length) throw new Error("Invalid encrypted window source");
      window.clearTimeout(pending.timeout);
      this.windowLists.delete(value.id);
      pending.resolve({
        permissions: {
          supported: value.permissions.supported === true,
          screenRecording: value.permissions.screenRecording === true,
          accessibility: value.permissions.accessibility === true,
        },
        sources,
        error: typeof value.error === "string" ? value.error : undefined,
      });
      return;
    }
    const windowLink = this.windows.get(value.id);
    if (windowLink) {
      if (value.type === "window_opened") {
        windowLink.markOpen();
        return;
      }
      if (value.type === "window_frame") {
        if (typeof value.data !== "string" || typeof value.width !== "number" || typeof value.height !== "number") throw new Error("Invalid selected-window frame");
        windowLink.receive(base64URLToBytes(value.data), value.width, value.height);
        return;
      }
      if (value.type === "window_notice") {
        windowLink.notice(typeof value.reason === "string" ? value.reason : "macOS rejected that input");
        return;
      }
      if (value.type === "window_close") {
        this.windows.delete(value.id);
        windowLink.remoteClose(typeof value.code === "number" ? value.code : 1000, typeof value.reason === "string" ? value.reason : "Selected window closed");
        return;
      }
      throw new Error("Unsupported selected-window message");
    }
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

  private async uploadExchange(id: string, expected: FileUploadReply["type"], message: Record<string, unknown>): Promise<FileUploadReply> {
    const response = new Promise<FileUploadReply>((resolve, reject) => {
      const timeout = window.setTimeout(() => {
        this.uploads.delete(id);
        reject(new Error("The file transfer timed out"));
      }, 30_000);
      this.uploads.set(id, { expected, resolve, reject, timeout });
    });
    try {
      await this.sendEncrypted(message);
      return await response;
    } catch (error) {
      const pending = this.uploads.get(id);
      if (pending) {
        window.clearTimeout(pending.timeout);
        this.uploads.delete(id);
        pending.reject(error instanceof Error ? error : new Error("Could not send the file"));
      }
      return await response;
    }
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
    for (const windowLink of this.windows.values()) windowLink.remoteClose(1012, error.message);
    this.windows.clear();
    for (const [id, pending] of this.windowLists) {
      window.clearTimeout(pending.timeout);
      pending.reject(error);
      this.windowLists.delete(id);
    }
    for (const [id, pending] of this.uploads) {
      window.clearTimeout(pending.timeout);
      pending.reject(error);
      this.uploads.delete(id);
    }
    if (encryptedBridge === this && state.authenticated) {
      encryptedBridge = undefined;
      state.authenticated = false;
      if (portalResumeKey) {
        setConnectionState("Connection paused · reconnecting…", "connecting");
        window.queueMicrotask(() => { void resumeEncryptedPortal(); });
      } else {
        window.queueMicrotask(() => renderLogin(error.message));
      }
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

function describeSessionExit(session: Session): string {
  if (session.signal) return `Killed · ${session.signal}`;
  if (session.exitCode === 0) return "Exited successfully";
  return `Exited · code ${session.exitCode ?? "?"}`;
}

function savedTerminalBySession(session: Session): SavedTerminal | undefined {
  return state.savedTerminals.find((item) => item.sourceSessionId === session.id || item.activeSessionId === session.id);
}

function runningSessionForSaved(saved: SavedTerminal): Session | undefined {
  return state.sessions.find((item) => item.id === savedSessionID(saved) && item.running);
}

function favoriteSavedTerminals(): SavedTerminal[] {
  return state.savedTerminals.filter((item) => item.favorite).sort((a, b) => savedActivityTime(b) - savedActivityTime(a));
}

function recentSavedTerminals(): SavedTerminal[] {
  return visibleSavedGroups(state.savedTerminals, new Set(state.sessions.map((item) => item.id))).recent;
}

async function loadTerminalHistory(): Promise<void> {
  try {
    const response = await api<unknown>("/api/terminal-history");
    state.savedTerminals = decodeTerminalHistory(response);
    state.terminalHistoryAvailable = true;
  } catch (caught) {
    const message = caught instanceof Error ? caught.message : "";
    const compatibilityError = message.includes("updated local Termlinks service") ||
      message.includes("terminal history is unavailable") || message.includes("API route is not allowed");
    if (!compatibilityError) throw caught;
    state.savedTerminals = [];
    state.terminalHistoryAvailable = false;
  }
}

function replaceSavedTerminal(updated: SavedTerminal): void {
  state.savedTerminals = [updated, ...state.savedTerminals.filter((item) => item.id !== updated.id)];
}

function decodeSavedTerminal(value: unknown): SavedTerminal {
  const [decoded] = decodeTerminalHistory({ terminals: [value] });
  if (!decoded) throw new Error("Your computer returned invalid terminal history");
  return decoded;
}

async function updateSavedTerminal(saved: SavedTerminal, changes: { name?: string; favorite?: boolean }): Promise<SavedTerminal> {
  const response = await api<unknown>(`/api/terminal-history/${encodeURIComponent(saved.id)}`, {
    method: "PATCH",
    body: JSON.stringify(changes),
  });
  const updated = decodeSavedTerminal(response);
  replaceSavedTerminal(updated);
  return updated;
}

async function removeSavedTerminal(saved: SavedTerminal): Promise<void> {
  await api(`/api/terminal-history/${encodeURIComponent(saved.id)}`, { method: "DELETE" });
  state.savedTerminals = state.savedTerminals.filter((item) => item.id !== saved.id);
}

function normalizeTerminalNameInput(value: string | null): string | undefined {
  if (value === null) return undefined;
  const name = value.trim();
  if (!name) {
    window.alert("Terminal name is required.");
    return undefined;
  }
  if (name.length > MAX_TERMINAL_NAME_LENGTH) {
    window.alert(`Terminal name must be ${MAX_TERMINAL_NAME_LENGTH} characters or less.`);
    return undefined;
  }
  return name;
}


function openPortalKeyDatabase(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(PORTAL_KEY_DATABASE, 1);
    request.addEventListener("upgradeneeded", () => {
      if (!request.result.objectStoreNames.contains(PORTAL_KEY_STORE)) request.result.createObjectStore(PORTAL_KEY_STORE);
    });
    request.addEventListener("success", () => resolve(request.result), { once: true });
    request.addEventListener("error", () => reject(request.error || new Error("Could not open secure device storage")), { once: true });
    request.addEventListener("blocked", () => reject(new Error("Secure device storage is blocked")), { once: true });
  });
}

async function loadPortalResumeKey(): Promise<CryptoKey | undefined> {
  if (!("indexedDB" in window)) return undefined;
  let database: IDBDatabase | undefined;
  try {
    database = await openPortalKeyDatabase();
    const value = await new Promise<unknown>((resolve, reject) => {
      const request = database!.transaction(PORTAL_KEY_STORE, "readonly").objectStore(PORTAL_KEY_STORE).get(PORTAL_KEY_ID);
      request.addEventListener("success", () => resolve(request.result), { once: true });
      request.addEventListener("error", () => reject(request.error || new Error("Could not read secure device storage")), { once: true });
    });
    if (!(value instanceof CryptoKey) || value.extractable || value.algorithm.name !== "AES-GCM") return undefined;
    if (!value.usages.includes("encrypt") || !value.usages.includes("decrypt")) return undefined;
    return value;
  } catch {
    return undefined;
  } finally {
    database?.close();
  }
}

async function savePortalResumeKey(key: CryptoKey): Promise<boolean> {
  if (!("indexedDB" in window) || key.extractable) return false;
  let database: IDBDatabase | undefined;
  try {
    database = await openPortalKeyDatabase();
    await new Promise<void>((resolve, reject) => {
      const transaction = database!.transaction(PORTAL_KEY_STORE, "readwrite");
      transaction.objectStore(PORTAL_KEY_STORE).put(key, PORTAL_KEY_ID);
      transaction.addEventListener("complete", () => resolve(), { once: true });
      transaction.addEventListener("abort", () => reject(transaction.error || new Error("Could not save secure login")), { once: true });
      transaction.addEventListener("error", () => reject(transaction.error || new Error("Could not save secure login")), { once: true });
    });
    return true;
  } catch {
    return false;
  } finally {
    database?.close();
  }
}

async function clearPortalResumeKey(): Promise<void> {
  portalResumeKey = undefined;
  if (portalReconnectTimer) window.clearTimeout(portalReconnectTimer);
  portalReconnectTimer = 0;
  if (!("indexedDB" in window)) return;
  let database: IDBDatabase | undefined;
  try {
    database = await openPortalKeyDatabase();
    await new Promise<void>((resolve, reject) => {
      const transaction = database!.transaction(PORTAL_KEY_STORE, "readwrite");
      transaction.objectStore(PORTAL_KEY_STORE).delete(PORTAL_KEY_ID);
      transaction.addEventListener("complete", () => resolve(), { once: true });
      transaction.addEventListener("abort", () => reject(transaction.error), { once: true });
    });
  } catch { /* Private browsing or cleared site storage already forgets the key. */ }
  finally { database?.close(); }
}

function isWindowSource(value: unknown): value is WindowSource {
  return isRecord(value)
    && Number.isInteger(value.id) && (value.id as number) > 0
    && typeof value.title === "string" && value.title.length > 0
    && typeof value.application === "string" && value.application.length > 0
    && typeof value.width === "number" && value.width > 0
    && typeof value.height === "number" && value.height > 0
    && (value.bundleId === undefined || typeof value.bundleId === "string");
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

type AppDestination = "home" | "ai" | "desktop" | "new";

function renderAppNavigation(active: AppDestination): HTMLElement {
  const navigation = el("nav", "app-bottom-nav");
  navigation.setAttribute("aria-label", "Main navigation");
  const addItem = (destination: AppDestination, icon: string, label: string, action: () => void): void => {
    const button = el("button", `app-nav-button${active === destination ? " active" : ""}`);
    button.type = "button";
    button.dataset.destination = destination;
    button.setAttribute("aria-label", label);
    if (active === destination) button.setAttribute("aria-current", "page");
    button.append(el("span", "app-nav-icon", icon), el("span", "app-nav-label", label));
    button.addEventListener("click", action);
    navigation.append(button);
  };
  addItem("home", "⌂", "Home", renderSessions);
  addItem("ai", "✦", "AI Work", () => { void renderWorkflows(); });
  addItem("desktop", "▣", "Desktop", renderDesktop);
  addItem("new", "+", "New", renderSessionsWithCreate);
  return navigation;
}

function markAppNavigationActive(destination: AppDestination): void {
  for (const button of document.querySelectorAll<HTMLButtonElement>(".app-nav-button")) {
    const active = button.dataset.destination === destination;
    button.classList.toggle("active", active);
    if (active) button.setAttribute("aria-current", "page");
    else button.removeAttribute("aria-current");
  }
}

async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  if (encryptedPortal) {
    if (!encryptedBridge) throw new Error("Encrypted portal is not connected");
    const method = init.method ?? "GET";
    const body = typeof init.body === "string" ? init.body : "";
    const response = await encryptedBridge.request(method, path, body);
    if (response.status === 401) {
      state.authenticated = false;
      await clearPortalResumeKey();
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
    try {
      return JSON.parse(response.body) as T;
    } catch {
      if (isWorkflowAPIPath(path)) {
        throw new Error("AI Work needs the updated local Termlinks service");
      }
      if (path.startsWith("/api/terminal-history")) {
        throw new Error("Terminal history needs the updated local Termlinks service");
      }
      throw new Error("Your computer returned an invalid response");
    }
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

function isWorkflowAPIPath(path: string): boolean {
  return path === "/api/agents" || path === "/api/agents/refresh" ||
    path === "/api/projects/suggestions" || path === "/api/workflows" ||
    path.startsWith("/api/workflows/");
}

async function readyUploadBridge(): Promise<EncryptedBridge> {
  const current = encryptedBridge;
  if (state.authenticated && current?.isReady()) return current;
  if (!encryptedPortal || !portalResumeKey) throw new Error("The encrypted computer connection is offline");
  if (state.authenticated) state.authenticated = false;
  await resumeEncryptedPortal();
  if (!state.authenticated || !encryptedBridge?.isReady()) {
    throw new Error("The computer is reconnecting. Try the upload again in a moment");
  }
  return encryptedBridge;
}

async function resumeEncryptedPortal(): Promise<void> {
  if (!encryptedPortal || !portalResumeKey || state.authenticated || document.visibilityState === "hidden") return;
  if (portalReconnect) {
    await portalReconnect;
    return;
  }
  const key = portalResumeKey;
  const selectedSession = state.selected;
  if (portalReconnectTimer) window.clearTimeout(portalReconnectTimer);
  portalReconnectTimer = 0;
  setConnectionState("Reconnecting securely…", "connecting");
  portalReconnect = (async () => {
    const previous = encryptedBridge;
    encryptedBridge = undefined;
    previous?.close();
    const bridge = new EncryptedBridge();
    await bridge.connectWithKey(key);
    encryptedBridge = bridge;
    state.authenticated = true;
    await loadSessions();
    const session = selectedSession ? state.sessions.find((item) => item.id === selectedSession) : undefined;
    if (state.view === "workflow" && state.selectedWorkflow) await renderWorkflowDetail(state.selectedWorkflow);
    else if (state.view === "workflows") await renderWorkflows();
    else if (state.view === "desktop") renderDesktop();
    else if (session && state.terminal && document.querySelector(".terminal-page")) connectTerminal(session);
    else if (session) renderTerminal(session.id, state.selectedWorkflow);
    else if (state.view === "sessions" && document.querySelector("#session-list")) {
      const container = document.querySelector<HTMLElement>("#session-list");
      if (container) renderSessionCards(container);
      updateSessionSummary();
      startPolling();
    }
    else renderSessions();
  })().catch(async (caught: unknown) => {
    encryptedBridge = undefined;
    state.authenticated = false;
    const message = caught instanceof Error ? caught.message : "Could not reconnect securely";
    if (message.includes("Invalid portal token")) {
      await clearPortalResumeKey();
      renderLogin("The saved device login is no longer valid. Enter the current portal token.");
      return;
    }
    // iOS briefly suspends network sockets while Photos/the file picker is on
    // screen. Preserve the terminal/dashboard DOM and its draft instead of
    // replacing the user's work with a login screen during that wake-up gap.
    const activePortalView = document.querySelector(".terminal-page, .dashboard, .desktop-page");
    if (activePortalView) {
      setConnectionState("Computer waking · reconnecting…", "connecting");
      const transfer = document.querySelector<HTMLElement>(".file-transfer-status");
      if (transfer && !transfer.hidden) {
        transfer.textContent = "Computer waking · reconnecting securely…";
        transfer.classList.remove("failed");
      }
    } else {
      renderLogin("Saved login found. Reconnecting when the computer is available…");
    }
    if (portalResumeKey && document.visibilityState === "visible") {
      portalReconnectTimer = window.setTimeout(() => {
        portalReconnectTimer = 0;
        void resumeEncryptedPortal();
      }, 2500);
    }
  }).finally(() => {
    portalReconnect = undefined;
  });
  await portalReconnect;
}

async function boot(): Promise<void> {
  encryptedPortal = !(await isDirectPortal());
  if (encryptedPortal) {
    portalResumeKey = await loadPortalResumeKey();
    if (portalResumeKey) {
      renderLogin("Restoring the secure device login…");
      await resumeEncryptedPortal();
    } else {
      renderLogin();
    }
    return;
  }
  try {
    await api<{ authenticated: boolean }>("/api/me");
    state.authenticated = true;
    await loadSessions();
    await renderRememberedView();
  } catch (caught) {
    if (!state.authenticated) {
      const message = caught instanceof Error && caught.message.includes("offline")
        ? "Your computer is offline. Start it with: termlinks cloud start"
        : "";
      renderLogin(message);
    }
  }
}

async function isDirectPortal(): Promise<boolean> {
  try {
    const response = await fetch("/api/mode", {
      headers: { Accept: "application/json" },
      cache: "no-store",
    });
    if (!response.ok || !response.headers.get("Content-Type")?.includes("application/json")) return false;
    const result = await response.json() as { mode?: string };
    return result.mode === "direct";
  } catch {
    return false;
  }
}

async function renderRememberedView(): Promise<void> {
  if (state.view === "workflow" && state.selectedWorkflow) {
    await renderWorkflowDetail(state.selectedWorkflow);
    return;
  }
  if (state.view === "workflows") {
    await renderWorkflows();
    return;
  }
  if (state.view === "desktop" && encryptedPortal) {
    renderDesktop();
    return;
  }
  if (state.view === "terminal" && state.selected && state.sessions.some((session) => session.id === state.selected)) {
    renderTerminal(state.selected, state.selectedWorkflow);
    return;
  }
  renderSessions();
}

function rememberPortalView(view: PortalView, selected = state.selected, selectedWorkflow = state.selectedWorkflow): void {
  state.view = view;
  try { localStorage.setItem("termlinks-last-view-v1", JSON.stringify({ view, selected, selectedWorkflow })); }
  catch { /* Private browsing may reject presentation-state storage. */ }
}

function renderLogin(message = ""): void {
  stopPolling();
  closeConnection();
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
  form.autocomplete = "on";
  form.method = "post";
  form.action = "/api/login";
  const label = el("label", "field-label", "Portal token");
  label.htmlFor = "token";
  const username = el("input", "password-manager-username");
  username.type = "text";
  username.name = "username";
  username.autocomplete = "username";
  username.value = "termlinks";
  username.tabIndex = -1;
  username.setAttribute("aria-hidden", "true");
  const input = el("input", "token-input");
  input.id = "token";
  input.name = "password";
  input.type = "password";
  input.autocomplete = "current-password";
  input.spellcheck = false;
  input.placeholder = "Paste your private token";
  input.required = true;
  const rememberLabel = el("label", "remember-device");
  const remember = el("input", "remember-device-checkbox");
  remember.type = "checkbox";
  remember.checked = true;
  rememberLabel.append(remember, el("span", "remember-device-copy", "Keep me signed in on this device"));
  const submit = el("button", "primary-button", "Unlock portal");
  submit.type = "submit";
  const error = el("p", "form-error", message);
  error.setAttribute("role", "alert");
  form.append(username, label, input, rememberLabel, submit, error);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (portalReconnectTimer) window.clearTimeout(portalReconnectTimer);
    portalReconnectTimer = 0;
    submit.disabled = true;
    submit.textContent = "Checking…";
    error.textContent = "";
    try {
      const token = input.value.trim();
      if (token.length < 32) throw new Error("Paste the complete portal token without backticks");
      if (encryptedPortal) {
        encryptedBridge?.close();
        const key = await deriveEncryptionKey(token);
        const bridge = new EncryptedBridge();
        await bridge.connectWithKey(key);
        encryptedBridge = bridge;
        portalResumeKey = key;
        if (remember.checked) await savePortalResumeKey(key);
        else await clearPortalResumeKey();
      } else {
        await api("/api/login", { method: "POST", body: JSON.stringify({ token }) });
      }
      state.authenticated = true;
      await loadSessions();
      await renderRememberedView();
    } catch (caught) {
      error.textContent = caught instanceof Error ? caught.message : "Login failed";
      submit.disabled = false;
      submit.textContent = "Unlock portal";
      input.focus();
    }
  });
  panel.append(form, createInstallButton(), el("p", "login-hint", "On your computer: termlinks token · A remembered device reconnects automatically after iOS suspension"));
  page.append(panel);
  app.append(page);
  if (!window.matchMedia("(pointer: coarse)").matches) input.focus();
}

async function loadSessions(): Promise<void> {
  const response = await api<{ sessions: Session[]; viewerControl?: boolean }>("/api/sessions");
  state.viewerControlAvailable = response.viewerControl === true;
  state.sessions = response.sessions.filter((session) => session.running && !state.closedSessions.has(session.id));
  await loadTerminalHistory();
}

function renderSessions(): void {
  closeConnection();
  state.sessions = state.sessions.filter((session) => session.running && !state.closedSessions.has(session.id));
  state.selected = undefined;
  state.selectedWorkflow = undefined;
  rememberPortalView("sessions");
  app.replaceChildren();
  const page = el("main", "dashboard");
  const header = el("header", "topbar home-topbar");
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
      await clearPortalResumeKey();
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
  titleGroup.append(el("p", "eyebrow", "HOME · YOUR COMPUTER"), el("h1", "dashboard-title", "Terminal sessions"), summary);
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
  const transferStatus = el("p", "file-transfer-status");
  transferStatus.hidden = true;
  if (encryptedPortal) {
    const desktop = el("button", "desktop-button", "▣ Remote desktop");
    desktop.type = "button";
    desktop.addEventListener("click", renderDesktop);
    const upload = el("button", "desktop-button", "⇧ Send file");
    upload.type = "button";
    upload.addEventListener("click", () => chooseAndUploadFiles(upload, (message, failed) => {
      transferStatus.hidden = false;
      transferStatus.textContent = message;
      transferStatus.classList.toggle("failed", failed);
    }));
    headingActions.append(desktop, upload);
  }

  const workflows = el("button", "ai-work-button", "✦ AI work");
  workflows.type = "button";
  workflows.addEventListener("click", () => { void renderWorkflows(); });
  headingActions.append(workflows);
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
  page.append(header, heading, transferStatus, createPanel, list, renderStartHint(), renderTermAdsTeaser(), renderAppNavigation("home"));
  app.append(page);
  updateSessionSummary();
  startPolling();
}

async function renderWorkflows(message = ""): Promise<void> {
  stopPolling();
  closeConnection();
  state.selected = undefined;
  state.selectedWorkflow = undefined;
  rememberPortalView("workflows");
  app.replaceChildren();
  const page = el("main", "dashboard workflow-page");
  const header = el("header", "topbar workflow-topbar");
  const brand = el("button", "brand brand-button");
  brand.type = "button";
  brand.append(el("span", "brand-mark", ">_"), el("span", "brand-name", "termlinks"));
  brand.addEventListener("click", renderSessions);
  const back = el("button", "ghost-button workflow-back-button", "← Terminals");
  back.type = "button";
  back.addEventListener("click", renderSessions);
  header.append(back, brand, el("span", "workflow-private-badge", encryptedPortal ? "E2E · LOCAL STATE" : "LOCAL STATE"));

  const heading = el("div", "workflow-heading");
  heading.append(
    el("p", "eyebrow", "LOCAL AI COORDINATOR"),
    el("h1", "dashboard-title", "Direct the agents on your computer"),
    el("p", "workflow-lead", "Mention installed agents in order. Termlinks opens each agent in a real managed terminal and carries the result into the next stage."),
  );
  const notice = el("p", "workflow-notice", message);
  notice.hidden = !message;
  const composer = renderWorkflowComposer();
  const list = el("section", "workflow-list");
  list.append(el("p", "workflow-loading", "Loading local workflows…"));
  page.append(header, heading, notice, composer, list, renderAppNavigation("ai"));
  app.append(page);

  try {
    const [{ agents }, { projects }, { workflows }] = await Promise.all([
      api<{ agents: LocalAgent[] }>("/api/agents"),
      api<{ projects: WorkspaceSuggestion[] }>("/api/projects/suggestions"),
      api<{ workflows: AIWorkflow[] }>("/api/workflows"),
    ]);
    populateWorkflowComposer(composer, agents, projects);
    renderWorkflowCards(list, workflows);
    if (workflows.some((workflow) => workflow.status === "queued" || workflow.status === "running")) {
      state.polling = window.setInterval(() => { void refreshWorkflowCards(list); }, 2000);
    }
  } catch (caught) {
    const rawMessage = caught instanceof Error ? caught.message : "Could not load AI workflows";
    const compatibilityFailure = rawMessage === "API route is not allowed" ||
      rawMessage.includes("updated local Termlinks service") || rawMessage.includes("invalid response");
    if (compatibilityFailure) rememberPortalView("sessions", undefined, undefined);
    composer.querySelector<HTMLElement>("[data-role=agents]")?.replaceChildren(el("span", "agent-chip muted", "AI Work unavailable"));
    for (const control of composer.querySelectorAll<HTMLInputElement | HTMLTextAreaElement | HTMLButtonElement>("input, textarea, button")) {
      control.disabled = true;
    }
    const error = el("section", "workflow-error-state");
    error.append(
      el("strong", "workflow-error-title", compatibilityFailure ? "AI Work needs a local restart" : "Could not load AI Work"),
      el("p", "workflow-error-copy", compatibilityFailure
        ? "Your terminals are safe. The website is newer than the Termlinks service currently running on this computer. Return to terminals now; restart Termlinks only after you have finished or stopped the active sessions."
        : rawMessage),
    );
    const actions = el("div", "workflow-error-actions");
    const home = el("button", "workflow-error-home", "← Return to terminals");
    home.type = "button";
    home.addEventListener("click", renderSessions);
    const retry = el("button", "workflow-error-retry", "Try again");
    retry.type = "button";
    retry.addEventListener("click", () => { void renderWorkflows(); });
    actions.append(home, retry);
    error.append(actions);
    if (compatibilityFailure) error.append(el("p", "workflow-error-detail", `Technical detail: ${rawMessage}`));
    list.replaceChildren(error);
  }
}

function renderWorkflowComposer(): HTMLElement {
  const panel = el("section", "workflow-composer");
  const agentRow = el("div", "workflow-agent-row");
  agentRow.dataset.role = "agents";
  agentRow.append(el("span", "agent-chip muted", "Detecting local agents…"));
  const form = el("form", "workflow-form");
  const request = el("textarea", "workflow-request");
  request.name = "request";
  request.rows = 4;
  request.maxLength = 48 << 10;
  request.placeholder = "@codex inspect and plan\n@claude implement the plan\n@codex review the result";
  request.setAttribute("aria-label", "Workflow instructions");
  const row = el("div", "workflow-form-row");
  const cwd = el("input", "workflow-cwd");
  cwd.name = "cwd";
  cwd.maxLength = 4096;
  cwd.placeholder = "/absolute/path/to/project";
  cwd.spellcheck = false;
  cwd.setAttribute("list", "workflow-projects");
  cwd.setAttribute("aria-label", "Project directory");
  const projects = el("datalist");
  projects.id = "workflow-projects";
  const submit = el("button", "workflow-start", "Start workflow →");
  submit.type = "submit";
  row.append(cwd, projects, submit);
  const hint = el("p", "workflow-form-hint", "Runs only on this computer. Explicit @agent mentions never fall back silently.");
  const error = el("p", "form-error");
  error.setAttribute("role", "alert");
  const preview = el("div", "workflow-preview");
  preview.hidden = true;
  const resetPreview = (): void => {
    form.dataset.reviewed = "false";
    preview.hidden = true;
    preview.replaceChildren();
    submit.textContent = "Review plan →";
  };
  request.addEventListener("input", resetPreview);
  cwd.addEventListener("input", resetPreview);
  form.append(request, row, hint, preview, error);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    error.textContent = "";
    submit.disabled = true;
    submit.textContent = form.dataset.reviewed === "true" ? "Starting…" : "Reviewing…";
    try {
      if (form.dataset.reviewed !== "true") {
        const draft = await api<WorkflowDraft>("/api/workflows/compile", { method: "POST", body: JSON.stringify({ request: request.value.trim(), cwd: cwd.value.trim() }) });
        preview.replaceChildren(el("strong", "workflow-preview-title", "Review before starting"));
        for (const [index, stage] of draft.stages.entries()) {
          preview.append(el("div", "workflow-preview-stage", `${index + 1}. @${stage.agentId} — ${stage.prompt}`));
        }
        preview.hidden = false;
        form.dataset.reviewed = "true";
        submit.disabled = false;
        submit.textContent = "Confirm & start →";
        return;
      }
      const created = await api<AIWorkflow>("/api/workflows", { method: "POST", body: JSON.stringify({ request: request.value.trim(), cwd: cwd.value.trim() }) });
      await renderWorkflowDetail(created.id);
    } catch (caught) {
      error.textContent = caught instanceof Error ? caught.message : "Could not start the workflow";
      submit.disabled = false;
      submit.textContent = "Start workflow →";
    }
  });
  panel.append(agentRow, form);
  return panel;
}

function populateWorkflowComposer(panel: HTMLElement, agents: LocalAgent[], projects: WorkspaceSuggestion[]): void {
  const row = panel.querySelector<HTMLElement>('[data-role="agents"]');
  row?.replaceChildren();
  for (const agent of agents) {
    const ready = agent.available && agent.runnable && agent.authStatus !== "needs-login";
    const label = !agent.available
      ? `@${agent.id} · not installed`
      : !agent.runnable ? `@${agent.id} · adapter pending` : `@${agent.id} · ${agent.authStatus}`;
    const chip = el("button", `agent-chip ${ready ? "available" : "muted"}`, label);
    chip.type = "button";
    chip.disabled = !ready;
    chip.title = agent.version || agent.command || agent.name;
    chip.addEventListener("click", () => {
      const input = panel.querySelector<HTMLTextAreaElement>(".workflow-request");
      if (!input) return;
      const prefix = input.value && !input.value.endsWith("\n") ? "\n" : "";
      input.value += `${prefix}@${agent.id} `;
      input.dispatchEvent(new Event("input"));
      input.focus();
    });
    row?.append(chip);
  }
  const refresh = el("button", "agent-chip agent-refresh", "↻ Refresh");
  refresh.type = "button";
  refresh.addEventListener("click", async () => {
    refresh.disabled = true;
    refresh.textContent = "Checking…";
    try {
      const response = await api<{ agents: LocalAgent[] }>("/api/agents/refresh", { method: "POST" });
      populateWorkflowComposer(panel, response.agents, projects);
    } catch {
      refresh.disabled = false;
      refresh.textContent = "Refresh failed";
    }
  });
  row?.append(refresh);
  const datalist = panel.querySelector<HTMLDataListElement>("#workflow-projects");
  const cwd = panel.querySelector<HTMLInputElement>(".workflow-cwd");
  for (const project of projects) {
    const option = document.createElement("option");
    option.value = project.path;
    option.label = project.name;
    datalist?.append(option);
  }
  if (cwd && projects[0]) cwd.value = projects[0].path;
}

function renderWorkflowCards(container: HTMLElement, workflows: AIWorkflow[]): void {
  container.replaceChildren();
  if (workflows.length === 0) {
    const empty = el("div", "empty-state");
    empty.append(el("span", "empty-prompt", "✦"), el("h2", "empty-title", "No AI work yet"), el("p", "empty-copy", "Create a directed workflow above. Its terminals remain available beside your normal sessions."));
    container.append(empty);
    return;
  }
  for (const workflow of workflows) {
    const card = el("button", "workflow-card");
    card.type = "button";
    card.addEventListener("click", () => { void renderWorkflowDetail(workflow.id); });
    const top = el("span", "workflow-card-top");
    top.append(el("strong", "workflow-card-title", workflow.request), el("span", `workflow-status ${workflow.status}`, workflow.status.toUpperCase()));
    const pipeline = el("span", "workflow-pipeline");
    workflow.stages.forEach((stage, index) => {
      if (index) pipeline.append(el("span", "pipeline-arrow", "→"));
      pipeline.append(el("span", `pipeline-agent ${stage.status}`, `@${stage.agentId}`));
    });
    card.append(top, pipeline, el("span", "workflow-card-path", compactPath(workflow.cwd)));
    container.append(card);
  }
}

async function refreshWorkflowCards(container: HTMLElement): Promise<void> {
  if (!document.body.contains(container)) return stopPolling();
  try {
    const { workflows } = await api<{ workflows: AIWorkflow[] }>("/api/workflows");
    renderWorkflowCards(container, workflows);
    if (!workflows.some((workflow) => workflow.status === "queued" || workflow.status === "running")) stopPolling();
  } catch { /* Keep the last durable snapshot during a brief reconnect. */ }
}

async function renderWorkflowDetail(id: string): Promise<void> {
  stopPolling();
  closeConnection();
  state.selectedWorkflow = id;
  rememberPortalView("workflow", undefined, id);
  app.replaceChildren();
  const page = el("main", "dashboard workflow-page");
  const header = el("header", "topbar workflow-detail-topbar");
  const back = el("button", "ghost-button", "‹ AI workflows");
  back.type = "button";
  back.addEventListener("click", () => { void renderWorkflows(); });
  header.append(el("div", "brand", "✦ Agent work"), back);
  const mount = el("section", "workflow-detail");
  mount.append(el("p", "workflow-loading", "Loading workflow…"));
  page.append(header, mount, renderAppNavigation("ai"));
  app.append(page);
  let remainsActive = false;
  const refresh = async (): Promise<void> => {
    if (!document.body.contains(mount)) { stopPolling(); return; }
    try {
      const workflow = await api<AIWorkflow>(`/api/workflows/${encodeURIComponent(id)}`);
      renderWorkflowDetailContent(mount, workflow);
      remainsActive = workflow.status === "queued" || workflow.status === "running";
      if (!remainsActive) stopPolling();
    } catch (caught) {
      mount.replaceChildren(el("p", "form-error", caught instanceof Error ? caught.message : "Could not load workflow"));
      stopPolling();
    }
  };
  await refresh();
  if (document.body.contains(mount) && remainsActive && !state.polling) state.polling = window.setInterval(() => { void refresh(); }, 1500);
}

function renderWorkflowDetailContent(container: HTMLElement, workflow: AIWorkflow): void {
  container.replaceChildren();
  const heading = el("div", "workflow-detail-heading");
  const title = el("div");
  title.append(el("p", "eyebrow", compactPath(workflow.cwd)), el("h1", "workflow-detail-title", workflow.request));
  heading.append(title, el("span", `workflow-status ${workflow.status}`, workflow.status.toUpperCase()));
  container.append(heading);
  const timeline = el("div", "workflow-timeline");
  for (const stage of workflow.stages) {
    const card = el("article", `workflow-stage ${stage.status}`);
    const top = el("div", "workflow-stage-top");
    top.append(el("strong", "workflow-stage-agent", `@${stage.agentId}`), el("span", "workflow-stage-status", stage.status));
    card.append(top, el("p", "workflow-stage-prompt", stage.prompt));
    if (stage.error) card.append(el("p", "workflow-stage-error", stage.error));
    if (stage.output) {
      const details = el("details", "workflow-output");
      details.append(el("summary", undefined, "Saved agent output"), el("pre", undefined, stage.output));
      card.append(details);
    }
    if (stage.sessionId && stage.status === "running") {
      const actions = el("div", "workflow-stage-actions");
      const open = el("button", "card-action", "Open live terminal");
      open.type = "button";
      open.addEventListener("click", () => { void openWorkflowTerminal(workflow.id, stage.sessionId!); });
      actions.append(open);
      actions.append(renderWorkflowInput(workflow.id, stage.id));
      card.append(actions);
    }
    timeline.append(card);
  }
  container.append(timeline);
  if (workflow.status === "queued" || workflow.status === "running") {
    const cancel = el("button", "workflow-cancel", "Cancel workflow");
    cancel.type = "button";
    cancel.addEventListener("click", async () => {
      if (!window.confirm("Cancel this workflow and stop its active agent terminal?")) return;
      cancel.disabled = true;
      try { await api(`/api/workflows/${encodeURIComponent(workflow.id)}/cancel`, { method: "POST" }); }
      catch (caught) { cancel.textContent = caught instanceof Error ? caught.message : "Cancellation failed"; }
    });
    container.append(cancel);
  }
}

function renderWorkflowInput(workflowID: string, stageID: string): HTMLElement {
  const form = el("form", "workflow-input");
  const input = el("input", "workflow-input-field");
  input.placeholder = "Send input to this agent…";
  input.maxLength = 48 << 10;
  const send = el("button", "card-action", "Send");
  send.type = "submit";
  form.append(input, send);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!input.value) return;
    send.disabled = true;
    try {
      await api(`/api/workflows/${encodeURIComponent(workflowID)}/stages/${encodeURIComponent(stageID)}/input`, { method: "POST", body: JSON.stringify({ input: input.value }) });
      input.value = "";
    } catch (caught) { input.value = caught instanceof Error ? caught.message : "Input failed"; }
    finally { send.disabled = false; }
  });
  return form;
}

async function openWorkflowTerminal(workflowID: string, sessionID: string): Promise<void> {
  await loadSessions();
  const found = state.sessions.find((session) => session.id === sessionID);
  if (!found) { await renderWorkflows("That agent terminal has already expired from the daemon."); return; }
  renderTerminal(sessionID, workflowID);
}

function renderSessionsWithCreate(): void {
  renderSessions();
  markAppNavigationActive("new");
  const panel = document.querySelector<HTMLElement>("#create-terminal-panel");
  const create = document.querySelector<HTMLButtonElement>('[aria-controls="create-terminal-panel"]');
  if (!panel || !create) return;
  panel.hidden = false;
  create.setAttribute("aria-expanded", "true");
  window.requestAnimationFrame(() => panel.querySelector<HTMLInputElement>("#new-session-name")?.focus({ preventScroll: true }));
}

function chooseAndUploadFiles(button: HTMLButtonElement, setStatus: (message: string, failed: boolean) => void): void {
  if (!encryptedBridge && !portalResumeKey) {
    setStatus("The encrypted computer connection is offline", true);
    return;
  }
  const picker = document.createElement("input");
  picker.type = "file";
  picker.multiple = true;
  picker.hidden = true;
  picker.addEventListener("change", async () => {
    const files = Array.from(picker.files || []);
    picker.remove();
    if (files.length === 0) return;
    button.disabled = true;
    try {
      for (let index = 0; index < files.length; index++) {
        const file = files[index]!;
        setStatus(`Sending ${file.name} (${index + 1}/${files.length})…`, false);
        const bridge = await readyUploadBridge();
        const path = await bridge.uploadFile(file, (received, total) => {
          const percent = total === 0 ? 100 : Math.round((received / total) * 100);
          setStatus(`Sending ${file.name} · ${percent}%`, false);
        });
        setStatus(`Saved on the Mac: ${path}`, false);
      }
    } catch (caught) {
      setStatus(caught instanceof Error ? caught.message : "The file transfer failed", true);
    } finally {
      button.disabled = false;
    }
  }, { once: true });
  picker.addEventListener("cancel", () => picker.remove(), { once: true });
  document.body.append(picker);
  picker.click();
}

function renderDesktop(): void {
  stopPolling();
  closeConnection();
  if (!encryptedPortal || !encryptedBridge) {
    renderLogin("Remote desktop requires the encrypted cloud portal");
    return;
  }
  rememberPortalView("desktop", undefined, undefined);

  app.replaceChildren();
  const page = el("main", "desktop-page");
  const header = el("header", "desktop-header");
  const back = el("button", "back-button", "‹");
  back.classList.add("terminal-back-button");
  back.type = "button";
  back.setAttribute("aria-label", "Back to sessions");
  back.addEventListener("click", renderSessions);
  const identity = el("div", "terminal-identity");
  const subtitle = el("span", "terminal-subtitle", "Choose a desktop or window · E2E");
  identity.append(el("strong", "terminal-name", "Remote desktop"), subtitle);
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
  empty.append(el("span", "desktop-waiting-icon", "▣"), el("span", "desktop-waiting-copy", "Choose what you want to view"));
  mount.append(empty);
  const touchGate = el("button", "desktop-touch-gate", "Tap to enable touch control");
  touchGate.type = "button";
  touchGate.hidden = true;
  mount.append(touchGate);
  frame.append(mount);

  const sourcePicker = el("section", "desktop-source-picker");
  const sourceCard = el("div", "desktop-source-card");
  sourceCard.append(
    el("h2", "desktop-source-title", "Choose what to share"),
    el("p", "desktop-source-copy", "Use Screen Sharing for the complete Mac, or stream one selected window with macOS ScreenCaptureKit."),
  );
  const fullDesktop = el("button", "desktop-source-full", "▣  Full Mac desktop");
  fullDesktop.type = "button";
  const windowHeading = el("div", "desktop-window-heading");
  windowHeading.append(el("strong", "desktop-window-label", "Open windows"));
  const refreshWindows = el("button", "desktop-window-refresh", "Refresh");
  refreshWindows.type = "button";
  windowHeading.append(refreshWindows);
  const permission = el("p", "desktop-permission", "Reading encrypted window list…");
  const windowList = el("div", "desktop-window-list");
  sourceCard.append(fullDesktop, windowHeading, permission, windowList);
  sourcePicker.append(sourceCard);
  frame.append(sourcePicker);

  const controls = el("div", "desktop-controls");
  controls.hidden = true;
  const control = el("button", "desktop-control primary-control", "Enable control");
  control.type = "button";
  const keyboard = el("button", "desktop-control", "Keyboard");
  keyboard.type = "button";
  const clipboard = el("button", "desktop-control", "Clipboard");
  clipboard.type = "button";
  const sendFile = el("button", "desktop-control", "Send file");
  sendFile.type = "button";
  const chooseSource = el("button", "desktop-control", "Sources");
  chooseSource.type = "button";
  controls.append(control, keyboard, clipboard, sendFile, chooseSource);

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
    button.addEventListener("click", () => {
      if (mode === "desktop") state.desktop?.sendKey(keysym, code);
      else {
        state.windowLink?.send({ kind: "key", code, down: true });
        state.windowLink?.send({ kind: "key", code, down: false });
      }
    });
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
  let mode: "none" | "desktop" | "window" = "none";
  const setControls = (enabled: boolean): void => {
    controlEnabled = enabled;
    if (state.desktop) state.desktop.viewOnly = !enabled;
    control.textContent = enabled ? "Control enabled" : "Enable control";
    control.classList.toggle("enabled", enabled);
    typingInput.disabled = !enabled;
    sendText.disabled = !enabled;
    for (const key of keys.querySelectorAll<HTMLButtonElement>("button")) key.disabled = !enabled;
    touchGate.hidden = enabled || mode === "none";
    setConnectionState(enabled ? "E2E · Live · touch and keyboard control enabled" : "E2E · Live · view only", "online");
  };
  control.addEventListener("click", () => {
    if (mode === "none") return;
    setControls(!controlEnabled);
  });
  touchGate.addEventListener("click", () => setControls(true));
  keyboard.addEventListener("click", () => {
    typingPanel.hidden = !typingPanel.hidden;
    if (!typingPanel.hidden) typingInput.focus();
  });
  clipboard.addEventListener("click", () => {
    if (!controlEnabled || mode === "none") {
      setConnectionState("Enable control before sending clipboard text", "warning");
      return;
    }
    const text = window.prompt("Text to copy into the Mac clipboard");
    if (text === null) return;
    if (mode === "desktop") state.desktop?.clipboardPasteFrom(text);
    else state.windowLink?.send({ kind: "clipboard", text });
  });
  sendFile.addEventListener("click", () => chooseAndUploadFiles(sendFile, (message, failed) => {
    setConnectionState(message, failed ? "offline" : "online");
  }));
  typingForm.addEventListener("submit", (event) => {
    event.preventDefault();
    if (!controlEnabled || mode === "none" || !typingInput.value) return;
    if (mode === "desktop" && state.desktop) sendDesktopText(state.desktop, typingInput.value);
    else state.windowLink?.send({ kind: "text", text: typingInput.value });
    typingInput.value = "";
    typingInput.focus();
  });

  const startFullDesktop = (): void => {
    mode = "desktop";
    sourcePicker.hidden = true;
    controls.hidden = false;
    subtitle.textContent = "Full Mac desktop · E2E VNC";
    empty.querySelector<HTMLElement>(".desktop-waiting-copy")!.textContent = "Waiting for Mac Screen Sharing…";
    setConnectionState("Opening encrypted full-desktop tunnel…", "connecting");
    const link = encryptedBridge!.openDesktop();
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
      state.touchCleanup = installDesktopTouchBridge(mount, () => state.desktop, () => controlEnabled);
      rfb.addEventListener("connect", () => {
        if (state.desktop !== rfb) return;
        empty.remove();
        credentials.hidden = true;
        setControls(false);
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
        setConnectionState(link.lastReason || (clean ? "Remote desktop closed" : "Remote desktop disconnected"), "offline");
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
  };

  const startWindow = (source: WindowSource): void => {
    mode = "window";
    sourcePicker.hidden = true;
    controls.hidden = false;
    subtitle.textContent = `${source.application} · ${source.title}`;
    mount.replaceChildren();
    const canvas = document.createElement("canvas");
    canvas.className = "window-canvas";
    canvas.tabIndex = 0;
    canvas.setAttribute("aria-label", `Remote window: ${source.application}, ${source.title}`);
    mount.append(canvas, touchGate);
    const renderer = createWindowRenderer(canvas);
    setConnectionState("Opening encrypted selected-window stream…", "connecting");
    const link = encryptedBridge!.openWindow(source.id, 1600, 1200, {
      open: () => {
        if (state.windowLink !== link) return;
        setControls(false);
        canvas.focus({ preventScroll: true });
      },
      frame: (data, width, height) => renderer.draw(data, width, height),
      notice: (reason) => setConnectionState(reason, "warning"),
      close: (_code, reason) => {
        if (state.windowLink === link) setConnectionState(reason || "Selected window closed", "offline");
      },
      error: () => setConnectionState("Selected-window stream failed", "offline"),
    });
    state.windowLink = link;
    state.touchCleanup = installWindowInput(canvas, link, () => controlEnabled);
  };

  const loadWindows = async (): Promise<void> => {
    refreshWindows.disabled = true;
    permission.textContent = "Reading encrypted window list…";
    windowList.replaceChildren();
    try {
      const response = await encryptedBridge!.listWindows();
      if (!response.permissions.supported) {
        permission.textContent = "Selected-window mode requires macOS 14 or newer.";
        return;
      }
      if (!response.permissions.screenRecording) {
        permission.textContent = "Screen Recording is not allowed. Run “termlinks desktop permissions” on the Mac, approve it, then restart the connector.";
        return;
      }
      permission.textContent = response.permissions.accessibility
        ? `${response.sources.length} windows available · viewing and control allowed`
        : `${response.sources.length} windows available · viewing allowed; Accessibility permission is required for control`;
      if (response.error) permission.textContent = response.error;
      if (response.sources.length === 0 && !response.error) permission.textContent = "No shareable on-screen windows found.";
      for (const source of response.sources) {
        const button = el("button", "desktop-window-item");
        button.type = "button";
        const copy = el("span", "desktop-window-copy");
        copy.append(el("strong", "desktop-window-app", source.application), el("span", "desktop-window-title", source.title));
        button.append(copy, el("span", "desktop-window-size", `${source.width}×${source.height}`));
        button.addEventListener("click", () => startWindow(source));
        windowList.append(button);
      }
    } catch (caught) {
      permission.textContent = caught instanceof Error ? caught.message : "Could not read the Mac window list";
    } finally {
      refreshWindows.disabled = false;
    }
  };

  fullDesktop.addEventListener("click", startFullDesktop);
  refreshWindows.addEventListener("click", () => { void loadWindows(); });
  chooseSource.addEventListener("click", renderDesktop);
  void loadWindows();
}

function sendDesktopText(rfb: RFB, text: string): void {
  for (const character of text) {
    const codePoint = character.codePointAt(0);
    if (codePoint === undefined) continue;
    const keysym = codePoint <= 0xff ? codePoint : 0x01000000 | codePoint;
    rfb.sendKey(keysym, null);
  }
}

function createWindowRenderer(canvas: HTMLCanvasElement): { draw: (data: Uint8Array, width: number, height: number) => void } {
  const context = canvas.getContext("2d", { alpha: false });
  let drawing = false;
  let pending: { data: Uint8Array; width: number; height: number } | undefined;

  const renderNext = async (): Promise<void> => {
    if (!context || drawing || !pending) return;
    drawing = true;
    const frame = pending;
    pending = undefined;
    try {
      const bitmap = await createImageBitmap(new Blob([frame.data], { type: "image/jpeg" }));
      if (canvas.width !== frame.width || canvas.height !== frame.height) {
        canvas.width = frame.width;
        canvas.height = frame.height;
      }
      context.drawImage(bitmap, 0, 0, frame.width, frame.height);
      bitmap.close();
    } catch {
      setConnectionState("Could not decode a selected-window frame", "warning");
    } finally {
      drawing = false;
      if (pending) void renderNext();
    }
  };

  return {
    draw: (data, width, height) => {
      pending = { data, width, height };
      void renderNext();
    },
  };
}

function installDesktopTouchBridge(mount: HTMLElement, desktop: () => RFB | undefined, controlEnabled: () => boolean): () => void {
  let touchId: number | undefined;
  let lastX = 0;
  let lastY = 0;
  const canvas = (): HTMLCanvasElement | null => mount.querySelector("canvas");
  const dispatchMouse = (type: "mousedown" | "mousemove" | "mouseup", clientX: number, clientY: number): void => {
    const target = canvas();
    if (!target || !desktop()) return;
    target.dispatchEvent(new MouseEvent(type, {
      bubbles: true, cancelable: true, view: window, clientX, clientY,
      button: 0, buttons: type === "mouseup" ? 0 : 1,
    }));
  };
  const findTouch = (touches: TouchList): Touch | undefined => {
    for (let index = 0; index < touches.length; index++) {
      const touch = touches.item(index);
      if (touch && touch.identifier === touchId) return touch;
    }
    return undefined;
  };
  const consume = (event: TouchEvent): void => {
    event.preventDefault();
    event.stopImmediatePropagation();
  };
  const onStart = (event: TouchEvent): void => {
    if (!controlEnabled() || touchId !== undefined || event.touches.length !== 1) return;
    const touch = event.touches.item(0);
    if (!touch) return;
    consume(event);
    touchId = touch.identifier;
    lastX = touch.clientX;
    lastY = touch.clientY;
    dispatchMouse("mousedown", lastX, lastY);
  };
  const onMove = (event: TouchEvent): void => {
    if (!controlEnabled() || touchId === undefined) return;
    const touch = findTouch(event.touches);
    if (!touch) return;
    consume(event);
    lastX = touch.clientX;
    lastY = touch.clientY;
    dispatchMouse("mousemove", lastX, lastY);
  };
  const onEnd = (event: TouchEvent): void => {
    if (touchId === undefined) return;
    const ended = findTouch(event.changedTouches);
    if (!ended && findTouch(event.touches)) return;
    consume(event);
    if (ended) {
      lastX = ended.clientX;
      lastY = ended.clientY;
    }
    dispatchMouse("mouseup", lastX, lastY);
    touchId = undefined;
  };
  mount.addEventListener("touchstart", onStart, { capture: true, passive: false });
  mount.addEventListener("touchmove", onMove, { capture: true, passive: false });
  mount.addEventListener("touchend", onEnd, { capture: true, passive: false });
  mount.addEventListener("touchcancel", onEnd, { capture: true, passive: false });
  return () => {
    mount.removeEventListener("touchstart", onStart, true);
    mount.removeEventListener("touchmove", onMove, true);
    mount.removeEventListener("touchend", onEnd, true);
    mount.removeEventListener("touchcancel", onEnd, true);
  };
}

function installWindowInput(canvas: HTMLCanvasElement, link: EncryptedWindowLink, controlEnabled: () => boolean): () => void {
  const normalized = (clientX: number, clientY: number): { x: number; y: number } => {
    const bounds = canvas.getBoundingClientRect();
    return {
      x: Math.max(0, Math.min(1, (clientX - bounds.left) / Math.max(1, bounds.width))),
      y: Math.max(0, Math.min(1, (clientY - bounds.top) / Math.max(1, bounds.height))),
    };
  };
  const pointerButton = (event: PointerEvent): number => event.button === 2 ? 2 : event.button === 1 ? 1 : 0;
  let pendingMove: PointerEvent | undefined;
  let animationFrame = 0;

  const flushMove = (): void => {
    animationFrame = 0;
    const event = pendingMove;
    pendingMove = undefined;
    if (!event || !controlEnabled()) return;
    const point = normalized(event.clientX, event.clientY);
    link.send({ kind: "pointer", action: event.buttons ? "drag" : "move", ...point, button: event.buttons & 2 ? 2 : event.buttons & 4 ? 1 : 0 });
  };
  const onPointerDown = (event: PointerEvent): void => {
    if (!controlEnabled() || event.pointerType === "touch") return;
    event.preventDefault();
    canvas.focus({ preventScroll: true });
    canvas.setPointerCapture(event.pointerId);
    link.send({ kind: "pointer", action: "down", ...normalized(event.clientX, event.clientY), button: pointerButton(event) });
  };
  const onPointerMove = (event: PointerEvent): void => {
    if (!controlEnabled() || event.pointerType === "touch") return;
    event.preventDefault();
    pendingMove = event;
    if (!animationFrame) animationFrame = requestAnimationFrame(flushMove);
  };
  const onPointerUp = (event: PointerEvent): void => {
    if (!controlEnabled() || event.pointerType === "touch") return;
    event.preventDefault();
    link.send({ kind: "pointer", action: "up", ...normalized(event.clientX, event.clientY), button: pointerButton(event) });
    if (canvas.hasPointerCapture(event.pointerId)) canvas.releasePointerCapture(event.pointerId);
  };
  const onWheel = (event: WheelEvent): void => {
    if (!controlEnabled()) return;
    event.preventDefault();
    const bounds = canvas.getBoundingClientRect();
    link.send({
      kind: "pointer",
      action: "scroll",
      x: Math.max(0, Math.min(1, (event.clientX - bounds.left) / Math.max(1, bounds.width))),
      y: Math.max(0, Math.min(1, (event.clientY - bounds.top) / Math.max(1, bounds.height))),
      button: 0,
      deltaX: Math.max(-4000, Math.min(4000, event.deltaX)),
      deltaY: Math.max(-4000, Math.min(4000, event.deltaY)),
    });
  };
  const onKey = (event: KeyboardEvent): void => {
    if (!controlEnabled() || event.isComposing || !event.code) return;
    event.preventDefault();
    link.send({ kind: "key", code: event.code, down: event.type === "keydown", shift: event.shiftKey, ctrl: event.ctrlKey, alt: event.altKey, meta: event.metaKey });
  };
  const preventMenu = (event: Event): void => {
    if (controlEnabled()) event.preventDefault();
  };
  let touchId: number | undefined;
  let touchPoint = { x: 0, y: 0 };
  let pendingTouchPoint: { x: number; y: number } | undefined;
  let touchAnimationFrame = 0;
  const findTouch = (touches: TouchList): Touch | undefined => {
    for (let index = 0; index < touches.length; index++) {
      const touch = touches.item(index);
      if (touch && touch.identifier === touchId) return touch;
    }
    return undefined;
  };
  const flushTouchMove = (): void => {
    touchAnimationFrame = 0;
    if (!pendingTouchPoint || !controlEnabled() || touchId === undefined) return;
    touchPoint = pendingTouchPoint;
    pendingTouchPoint = undefined;
    link.send({ kind: "pointer", action: "drag", ...touchPoint, button: 0 });
  };
  const onTouchStart = (event: TouchEvent): void => {
    if (!controlEnabled() || touchId !== undefined || event.touches.length !== 1) return;
    const touch = event.touches.item(0);
    if (!touch) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    canvas.focus({ preventScroll: true });
    touchId = touch.identifier;
    touchPoint = normalized(touch.clientX, touch.clientY);
    link.send({ kind: "pointer", action: "down", ...touchPoint, button: 0 });
  };
  const onTouchMove = (event: TouchEvent): void => {
    if (!controlEnabled() || touchId === undefined) return;
    const touch = findTouch(event.touches);
    if (!touch) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    pendingTouchPoint = normalized(touch.clientX, touch.clientY);
    if (!touchAnimationFrame) touchAnimationFrame = requestAnimationFrame(flushTouchMove);
  };
  const onTouchEnd = (event: TouchEvent): void => {
    if (touchId === undefined) return;
    const ended = findTouch(event.changedTouches);
    if (!ended && findTouch(event.touches)) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    if (touchAnimationFrame) cancelAnimationFrame(touchAnimationFrame);
    touchAnimationFrame = 0;
    pendingTouchPoint = undefined;
    if (ended) touchPoint = normalized(ended.clientX, ended.clientY);
    link.send({ kind: "pointer", action: "up", ...touchPoint, button: 0 });
    touchId = undefined;
  };

  canvas.addEventListener("pointerdown", onPointerDown);
  canvas.addEventListener("pointermove", onPointerMove);
  canvas.addEventListener("pointerup", onPointerUp);
  canvas.addEventListener("pointercancel", onPointerUp);
  canvas.addEventListener("wheel", onWheel, { passive: false });
  canvas.addEventListener("keydown", onKey);
  canvas.addEventListener("keyup", onKey);
  canvas.addEventListener("contextmenu", preventMenu);
  canvas.addEventListener("touchstart", onTouchStart, { passive: false });
  canvas.addEventListener("touchmove", onTouchMove, { passive: false });
  canvas.addEventListener("touchend", onTouchEnd, { passive: false });
  canvas.addEventListener("touchcancel", onTouchEnd, { passive: false });
  return () => {
    if (animationFrame) cancelAnimationFrame(animationFrame);
    if (touchAnimationFrame) cancelAnimationFrame(touchAnimationFrame);
    canvas.removeEventListener("pointerdown", onPointerDown);
    canvas.removeEventListener("pointermove", onPointerMove);
    canvas.removeEventListener("pointerup", onPointerUp);
    canvas.removeEventListener("pointercancel", onPointerUp);
    canvas.removeEventListener("wheel", onWheel);
    canvas.removeEventListener("keydown", onKey);
    canvas.removeEventListener("keyup", onKey);
    canvas.removeEventListener("contextmenu", preventMenu);
    canvas.removeEventListener("touchstart", onTouchStart);
    canvas.removeEventListener("touchmove", onTouchMove);
    canvas.removeEventListener("touchend", onTouchEnd);
    canvas.removeEventListener("touchcancel", onTouchEnd);
  };
}

function renderCreatePanel(close: () => void): HTMLElement {
  const panel = el("section", "create-panel");
  panel.id = "create-terminal-panel";
  panel.hidden = true;
  const intro = el("div", "create-intro");
  intro.append(
    el("h2", "create-title", "Open a new shell"),
    el("p", "create-copy", state.viewerControlAvailable
      ? "This creates a persistent shell and opens it here without adding a window on your computer. You can open that exact session on the computer whenever you need it."
      : "Your running local service is older, so this shell will also open on the computer. Existing sessions are safe; restart Termlinks only after finishing them to enable headless creation."),
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
  const createLabel = state.viewerControlAvailable ? "Create and open here" : "Create here + computer";
  const submit = el("button", "create-submit", createLabel);
  submit.type = "submit";
  actions.append(cancel, submit);
  form.append(nameField, cwdField, error, actions);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    error.textContent = "";
    submit.disabled = true;
    submit.textContent = "Creating…";
    try {
      const created = await createTerminalFromTemplate({
        name: name.value.trim(),
        cwd: cwd.value.trim(),
      });
      renderTerminal(created.id);
    } catch (caught) {
      error.textContent = caught instanceof Error ? caught.message : "Could not create the terminal";
      submit.disabled = false;
      submit.textContent = createLabel;
    }
  });
  panel.append(intro, form);
  return panel;
}

async function createTerminalFromTemplate(template: TerminalTemplate): Promise<Session> {
  const created = await api<Session>("/api/sessions", {
    method: "POST",
    body: JSON.stringify({ name: template.name, cwd: template.cwd }),
  });
  state.sessions = [created, ...state.sessions.filter((item) => item.id !== created.id)];
  return created;
}

async function openSavedTerminal(saved: SavedTerminal, button?: HTMLButtonElement): Promise<void> {
  const running = runningSessionForSaved(saved);
  if (running) {
    renderTerminal(running.id);
    return;
  }
  const originalText = button?.textContent ?? "";
  if (button) {
    button.disabled = true;
    button.textContent = "Opening…";
  }
  try {
    const created = await api<Session>(`/api/terminal-history/${encodeURIComponent(saved.id)}/open`, { method: "POST" });
    state.sessions = [created, ...state.sessions.filter((item) => item.id !== created.id)];
    await loadTerminalHistory();
    renderTerminal(created.id);
  } catch (caught) {
    if (button) {
      button.disabled = false;
      button.textContent = caught instanceof Error ? caught.message : "Could not open";
      window.setTimeout(() => { button.textContent = originalText; }, 1800);
    } else {
      window.alert(caught instanceof Error ? caught.message : "Could not open the terminal.");
    }
  }
}

async function duplicateTerminal(template: TerminalTemplate, button?: HTMLButtonElement): Promise<void> {
  const originalText = button?.textContent ?? "";
  if (button) {
    button.disabled = true;
    button.textContent = "Creating…";
  }
  try {
    const created = await createTerminalFromTemplate({ ...template, name: duplicateTerminalName(template.name) });
    renderTerminal(created.id);
  } catch (caught) {
    if (button) {
      button.disabled = false;
      button.textContent = caught instanceof Error ? caught.message : "Could not duplicate";
      window.setTimeout(() => { button.textContent = originalText; }, 1800);
    } else {
      window.alert(caught instanceof Error ? caught.message : "Could not duplicate the terminal.");
    }
  }
}

async function renameRunningSession(session: Session, forcedName?: string): Promise<Session | undefined> {
  const name = normalizeTerminalNameInput(forcedName ?? window.prompt("Terminal name", session.name));
  if (!name || name === session.name) return undefined;
  const updated = await api<Session>(`/api/sessions/${encodeURIComponent(session.id)}`, {
    method: "PATCH",
    body: JSON.stringify({ name }),
  });
  state.sessions = state.sessions.map((item) => item.id === updated.id ? updated : item);
  await loadTerminalHistory();
  return updated;
}

async function renameSavedTerminal(saved: SavedTerminal): Promise<void> {
  const name = normalizeTerminalNameInput(window.prompt("Terminal name", saved.name));
  if (!name || name === saved.name) return;
  const running = runningSessionForSaved(saved);
  if (running) {
    try {
      await renameRunningSession(running, name);
    } catch (caught) {
      window.alert(caught instanceof Error ? caught.message : "Could not rename the terminal.");
      return;
    }
  } else {
    try {
      await updateSavedTerminal(saved, { name });
    } catch (caught) {
      window.alert(caught instanceof Error ? caught.message : "Could not rename the saved terminal.");
      return;
    }
  }
  const container = document.querySelector<HTMLElement>("#session-list");
  if (container) {
    renderSessionCards(container);
    updateSessionSummary();
  }
}

function renderStartHint(): HTMLElement {
  const hint = el("aside", "start-hint");
  const icon = el("span", "hint-icon", "+");
  const copy = el("div");
  copy.append(
    el("strong", "hint-title", state.terminalHistoryAvailable ? "Your shells stay managed" : "Terminal history needs a local update"),
    el("p", "hint-copy", state.terminalHistoryAvailable
      ? "Leaving this page only disconnects the viewer. A terminal moves to Recent history after it exits or you stop it. History and favorites stay privately on this computer."
      : "Running terminals are safe and still available. Restart Termlinks after finishing active work to enable private history and favorites."),
    el("code", "hint-command", state.terminalHistoryAvailable ? "termlinks list  ·  show/hide <id>  ·  stop <id>" : "termlinks update  ·  termlinks restart"),
  );
  hint.append(icon, copy);
  return hint;
}

function renderTermAdsTeaser(): HTMLElement {
  const teaser = document.createElement("a");
  teaser.className = "termads-teaser";
  teaser.href = "https://termads.dev/";
  teaser.target = "_blank";
  teaser.rel = "noopener noreferrer";
  teaser.setAttribute("aria-label", "TermAds, coming soon to Termlinks (opens in a new tab)");
  const mark = el("span", "termads-mark", "ad_");
  const copy = el("span", "termads-copy");
  copy.append(
    el("strong", "termads-name", "TermAds"),
    el("span", "termads-description", "Developer-tool sponsorships, built for the terminal."),
  );
  const status = el("span", "termads-status", "COMING SOON ↗");
  teaser.append(mark, copy, status);
  return teaser;
}

function renderSessionCards(container: HTMLElement): void {
  const groups = visibleSavedGroups(state.savedTerminals, new Set(state.sessions.map((item) => item.id)));
  container.replaceChildren();
  container.append(
    renderRunningTerminalSection(),
    renderSavedTerminalSection("Favorites", groups.favorites),
    renderSavedTerminalSection("Recent history", groups.recent),
  );
}

function renderRunningTerminalSection(): HTMLElement {
  const section = renderDashboardSection("Running", state.sessions.length);
  const list = el("div", "session-section-list");
  if (state.sessions.length === 0) {
    const empty = el("div", "empty-state");
    empty.append(el("span", "empty-prompt", "$_"), el("h2", "empty-title", "Nothing running yet"), el("p", "empty-copy", "Tap New terminal to open an interactive shell on your computer."));
    list.append(empty);
    section.append(list);
    return section;
  }
  for (const session of state.sessions) {
    list.append(renderRunningSessionCard(session));
  }
  section.append(list);
  return section;
}

function renderSavedTerminalSection(title: string, savedTerminals: SavedTerminal[]): HTMLElement {
  const section = renderDashboardSection(title, savedTerminals.length);
  const list = el("div", "session-section-list");
  if (savedTerminals.length === 0) {
    list.append(el("p", "saved-empty", title === "Favorites" ? "No favorites saved" : "No recent history"));
  } else {
    for (const saved of savedTerminals) list.append(renderSavedTerminalCard(saved));
  }
  section.append(list);
  return section;
}

function renderDashboardSection(title: string, count: number): HTMLElement {
  const section = el("section", "session-section");
  const heading = el("div", "session-section-heading");
  heading.append(el("h2", "session-section-title", title), el("span", "session-section-count", String(count)));
  section.append(heading);
  return section;
}

function renderRunningSessionCard(session: Session): HTMLElement {
  const card = el("article", "session-card");
  const open = el("button", "session-open");
  open.type = "button";
  open.setAttribute("aria-label", `Open ${session.name} terminal`);
  open.addEventListener("click", () => renderTerminal(session.id));
  const row = el("div", "session-card-row");
  const identity = el("div", "session-identity");
  const folder = el("span", "project-label", projectLabel(session.cwd));
  folder.title = session.cwd;
  identity.append(el("span", "session-dot live"), folder, el("h2", "session-name", session.name));
  const viewerBadge = session.viewer === "visible" ? "ON COMPUTER" : session.viewer === "opening" ? "OPENING" : session.viewer === "hidden" ? "HEADLESS" : "RUNNING";
  const badge = el("span", `status-badge running viewer-${session.viewer ?? "legacy"}`, viewerBadge);
  row.append(identity, badge);
  const command = el("code", "session-command", `$ ${session.command.join(" ")}`);
  const meta = el("div", "session-meta");
  const cwd = el("span", "cwd", compactPath(session.cwd));
  cwd.title = session.cwd;
  meta.append(cwd, el("span", "session-age", ageLabel(session)), el("span", "card-arrow", "›"));
  open.append(row, command, meta);

  const controls = el("div", "card-controls");
  const openAction = el("button", "card-action", "Open here");
  openAction.type = "button";
  openAction.addEventListener("click", () => renderTerminal(session.id));
  const renameAction = el("button", "card-action", "Rename");
  renameAction.type = "button";
  renameAction.addEventListener("click", () => {
    void renameRunningSession(session)
      .then(() => refreshDashboardCards())
      .catch((caught) => {
        window.alert(caught instanceof Error ? caught.message : "Could not rename the terminal.");
      });
  });
  const duplicateAction = el("button", "card-action", "Duplicate");
  duplicateAction.type = "button";
  duplicateAction.addEventListener("click", () => { void duplicateTerminal(session, duplicateAction); });
  const favoriteAction = el("button", "card-action", savedTerminalBySession(session)?.favorite ? "Unfavorite" : "Favorite");
  favoriteAction.type = "button";
  favoriteAction.addEventListener("click", () => {
    void toggleFavoriteForSession(session)
      .then(refreshDashboardCards)
      .catch((caught) => window.alert(caught instanceof Error ? caught.message : "Could not update favorite."));
  });
  const stopAction = el("button", "card-action danger", "Stop & close");
  stopAction.type = "button";
  stopAction.addEventListener("click", () => stopFromDashboard(session, stopAction));
  controls.append(openAction, createNativeViewerButton(session, "card-action"), renameAction, duplicateAction, favoriteAction, stopAction);
  card.append(open, controls);
  return card;
}

function renderSavedTerminalCard(saved: SavedTerminal): HTMLElement {
  const running = runningSessionForSaved(saved);
  const card = el("article", "session-card saved-terminal-card");
  const open = el("button", "session-open");
  open.type = "button";
  open.setAttribute("aria-label", `${running ? "Open" : "Open a new shell for"} ${saved.name}`);
  open.addEventListener("click", () => { void openSavedTerminal(saved); });
  const row = el("div", "session-card-row");
  const identity = el("div", "session-identity");
  const folder = el("span", "project-label", projectLabel(saved.cwd));
  folder.title = saved.cwd;
  identity.append(el("span", `session-dot ${running ? "live" : "ended"}`), folder, el("h2", "session-name", saved.name));
  const badge = el("span", `status-badge ${running ? "running" : saved.favorite ? "favorite" : "finished"}`, running ? "RUNNING" : saved.favorite ? "FAVORITE" : "RECENT");
  row.append(identity, badge);
  const command = el("code", "session-command", running ? `$ ${running.command.join(" ")}` : "$ new interactive shell");
  const meta = el("div", "session-meta");
  const cwd = el("span", "cwd", compactPath(saved.cwd));
  cwd.title = saved.cwd;
  meta.append(cwd, el("span", "session-age", savedAgeLabel(saved)), el("span", "card-arrow", "›"));
  open.append(row, command, meta);

  const controls = el("div", "card-controls");
  const openAction = el("button", "card-action", running ? "Open here" : state.viewerControlAvailable ? "Open new shell" : "Open new shell + computer");
  openAction.type = "button";
  openAction.addEventListener("click", () => { void openSavedTerminal(saved, openAction); });
  const renameAction = el("button", "card-action", "Rename");
  renameAction.type = "button";
  renameAction.addEventListener("click", () => { void renameSavedTerminal(saved); });
  const duplicateAction = el("button", "card-action", "New copy shell");
  duplicateAction.type = "button";
  duplicateAction.addEventListener("click", () => { void duplicateTerminal(saved, duplicateAction); });
  const favoriteAction = el("button", "card-action", saved.favorite ? "Unfavorite" : "Favorite");
  favoriteAction.type = "button";
  favoriteAction.addEventListener("click", () => {
    void toggleFavoriteForSaved(saved)
      .then(refreshDashboardCards)
      .catch((caught) => window.alert(caught instanceof Error ? caught.message : "Could not update favorite."));
  });
  const removeAction = el("button", "card-action danger", "Remove");
  removeAction.type = "button";
  removeAction.addEventListener("click", () => {
    if (!window.confirm(`Remove "${saved.name}" from saved terminals?`)) return;
    void removeSavedTerminal(saved)
      .then(refreshDashboardCards)
      .catch((caught) => window.alert(caught instanceof Error ? caught.message : "Could not remove saved terminal."));
  });
  controls.append(openAction, renameAction, duplicateAction, favoriteAction, removeAction);
  card.append(open, controls);
  return card;
}

async function toggleFavoriteForSession(session: Session): Promise<void> {
  const saved = savedTerminalBySession(session);
  if (saved?.favorite) {
    if (saved.lastClosedAt) await updateSavedTerminal(saved, { favorite: false });
    else await removeSavedTerminal(saved);
    return;
  }
  if (saved) {
    await updateSavedTerminal(saved, { favorite: true });
    return;
  }
  const response = await api<unknown>(`/api/terminal-history/session/${encodeURIComponent(session.id)}/favorite`, { method: "POST" });
  const created = decodeSavedTerminal(response);
  replaceSavedTerminal(created);
}

async function toggleFavoriteForSaved(saved: SavedTerminal): Promise<void> {
  await updateSavedTerminal(saved, { favorite: !saved.favorite });
}

function refreshDashboardCards(): void {
  const container = document.querySelector<HTMLElement>("#session-list");
  if (!container) return;
  renderSessionCards(container);
  updateSessionSummary();
}

function nativeViewerButtonText(session: Session): string {
  switch (session.viewer) {
  case "visible": return "Hide on computer";
  case "opening": return "Cancel opening";
  case "hidden": return "Open on computer";
  case "unsupported": return "Desktop viewer unavailable";
  default: return "Update local app";
  }
}

function createNativeViewerButton(session: Session, className: string): HTMLButtonElement {
  const button = el("button", className, nativeViewerButtonText(session));
  button.type = "button";
  button.disabled = session.viewer === undefined || session.viewer === "unsupported" || !session.running;
  if (session.viewer === undefined) button.title = "Run termlinks update, then restart the daemon after active sessions finish";
  if (session.viewer === "unsupported") button.title = "Native terminal viewers are disabled or unsupported on this computer";
  button.addEventListener("click", () => { void toggleNativeViewer(session, button); });
  return button;
}

async function toggleNativeViewer(session: Session, button: HTMLButtonElement): Promise<void> {
  const action = session.viewer === "visible" || session.viewer === "opening" ? "hide" : "show";
  button.disabled = true;
  button.textContent = action === "show" ? "Opening…" : "Hiding…";
  try {
    const result = await api<{ viewer: Session["viewer"] }>(`/api/sessions/${encodeURIComponent(session.id)}/viewer/${action}`, { method: "POST" });
    session.viewer = result.viewer;
    button.textContent = nativeViewerButtonText(session);
    button.disabled = session.viewer === "unsupported";
    if (document.querySelector("#session-list")) refreshDashboardCards();
    if (action === "show" && session.viewer === "opening") void refreshNativeViewerStatus(session, button);
  } catch (caught) {
    button.disabled = false;
    button.textContent = nativeViewerButtonText(session);
    window.alert(caught instanceof Error ? caught.message : "Could not change the desktop viewer");
  }
}

async function refreshNativeViewerStatus(session: Session, button: HTMLButtonElement): Promise<void> {
  for (let attempt = 0; attempt < 15 && session.viewer === "opening"; attempt += 1) {
    await new Promise((resolve) => window.setTimeout(resolve, 700));
    try {
      const response = await api<{ sessions: Session[] }>("/api/sessions");
      const fresh = response.sessions.find((item) => item.id === session.id);
      if (!fresh) return;
      session.viewer = fresh.viewer;
      state.sessions = state.sessions.map((item) => item.id === fresh.id ? { ...item, viewer: fresh.viewer } : item);
      if (document.body.contains(button)) {
        button.textContent = nativeViewerButtonText(session);
        button.disabled = session.viewer === undefined || session.viewer === "unsupported";
      }
      if (document.querySelector("#session-list")) refreshDashboardCards();
    } catch {
      return;
    }
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
  const favorites = favoriteSavedTerminals().length;
  const recent = recentSavedTerminals().length;
  const saved = favorites + recent;
  const summary = document.querySelector<HTMLElement>("#session-summary");
  if (summary) summary.textContent = saved > 0 ? `${running} running · ${favorites} favorites · ${recent} recent` : `${running} running`;
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

function renderTerminal(id: string, workflowID?: string): void {
  stopPolling();
  closeConnection();
  const session = state.sessions.find((item) => item.id === id);
  if (!session) return renderSessions();
  state.selected = id;
  if (workflowID !== undefined) state.selectedWorkflow = workflowID;
  else if (state.view !== "terminal") state.selectedWorkflow = undefined;
  rememberPortalView("terminal", id, state.selectedWorkflow);
  app.replaceChildren();
  const page = el("main", "terminal-page");
  page.dataset.inputMode = readTerminalInputMode();
  const header = el("header", "terminal-header");
  const back = el("button", "back-button terminal-back-button", "‹");
  back.type = "button";
  back.setAttribute("aria-label", state.selectedWorkflow ? "Back to AI workflow" : "Back to sessions");
  back.addEventListener("click", () => {
    if (state.selectedWorkflow) void renderWorkflowDetail(state.selectedWorkflow);
    else renderSessions();
  });
  const identity = el("div", "terminal-identity");
  const title = el("div", "terminal-title-row");
  title.append(el("strong", "terminal-name", session.name), el("span", "terminal-live-badge", "LIVE"));
  identity.append(title, el("span", "terminal-subtitle", shortCommand(session.command)));
  const menu = el("button", "icon-button terminal-menu-button", "•••");
  menu.type = "button";
  menu.title = "Session actions";
  menu.setAttribute("aria-label", "Session actions");
  menu.addEventListener("click", () => actions.classList.toggle("open"));
  const inputModeButton = el("button", "terminal-input-mode-button");
  inputModeButton.type = "button";
  const headerActions = el("div", "terminal-header-actions");
  headerActions.append(inputModeButton, menu);
  header.append(back, identity, headerActions);

  const actions = el("div", "actions-menu");
  const reconnect = el("button", "menu-button", "Reconnect");
  reconnect.type = "button";
  reconnect.addEventListener("click", () => connectTerminal(session));
  const terminalText = el("button", "menu-button", "Copy visible terminal output");
  terminalText.type = "button";
  terminalText.addEventListener("click", () => {
    actions.classList.remove("open");
    void copyVisibleTerminalOutput();
  });
  const rename = el("button", "menu-button", "Rename terminal");
  rename.type = "button";
  rename.addEventListener("click", () => {
    actions.classList.remove("open");
    void renameRunningSession(session)
      .then((updated) => {
        if (!updated) return;
        session.name = updated.name;
        const terminalName = document.querySelector<HTMLElement>(".terminal-name");
        if (terminalName) terminalName.textContent = updated.name;
        const activeTab = document.querySelector<HTMLElement>(".terminal-tab.active .terminal-tab-name");
        if (activeTab) activeTab.textContent = updated.name;
      })
      .catch((caught) => {
        setConnectionState(caught instanceof Error ? caught.message : "Could not rename", "warning");
      });
  });
  const favorite = el("button", "menu-button", savedTerminalBySession(session)?.favorite ? "Remove from favorites" : "Add to favorites");
  favorite.type = "button";
  favorite.addEventListener("click", () => {
    actions.classList.remove("open");
    void toggleFavoriteForSession(session)
      .then(() => {
        favorite.textContent = savedTerminalBySession(session)?.favorite ? "Remove from favorites" : "Add to favorites";
      })
      .catch((caught) => setConnectionState(caught instanceof Error ? caught.message : "Could not update favorite", "warning"));
  });
	const duplicate = el("button", "menu-button", "Open copy shell");
  duplicate.type = "button";
  duplicate.addEventListener("click", () => {
    actions.classList.remove("open");
    void duplicateTerminal(session, duplicate);
  });
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
  const nativeViewer = createNativeViewerButton(session, "menu-button");
  nativeViewer.addEventListener("click", () => actions.classList.remove("open"));
  actions.append(reconnect, nativeViewer, rename, favorite, duplicate, terminalText, stop);

  const connection = el("div", "connection-bar");
  connection.id = "connection-state";
  connection.append(
    el("span", "connection-dot"),
    el("span", "connection-label", "Connecting…"),
    el("span", "connection-security", encryptedPortal ? "E2E" : "LOCAL"),
  );
  const frame = el("section", "terminal-frame");
  const mount = el("div", "terminal-mount");
  mount.id = "terminal";
  const sync = el("div", "terminal-sync-indicator");
  sync.hidden = true;
  sync.setAttribute("role", "status");
  sync.setAttribute("aria-live", "polite");
  sync.append(el("span", "terminal-sync-dot"), el("span", "terminal-sync-label", "Reconnecting…"));
  const tuiHint = el("div", "terminal-tui-hint", "TUI · swipe controls app");
  tuiHint.hidden = true;
  tuiHint.setAttribute("aria-live", "polite");
  frame.append(mount, sync, tuiHint);
  const composer = renderTerminalComposer();
  page.append(header, actions, connection, frame, renderTerminalTabs(session.id), composer);
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
    // event. Keep it disabled because touch devices use the native WebKit
    // scroll container installed below.
    smoothScrollDuration: 0,
    theme: {
      background: "#090d13", foreground: "#d7dee9", cursor: "#5cc8f5", cursorAccent: "#090d13",
      selectionBackground: "#2b6f9b66", black: "#111820", brightBlack: "#637180",
      green: "#71d99a", brightGreen: "#99edb8", cyan: "#63cce9", brightCyan: "#8eddf3",
      blue: "#64a8ff", brightBlue: "#8fc1ff", yellow: "#e5c07b", brightYellow: "#f0d79a",
    },
  });
  const fit = new FitAddon();
  terminal.loadAddon(fit);
  terminal.open(mount);
  state.terminal = terminal;
  state.terminalSessionID = session.id;
  state.terminalSnapshotApplied = false;
  const terminalReplyGate = new TerminalReplyGate();
  state.terminalReplyGate = terminalReplyGate;
  state.fit = fit;
  const touchScroll = enableTouchScroll(terminal, tuiHint, () => page.dataset.inputMode === "direct");
  state.touchCleanup = touchScroll.cleanup;
  state.touchSync = touchScroll.align;
  state.layoutCleanup = installTerminalViewportSizing(page);
  fitTerminal();
  if (!window.matchMedia("(pointer: coarse)").matches) terminal.focus();
  page.addEventListener("keydown", (event) => {
    if (
      page.dataset.inputMode !== "direct"
      || event.key !== "Enter"
      || event.isComposing
      || !(event.target instanceof HTMLElement)
      || !event.target.classList.contains("xterm-helper-textarea")
    ) return;
    // WebKit can stop xterm's hidden textarea from translating Return after
    // a full-screen TUI redraw even though ordinary character keys continue
    // to work. Capture the key before xterm so the physical/software Return
    // key always becomes exactly one terminal carriage return.
    if (!sendTerminalInput("\r")) return;
    event.preventDefault();
    event.stopImmediatePropagation();
  }, { capture: true });
  terminal.onData((data) => {
    const reply = terminalReplyGate.receive(new TextEncoder().encode(data));
    if (reply && state.socket?.readyState === WebSocket.OPEN) state.socket.send(reply);
  });
  terminal.onBinary((data) => {
    const reply = terminalReplyGate.receive(binaryStringToBytes(data));
    if (reply && state.socket?.readyState === WebSocket.OPEN) state.socket.send(reply);
  });
  const applyInputMode = (mode: TerminalInputMode, persist: boolean): void => {
    page.dataset.inputMode = mode;
    const direct = mode === "direct";
    inputModeButton.textContent = direct ? "Compose" : "Direct";
    inputModeButton.title = direct ? "Use the message composer" : "Use Termius-style direct terminal input";
    inputModeButton.setAttribute("aria-label", inputModeButton.title);
    inputModeButton.setAttribute("aria-pressed", String(direct));
    if (direct && document.activeElement?.classList.contains("terminal-composer-input")) {
      (document.activeElement as HTMLElement).blur();
    }
    touchScroll.refresh();
    if (persist) saveTerminalInputMode(mode);
    window.requestAnimationFrame(fitTerminal);
  };
  inputModeButton.addEventListener("click", () => {
    applyInputMode(nextTerminalInputMode(parseTerminalInputMode(page.dataset.inputMode)), true);
  });
  applyInputMode(parseTerminalInputMode(page.dataset.inputMode), false);
  connectTerminal(session);
  const resize = new ResizeObserver(fitTerminal);
  resize.observe(frame);
  const onOrientationChange = (): void => fitTerminal();
  window.addEventListener("orientationchange", onOrientationChange);
  const viewportCleanup = state.layoutCleanup;
  state.layoutCleanup = () => {
    resize.disconnect();
    window.removeEventListener("orientationchange", onOrientationChange);
    viewportCleanup?.();
  };
}

function renderTerminalTabs(activeSessionId: string): HTMLElement {
  const navigation = el("nav", "terminal-tab-bar");
  navigation.setAttribute("aria-label", "Terminal navigation");

  const sessions = el("button", "terminal-tab-action", "☷");
  sessions.type = "button";
  sessions.title = "All terminal sessions";
  sessions.setAttribute("aria-label", "Show all terminal sessions");
  sessions.addEventListener("click", renderSessions);

  const list = el("div", "terminal-tab-list");
  list.setAttribute("role", "tablist");
  list.setAttribute("aria-label", "Running terminals");
  let activeTab: HTMLButtonElement | undefined;
  const runningSessions = orderedRunningSessions();
  for (const [index, session] of runningSessions.entries()) {
    const tab = el("button", "terminal-tab");
    tab.type = "button";
    tab.dataset.sessionId = session.id;
    tab.setAttribute("role", "tab");
    const active = session.id === activeSessionId;
    tab.classList.toggle("active", active);
    tab.setAttribute("aria-selected", String(active));
    if (active) tab.setAttribute("aria-current", "page");
    tab.title = `${session.name} · ${shortCommand(session.command)}`;
    const dragHandle = el("span", "terminal-tab-drag-handle");
    dragHandle.title = "Hold and drag to reposition";
    dragHandle.setAttribute("aria-hidden", "true");
    tab.append(
      dragHandle,
      el("span", "terminal-tab-index", String(index + 1)),
      el("span", "terminal-tab-name", session.name),
      el("span", "terminal-tab-dot"),
    );
    tab.addEventListener("click", (event) => {
      if (tab.dataset.suppressClick === "true") {
        event.preventDefault();
        return;
      }
      if (!active) renderTerminal(session.id);
    });
    tab.addEventListener("keydown", (event) => {
      if (!event.altKey || (event.key !== "ArrowLeft" && event.key !== "ArrowRight")) return;
      event.preventDefault();
      moveTerminalTabWithKeyboard(tab, list, event.key === "ArrowLeft" ? -1 : 1);
    });
    list.append(tab);
    if (active) activeTab = tab;
  }
  updateTerminalTabPositions(list);
  installTerminalTabRailGestures(list);

  const create = el("button", "terminal-tab-action terminal-tab-create", "+");
  create.type = "button";
  create.title = "Open a new terminal";
  create.setAttribute("aria-label", "Open a new terminal");
  create.addEventListener("click", renderSessionsWithCreate);
  navigation.append(sessions, list, create);
  window.requestAnimationFrame(() => {
    if (!activeTab) return;
    list.scrollLeft = Math.max(0, activeTab.offsetLeft - ((list.clientWidth - activeTab.clientWidth) / 2));
  });
  return navigation;
}

function readTerminalTabOrder(): string[] {
  try {
    const stored: unknown = JSON.parse(localStorage.getItem(TERMINAL_TAB_ORDER_KEY) || "[]");
    if (!Array.isArray(stored)) return [];
    const unique = new Set<string>();
    for (const value of stored) {
      if (typeof value !== "string" || value.length === 0 || value.length > 160) continue;
      unique.add(value);
      if (unique.size >= MAX_PERSISTED_TERMINAL_TABS) break;
    }
    return Array.from(unique);
  } catch {
    return [];
  }
}

function writeTerminalTabOrder(ids: string[]): void {
  try {
    localStorage.setItem(TERMINAL_TAB_ORDER_KEY, JSON.stringify(ids.slice(0, MAX_PERSISTED_TERMINAL_TABS)));
  } catch {
    // Private browsing and managed browsers may reject local storage writes.
  }
}

function orderedRunningSessions(): Session[] {
  const running = state.sessions.filter((item) => item.running);
  const byId = new Map(running.map((session) => [session.id, session]));
  const ordered: Session[] = [];
  for (const id of readTerminalTabOrder()) {
    const session = byId.get(id);
    if (!session) continue;
    ordered.push(session);
    byId.delete(id);
  }
  for (const session of running) {
    if (byId.delete(session.id)) ordered.push(session);
  }
  const normalized = ordered.map((session) => session.id);
  if (normalized.length > 0) writeTerminalTabOrder(normalized);
  return ordered;
}

function updateTerminalTabPositions(list: HTMLElement): void {
  const tabs = Array.from(list.querySelectorAll<HTMLButtonElement>(".terminal-tab"));
  for (const [index, tab] of tabs.entries()) {
    tab.querySelector<HTMLElement>(".terminal-tab-index")!.textContent = String(index + 1);
    tab.setAttribute("aria-posinset", String(index + 1));
    tab.setAttribute("aria-setsize", String(tabs.length));
    const name = tab.querySelector<HTMLElement>(".terminal-tab-name")?.textContent || "terminal";
    tab.setAttribute("aria-label", `${name}, tab ${index + 1} of ${tabs.length}. Hold the drag grip or press Alt and an arrow key to reposition.`);
  }
}

function persistTerminalTabOrder(list: HTMLElement): void {
  const ids = Array.from(list.querySelectorAll<HTMLElement>(".terminal-tab"))
    .map((tab) => tab.dataset.sessionId)
    .filter((id): id is string => Boolean(id));
  writeTerminalTabOrder(ids);
}

function moveTerminalTabWithKeyboard(tab: HTMLButtonElement, list: HTMLElement, direction: -1 | 1): void {
  const tabs = Array.from(list.querySelectorAll<HTMLButtonElement>(".terminal-tab"));
  const index = tabs.indexOf(tab);
  const target = tabs[index + direction];
  if (index < 0 || !target) return;
  if (direction < 0) list.insertBefore(tab, target);
  else list.insertBefore(tab, target.nextSibling);
  updateTerminalTabPositions(list);
  persistTerminalTabOrder(list);
  tab.scrollIntoView({ behavior: "smooth", block: "nearest", inline: "center" });
}

function installTerminalTabRailGestures(list: HTMLElement): void {
  let pointerId: number | undefined;
  let pressTimer = 0;
  let reorderTab: HTMLButtonElement | undefined;
  let pressedTab: HTMLButtonElement | undefined;
  let scrolling = false;
  let startX = 0;
  let startY = 0;
  let lastX = 0;
  let lastTime = 0;
  let velocity = 0;
  let momentumFrame = 0;

  const clearPressTimer = (): void => {
    if (pressTimer) window.clearTimeout(pressTimer);
    pressTimer = 0;
  };
  const stopMomentum = (): void => {
    if (momentumFrame) window.cancelAnimationFrame(momentumFrame);
    momentumFrame = 0;
  };
  const beginDrag = (): void => {
    pressTimer = 0;
    if (pointerId === undefined || !pressedTab || scrolling) return;
    reorderTab = pressedTab;
    reorderTab.dataset.suppressClick = "true";
    reorderTab.classList.add("dragging");
    list.classList.add("reordering");
  };
  const startMomentum = (): void => {
    if (Math.abs(velocity) < 0.08) return;
    let previous = performance.now();
    const step = (now: number): void => {
      const elapsed = Math.min(32, now - previous);
      previous = now;
      list.scrollLeft += velocity * elapsed;
      velocity *= Math.pow(0.9, elapsed / 16.67);
      if (Math.abs(velocity) >= 0.02) momentumFrame = window.requestAnimationFrame(step);
      else momentumFrame = 0;
    };
    momentumFrame = window.requestAnimationFrame(step);
  };
  const finishGesture = (): void => {
    clearPressTimer();
    if (reorderTab) {
      persistTerminalTabOrder(list);
      updateTerminalTabPositions(list);
    }
    reorderTab?.classList.remove("dragging");
    list.classList.remove("reordering");
    if (scrolling) startMomentum();
    if (pressedTab && (scrolling || reorderTab)) {
      pressedTab.dataset.suppressClick = "true";
      const tab = pressedTab;
      window.setTimeout(() => { delete tab.dataset.suppressClick; }, 120);
    }
    pointerId = undefined;
    reorderTab = undefined;
    pressedTab = undefined;
    scrolling = false;
  };
  const onPointerDown = (event: PointerEvent): void => {
    if (event.pointerType === "mouse" && event.button !== 0) return;
    const target = event.target instanceof Element ? event.target : undefined;
    const tab = target?.closest<HTMLButtonElement>(".terminal-tab") || undefined;
    stopMomentum();
    clearPressTimer();
    pointerId = event.pointerId;
    pressedTab = tab;
    startX = event.clientX;
    startY = event.clientY;
    lastX = event.clientX;
    lastTime = performance.now();
    velocity = 0;
    scrolling = false;
    reorderTab = undefined;
    try { list.setPointerCapture(pointerId); } catch { /* Older WebKit can still deliver the gesture without capture. */ }
    if (tab && target?.closest(".terminal-tab-drag-handle")) {
      pressTimer = window.setTimeout(beginDrag, event.pointerType === "mouse" ? 120 : 320);
    }
  };
  const onPointerMove = (event: PointerEvent): void => {
    if (pointerId !== event.pointerId) return;
    if (!reorderTab) {
      const horizontal = Math.abs(event.clientX - startX);
      const vertical = Math.abs(event.clientY - startY);
      if (!scrolling && (horizontal > 6 || vertical > 6)) {
        clearPressTimer();
        if (horizontal <= vertical) {
          // This fixed page has no vertical navigation gesture on the tab
          // rail, but suppress the synthetic click after an aborted drag.
          scrolling = true;
          lastX = event.clientX;
          lastTime = performance.now();
          return;
        }
        scrolling = true;
      }
      if (!scrolling) return;
      if (event.cancelable) event.preventDefault();
      const now = performance.now();
      const elapsed = Math.max(1, now - lastTime);
      const delta = lastX - event.clientX;
      list.scrollLeft += delta;
      velocity = (velocity * 0.65) + ((delta / elapsed) * 0.35);
      lastX = event.clientX;
      lastTime = now;
      return;
    }
    if (event.cancelable) event.preventDefault();
    const listRect = list.getBoundingClientRect();
    if (event.clientX < listRect.left + 34) list.scrollLeft -= 18;
    else if (event.clientX > listRect.right - 34) list.scrollLeft += 18;

    const siblings = Array.from(list.querySelectorAll<HTMLButtonElement>(".terminal-tab:not(.dragging)"));
    let placed = false;
    for (const sibling of siblings) {
      const rect = sibling.getBoundingClientRect();
      if (event.clientX < rect.left + (rect.width / 2)) {
        list.insertBefore(reorderTab, sibling);
        placed = true;
        break;
      }
    }
    if (!placed) list.append(reorderTab);
    updateTerminalTabPositions(list);
  };

  list.addEventListener("pointerdown", onPointerDown);
  list.addEventListener("pointermove", onPointerMove);
  list.addEventListener("pointerup", finishGesture);
  list.addEventListener("pointercancel", finishGesture);
  list.addEventListener("lostpointercapture", finishGesture);
}

function installTerminalViewportSizing(page: HTMLElement): () => void {
  const viewport = window.visualViewport;
  document.documentElement.classList.add("terminal-active");
  let animationFrame = 0;
  let settleTimer = 0;

  const sync = (): void => {
    animationFrame = 0;
    if (document.visibilityState === "hidden") return;
    const height = viewport?.height ?? window.innerHeight;
    const width = viewport?.width ?? document.documentElement.clientWidth;
    // WebKit can briefly report zero while a standalone PWA is suspended.
    // Keeping the last valid layout prevents the composer from collapsing.
    if (!Number.isFinite(height) || height < 160 || !Number.isFinite(width) || width < 240) return;
    const offsetTop = Math.max(0, viewport?.offsetTop ?? 0);
    const offsetLeft = Math.max(0, viewport?.offsetLeft ?? 0);
    page.style.setProperty("--terminal-viewport-height", `${height}px`);
    page.style.setProperty("--terminal-viewport-top", `${offsetTop}px`);
    page.style.setProperty("--terminal-viewport-width", `${width}px`);
    page.style.setProperty("--terminal-viewport-left", `${offsetLeft}px`);
    fitTerminal();
  };
  const schedule = (): void => {
    if (!animationFrame) animationFrame = window.requestAnimationFrame(sync);
  };
  const settle = (): void => {
    schedule();
    if (settleTimer) window.clearTimeout(settleTimer);
    // iOS animates its keyboard after focus has moved. Recheck once that
    // animation settles in case the last VisualViewport event was skipped.
    settleTimer = window.setTimeout(schedule, 350);
  };
  const onVisibilityChange = (): void => {
    if (document.visibilityState === "visible") settle();
  };

  window.addEventListener("resize", schedule, { passive: true });
  viewport?.addEventListener("resize", schedule, { passive: true });
  viewport?.addEventListener("scroll", schedule, { passive: true });
  page.addEventListener("focusin", settle);
  page.addEventListener("focusout", settle);
  document.addEventListener("visibilitychange", onVisibilityChange);
  sync();

  return () => {
    if (animationFrame) window.cancelAnimationFrame(animationFrame);
    if (settleTimer) window.clearTimeout(settleTimer);
    window.removeEventListener("resize", schedule);
    viewport?.removeEventListener("resize", schedule);
    viewport?.removeEventListener("scroll", schedule);
    page.removeEventListener("focusin", settle);
    page.removeEventListener("focusout", settle);
    document.removeEventListener("visibilitychange", onVisibilityChange);
    document.documentElement.classList.remove("terminal-active");
  };
}

function enableTouchScroll(
  terminal: Terminal,
  tuiHint?: HTMLElement,
  isDirectMode: () => boolean = () => false,
): { cleanup: () => void; align: () => void; refresh: () => void } {
  const root = terminal.element;
  const screen = root?.querySelector<HTMLElement>(".xterm-screen");
  const hasTouchInput = navigator.maxTouchPoints > 0 || window.matchMedia("(pointer: coarse)").matches;
  if (!root || !screen || !hasTouchInput) {
    return { cleanup: () => undefined, align: () => undefined, refresh: () => undefined };
  }

  const spacer = el("div", "terminal-native-scroll-spacer");
  spacer.setAttribute("aria-hidden", "true");
  root.append(spacer);
  root.classList.add("native-touch-terminal");
  terminal.options.cursorBlink = false;
  let syncFrame = 0;
  let disposed = false;
  let ignoreProgrammaticScroll = false;
  let touchIdentifier: number | undefined;
  let startingTouchY = 0;
  let touchStartedAt = 0;
  let touchMoved = false;
  let touchRemainder = 0;
  let tuiScrollAnchor = 0;
  let tuiRecenterTimer = 0;

  // Full-screen applications own their history, so a retained xterm buffer
  // cannot provide normal browser scrolling. Give WebKit/Chromium a large,
  // centered native scroll surface and translate its inertial movement into
  // bounded terminal wheel reports while the rendered screen stays pinned.
  const TUI_SCROLL_RANGE = 100_000;

  const resetTouch = (): void => {
    touchIdentifier = undefined;
    startingTouchY = 0;
    touchStartedAt = 0;
    touchMoved = false;
    touchRemainder = 0;
  };
  const clearTUIRecenter = (): void => {
    if (tuiRecenterTimer) window.clearTimeout(tuiRecenterTimer);
    tuiRecenterTimer = 0;
  };
  const isAlternateScreen = (): boolean => terminal.buffer.active.type === "alternate";
  const findTouch = (touches: TouchList, identifier: number): Touch | undefined => {
    for (let index = 0; index < touches.length; index += 1) {
      const touch = touches.item(index);
      if (touch?.identifier === identifier) return touch;
    }
    return undefined;
  };
  const syncInputMode = (): void => {
    const alternate = isAlternateScreen();
    root.classList.toggle("tui-touch-terminal", alternate);
    root.classList.toggle("direct-touch-terminal", isDirectMode());
    if (tuiHint) tuiHint.hidden = !alternate || isDirectMode();
    resetTouch();
    if (alternate) {
      clearTUIRecenter();
      spacer.style.height = `${TUI_SCROLL_RANGE + root.clientHeight}px`;
      ignoreProgrammaticScroll = true;
      tuiScrollAnchor = TUI_SCROLL_RANGE / 2;
      root.scrollTop = tuiScrollAnchor;
    } else {
      syncNativeScroller(true);
    }
  };

  const rowHeight = (): number => screen.clientHeight / terminal.rows;
  const syncTUIScroller = (recenter = false): void => {
    spacer.style.height = `${TUI_SCROLL_RANGE + root.clientHeight}px`;
    if (!recenter) return;
    clearTUIRecenter();
    ignoreProgrammaticScroll = true;
    touchRemainder = 0;
    tuiScrollAnchor = TUI_SCROLL_RANGE / 2;
    root.scrollTop = tuiScrollAnchor;
  };
  const syncNativeScroller = (force = false): void => {
    if (disposed) return;
    const height = rowHeight();
    if (!Number.isFinite(height) || height <= 0) return;
    const buffer = terminal.buffer.active;
    spacer.style.height = `${Math.max(0, buffer.length - terminal.rows) * height}px`;
    const target = buffer.viewportY * height;
    // A nearby difference means this scroll originated from the finger. Do
    // not snap WebKit's fractional momentum position back to a terminal row.
    if (force || Math.abs(root.scrollTop - target) > height * 1.5) {
      ignoreProgrammaticScroll = true;
      root.scrollTop = target;
    }
  };
  const scheduleSync = (): void => {
    if (!syncFrame) {
      syncFrame = window.requestAnimationFrame(() => {
        syncFrame = 0;
        if (isAlternateScreen()) syncTUIScroller();
        else syncNativeScroller();
      });
    }
  };
  const onNativeScroll = (): void => {
    if (ignoreProgrammaticScroll) return;
    const selection = window.getSelection();
    if (selection && !selection.isCollapsed && selection.anchorNode && root.contains(selection.anchorNode)) return;
    const height = rowHeight();
    if (!Number.isFinite(height) || height <= 0) return;
    if (isAlternateScreen()) {
      const current = root.scrollTop;
      const consumed = consumeTouchWheel(current, tuiScrollAnchor, touchRemainder, Math.max(12, Math.min(28, height * 1.1)));
      tuiScrollAnchor = current;
      touchRemainder = consumed.remainder;
      const bounds = screen.getBoundingClientRect();
      const clientX = bounds.left + (bounds.width / 2);
      const clientY = bounds.top + (bounds.height / 2);
      for (const direction of consumed.directions) {
        root.dispatchEvent(new WheelEvent("wheel", {
          bubbles: true,
          cancelable: true,
          clientX,
          clientY,
          deltaMode: WheelEvent.DOM_DELTA_LINE,
          deltaY: direction,
        }));
      }
      clearTUIRecenter();
      tuiRecenterTimer = window.setTimeout(() => {
        tuiRecenterTimer = 0;
        if (!isAlternateScreen()) return;
        ignoreProgrammaticScroll = true;
        touchRemainder = 0;
        tuiScrollAnchor = TUI_SCROLL_RANGE / 2;
        root.scrollTop = tuiScrollAnchor;
      }, 180);
      return;
    }
    const buffer = terminal.buffer.active;
    const line = Math.max(0, Math.min(buffer.length - terminal.rows, Math.round(root.scrollTop / height)));
    if (line !== buffer.viewportY) terminal.scrollToLine(line);
  };
  const onTouchStart = (): void => {
    // From this point, scroll events belong to the user's finger rather than
    // a resize or xterm-driven position correction.
    ignoreProgrammaticScroll = false;
    clearTUIRecenter();
    if (isAlternateScreen()) {
      tuiScrollAnchor = root.scrollTop;
      touchRemainder = 0;
    }
  };

  const onTUITouchStart = (event: TouchEvent): void => {
    if ((!isAlternateScreen() && !isDirectMode()) || event.touches.length !== 1) return;
    const touch = event.touches[0];
    if (!touch) return;
    touchIdentifier = touch.identifier;
    startingTouchY = touch.clientY;
    touchStartedAt = performance.now();
    touchMoved = false;
    touchRemainder = 0;
  };
  const onTUITouchMove = (event: TouchEvent): void => {
    if (touchIdentifier === undefined) return;
    const touch = findTouch(event.touches, touchIdentifier);
    if (!touch) return;
    if (Math.abs(touch.clientY - startingTouchY) > 7) touchMoved = true;
  };
  const onTUITouchEnd = (event: TouchEvent): void => {
    if (touchIdentifier === undefined) return;
    if (findTouch(event.touches, touchIdentifier)) return;
    const focusDirectInput = isDirectMode() && !touchMoved && performance.now() - touchStartedAt < 500;
    resetTouch();
    if (focusDirectInput) terminal.focus();
  };

  root.addEventListener("scroll", onNativeScroll, { passive: true });
  root.addEventListener("touchstart", onTouchStart, { passive: true });
  root.addEventListener("touchstart", onTUITouchStart, { passive: true });
  root.addEventListener("touchmove", onTUITouchMove, { passive: false });
  root.addEventListener("touchend", onTUITouchEnd, { passive: true });
  root.addEventListener("touchcancel", onTUITouchEnd, { passive: true });
  const renderSubscription = terminal.onRender(scheduleSync);
  const scrollSubscription = terminal.onScroll(scheduleSync);
  const resizeSubscription = terminal.onResize(scheduleSync);
  const bufferSubscription = terminal.buffer.onBufferChange(syncInputMode);
  syncInputMode();
  scheduleSync();
  return {
    refresh: syncInputMode,
    align: () => {
      // Changing terminal rows can make WebKit emit delayed scroll events for
      // the old geometry. Ignore them until the next real finger gesture.
      ignoreProgrammaticScroll = true;
      if (syncFrame) window.cancelAnimationFrame(syncFrame);
      syncFrame = 0;
      if (isAlternateScreen()) syncTUIScroller(true);
      else syncNativeScroller(true);
    },
    cleanup: () => {
      disposed = true;
      if (syncFrame) window.cancelAnimationFrame(syncFrame);
      clearTUIRecenter();
      renderSubscription.dispose();
      scrollSubscription.dispose();
      resizeSubscription.dispose();
      bufferSubscription.dispose();
      root.removeEventListener("scroll", onNativeScroll);
      root.removeEventListener("touchstart", onTouchStart);
      root.removeEventListener("touchstart", onTUITouchStart);
      root.removeEventListener("touchmove", onTUITouchMove);
      root.removeEventListener("touchend", onTUITouchEnd);
      root.removeEventListener("touchcancel", onTUITouchEnd);
      root.classList.remove("native-touch-terminal");
      root.classList.remove("tui-touch-terminal");
      if (tuiHint) tuiHint.hidden = true;
      spacer.remove();
    },
  };
}

function renderTerminalComposer(): HTMLElement {
  const section = el("section", "terminal-composer");
  section.dataset.connected = "false";
  const attachmentList = el("div", "terminal-attachment-list");
  attachmentList.hidden = true;
  const input = el("textarea", "terminal-composer-input");
  input.rows = 1;
  input.wrap = "soft";
  input.placeholder = "Ask or type a command…";
  input.spellcheck = false;
  input.autocapitalize = "off";
  input.autocomplete = "off";
  input.setAttribute("enterkeyhint", "send");
  input.setAttribute("aria-label", "Terminal command or message");

  const send = el("button", "terminal-composer-send", "↑");
  send.type = "submit";
  send.disabled = true;
  send.setAttribute("aria-label", "Send to terminal");
  const attach = el("button", "terminal-attach-button", "+");
  attach.type = "button";
  attach.disabled = true;
  attach.setAttribute("aria-label", "Attach an image, screenshot, or PDF");
  attach.title = "Attach image or file";
  const form = el("form", "terminal-composer-form");
  form.append(attach, input, send);
  const panel = el("div", "terminal-composer-panel");
  panel.append(attachmentList, form);
  const status = el("div", "terminal-composer-meta");
  status.append(
    el("span", "terminal-composer-hint", "Enter to send · Shift+Enter for a new line"),
    el("span", "terminal-direct-hint", "Tap terminal to type · swipe to navigate"),
    el("span", "terminal-composer-state", "Connecting…"),
  );

  const syncSend = (): void => {
    send.disabled = section.dataset.connected !== "true" || input.value.length === 0;
  };
  const addPathToComposer = (path: string): void => {
    const quoted = `'${path.replaceAll("'", `'\\''`)}'`;
    const start = input.selectionStart ?? input.value.length;
    const end = input.selectionEnd ?? start;
    const prefix = start > 0 && !/\s$/.test(input.value.slice(0, start)) ? " " : "";
    const suffix = end < input.value.length && !/^\s/.test(input.value.slice(end)) ? " " : "";
    input.setRangeText(`${prefix}${quoted}${suffix}`, start, end, "end");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  };
  const showAttachment = (file: File, path: string): void => {
    const chip = el("div", "terminal-attachment-chip");
    chip.title = path;
    chip.append(
      el("span", "terminal-attachment-icon", file.type.startsWith("image/") ? "▧" : "▤"),
      el("span", "terminal-attachment-name", file.name),
      el("span", "terminal-attachment-ready", "✓"),
    );
    attachmentList.append(chip);
    attachmentList.hidden = false;
  };
  const chooseAttachments = (): void => {
    const statusText = status.querySelector<HTMLElement>(".terminal-composer-state");
    if (!encryptedBridge && !portalResumeKey) {
      if (statusText) statusText.textContent = "Encrypted upload unavailable";
      return;
    }
    const picker = document.createElement("input");
    picker.type = "file";
    picker.accept = "image/*,application/pdf";
    picker.multiple = true;
    picker.hidden = true;
    picker.addEventListener("change", async () => {
      const files = Array.from(picker.files || []);
      picker.remove();
      if (files.length === 0) return;
      attach.dataset.uploading = "true";
      attach.disabled = true;
      try {
        for (const file of files) {
          if (statusText) statusText.textContent = `Uploading ${file.name}…`;
          const bridge = await readyUploadBridge();
          const path = await bridge.uploadFile(file, (received, total) => {
            const percent = total === 0 ? 100 : Math.round((received / total) * 100);
            if (statusText) statusText.textContent = `Uploading ${file.name} · ${percent}%`;
          });
          showAttachment(file, path);
          addPathToComposer(path);
        }
        if (statusText) statusText.textContent = "Attached · saved on computer";
        input.focus({ preventScroll: true });
      } catch (caught) {
        if (statusText) statusText.textContent = caught instanceof Error ? caught.message : "Upload failed";
      } finally {
        delete attach.dataset.uploading;
        attach.disabled = section.dataset.connected !== "true" || !encryptedBridge;
      }
    }, { once: true });
    picker.addEventListener("cancel", () => picker.remove(), { once: true });
    document.body.append(picker);
    picker.click();
  };
  const submit = (): void => {
    if (!input.value || section.dataset.connected !== "true") return;
    const value = input.value;
    // Send the composer value and Enter as one ordered PTY message. Calling
    // xterm.paste() and then sending Enter separately can race a just-resumed
    // encrypted link, clearing the composer after only one half was delivered.
    const normalized = value.replace(/\r?\n/g, "\r");
    const pasted = state.terminal?.modes.bracketedPasteMode
      ? `\u001b[200~${normalized}\u001b[201~`
      : normalized;
    if (!sendTerminalInput(`${pasted}\r`)) {
      const statusText = status.querySelector<HTMLElement>(".terminal-composer-state");
      if (statusText) statusText.textContent = "Not sent · reconnecting";
      const activeSession = state.sessions.find((session) => session.id === state.selected);
      if (activeSession) scheduleTerminalReconnect(activeSession);
      return;
    }
    input.value = "";
    attachmentList.replaceChildren();
    attachmentList.hidden = true;
    syncSend();
    // Keep focus after submit. On iOS, blurring here closes the keyboard and
    // resizes the visual viewport during xterm's redraw, which can leave the
    // terminal canvas black even though the PTY is still connected.
  };
  input.addEventListener("input", () => {
    syncSend();
  });
  input.addEventListener("keydown", (event) => {
    if (event.key !== "Enter" || event.shiftKey || event.isComposing) return;
    event.preventDefault();
    submit();
  });
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    submit();
  });
  attach.addEventListener("pointerdown", (event) => event.preventDefault());
  attach.addEventListener("click", chooseAttachments);

  section.append(renderExtraKeys(input), panel, status);
  return section;
}

function renderExtraKeys(focusTarget?: HTMLElement): HTMLElement {
  const bar = el("div", "extra-keys");
  const keys: Array<[string, string, string?]> = [
    ["\r", "Enter"], ["\u001b", "Esc"], ["\t", "Tab"], ["\u0003", "Ctrl C"], ["\u0004", "Ctrl D"],
    ["\u001b[5~", "PgUp", "terminal-page-navigation-key"], ["\u001b[6~", "PgDn", "terminal-page-navigation-key"],
    ["\u001b[A", "↑"], ["\u001b[B", "↓"], ["\u001b[D", "←"], ["\u001b[C", "→"],
  ];
  for (const [value, label, className] of keys) {
    const button = el("button", "key-button", label);
    button.type = "button";
    button.classList.add("terminal-control-key");
    if (className) button.classList.add(className);
    button.disabled = true;
    button.addEventListener("pointerdown", (event) => event.preventDefault());
    button.addEventListener("click", () => {
      sendTerminalInput(value);
      focusTarget?.focus();
    });
    bar.append(button);
  }
  return bar;
}

function sendTerminalInput(value: string): boolean {
  if (state.socket?.readyState !== WebSocket.OPEN) return false;
  state.socket.send(new TextEncoder().encode(value));
  return true;
}

async function copyVisibleTerminalOutput(): Promise<void> {
  const terminal = state.terminal;
  if (!terminal) return;
  const copied = await copyToDeviceClipboard(terminalVisibleText(terminal));
  const status = document.querySelector<HTMLElement>(".terminal-composer-state");
  if (!status) return;
  const message = copied ? "Screen copied" : "Hold terminal text to select";
  status.textContent = message;
  window.setTimeout(() => {
    if (status.textContent === message) status.textContent = state.socket?.readyState === WebSocket.OPEN ? "Ready" : "Input unavailable";
  }, 1800);
}

function terminalBufferText(terminal: Terminal, first = 0, last = terminal.buffer.active.length): string {
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

function terminalVisibleText(terminal: Terminal): string {
  const buffer = terminal.buffer.active;
  return terminalBufferText(terminal, buffer.viewportY, buffer.viewportY + terminal.rows);
}

async function copyToDeviceClipboard(text: string, fallback?: HTMLTextAreaElement): Promise<boolean> {
  if (!text) return false;
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const helper = document.createElement("textarea");
    helper.value = text;
    helper.readOnly = true;
    helper.style.position = "fixed";
    helper.style.opacity = "0";
    helper.style.pointerEvents = "none";
    document.body.append(helper);
    helper.select();
    try {
      const copied = document.execCommand("copy");
      helper.remove();
      fallback?.focus();
      return copied;
    } catch {
      helper.remove();
      fallback?.focus();
      return false;
    }
  }
}

function connectTerminal(session: Session, automatic = false): void {
  if (state.terminalReconnectTimer !== undefined) window.clearTimeout(state.terminalReconnectTimer);
  state.terminalReconnectTimer = undefined;
  if (!automatic) state.terminalReconnectAttempts = 0;
  state.socket?.close();
  if (state.resizeTimer !== undefined) window.clearTimeout(state.resizeTimer);
  state.resizeTimer = undefined;
  state.lastResize = undefined;
  const preserveExisting = state.terminalSessionID === session.id && state.terminalSnapshotApplied;
  const stream = new TerminalStreamReconciler();
  let readyTimer: number | undefined;
  let ended = !session.running;
  const clearReadyTimer = (): void => {
    if (readyTimer !== undefined) window.clearTimeout(readyTimer);
    readyTimer = undefined;
  };
  const markReady = (): void => {
    if (state.socket !== socket) return;
    clearReadyTimer();
    state.terminalSnapshotApplied = true;
    const prefix = encryptedPortal ? "E2E · " : "";
    if (!ended) setConnectionState(`${prefix}Live · input enabled`, "online");
    fitTerminal();
  };
  const applySnapshot = (snapshot: Uint8Array): void => {
    if (state.socket !== socket || !state.terminal) return;
    const terminal = state.terminal;
    const replyGate = state.terminalReplyGate;
    const replyGeneration = replyGate?.beginSnapshot();
    const buffer = terminal.buffer.active;
    const wasAtBottom = buffer.viewportY >= buffer.baseY;
    const distanceFromBottom = Math.max(0, buffer.baseY - buffer.viewportY);
    terminal.reset();
    const applied = (): void => {
      if (state.socket !== socket || state.terminal !== terminal) return;
      if (wasAtBottom) terminal.scrollToBottom();
      else terminal.scrollToLine(Math.max(0, terminal.buffer.active.baseY - distanceFromBottom));
      state.touchSync?.();
      if (replyGeneration !== undefined) {
        const reply = replyGate?.finishSnapshot(replyGeneration);
        if (reply && socket.readyState === WebSocket.OPEN) socket.send(reply);
      }
      markReady();
    };
    if (snapshot.byteLength === 0) applied();
    else terminal.write(snapshot, applied);
  };
  setConnectionState(preserveExisting ? "Reconnecting · terminal kept visible…" : "Connecting…", "connecting");
  const opened = (): void => {
    if (state.socket !== socket) return;
    state.terminalReconnectAttempts = 0;
    setConnectionState(preserveExisting ? "Connected · syncing terminal…" : "Connected · loading terminal…", "connecting");
    fitTerminal();
    // Older daemons do not frame an empty initial snapshot. Keep treating the
    // first eventual binary frame as their snapshot, but avoid blocking input
    // forever when a brand-new quiet shell has no output yet.
    readyTimer = window.setTimeout(() => {
      if (state.socket === socket && stream.waitingForSnapshot && !stream.framedSnapshotStarted) markReady();
    }, 400);
  };
  const received = async (data: string | ArrayBuffer | Blob): Promise<void> => {
    if (state.socket !== socket) return;
    if (typeof data === "string") {
      try {
        const message: unknown = JSON.parse(data);
        const control = terminalStreamControl(message);
        if (control) {
          if (control.type === "terminal_snapshot_start") clearReadyTimer();
          const action = stream.receiveControl(control);
          if (action?.kind === "snapshot") applySnapshot(action.data);
          return;
        }
        if (isRecord(message) && message.type === "status" && message.running === false) {
          ended = true;
          session.running = false;
          session.exitCode = typeof message.exitCode === "number" ? message.exitCode : undefined;
          session.signal = typeof message.signal === "string" && message.signal ? message.signal : undefined;
          void loadTerminalHistory().catch(() => undefined);
          setConnectionState(describeSessionExit(session), "offline");
        }
      } catch {
        if (state.socket === socket) {
          socket.close();
          scheduleTerminalReconnect(session);
        }
      }
      return;
    }
    const bytes = data instanceof Blob ? await data.arrayBuffer() : data;
    if (state.socket !== socket) return;
    try {
      const action = stream.receiveBinary(new Uint8Array(bytes));
      if (action?.kind === "snapshot") applySnapshot(action.data);
      else if (action?.kind === "live") state.terminal?.write(action.data);
    } catch {
      socket.close();
      scheduleTerminalReconnect(session);
    }
  };
  const closed = (code: number): void => {
    if (state.socket !== socket) return;
    clearReadyTimer();
    if (code === 1008) {
      state.authenticated = false;
      renderLogin("Your portal session expired");
      return;
    }
    if (session.running) scheduleTerminalReconnect(session);
  };
  const failed = (): void => {
    if (state.socket === socket) {
      clearReadyTimer();
      scheduleTerminalReconnect(session);
    }
  };

  let socket: TerminalLink;
  if (encryptedPortal) {
    if (!encryptedBridge) {
      setConnectionState("Connection paused · reconnecting…", "connecting");
      if (portalResumeKey) void resumeEncryptedPortal();
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

function scheduleTerminalReconnect(session: Session): void {
  if (!session.running || state.selected !== session.id || !document.querySelector(".terminal-page")) return;
  if (state.terminalReconnectTimer !== undefined) return;
  const delay = Math.min(500 * (2 ** Math.min(state.terminalReconnectAttempts, 3)), 4_000);
  state.terminalReconnectAttempts += 1;
  setConnectionState("Disconnected · reconnecting…", "connecting");
  state.terminalReconnectTimer = window.setTimeout(() => {
    state.terminalReconnectTimer = undefined;
    if (!session.running || state.selected !== session.id || !document.querySelector(".terminal-page")) return;
    if (state.socket?.readyState === WebSocket.OPEN) return;
    if (encryptedPortal && (!state.authenticated || !encryptedBridge)) {
      void resumeEncryptedPortal();
      scheduleTerminalReconnect(session);
      return;
    }
    connectTerminal(session, true);
  }, delay);
}

function fitTerminal(): void {
  if (!state.fit || !state.terminal) return;
  try {
    const composerFocused = document.activeElement?.classList.contains("terminal-composer-input") ?? false;
    const wasAtBottom = composerFocused || state.terminal.buffer.active.viewportY >= state.terminal.buffer.active.baseY;
    state.fit.fit();
    if (wasAtBottom) state.terminal.scrollToBottom();
    state.touchSync?.();
    const size = `${state.terminal.cols}x${state.terminal.rows}`;
    if (state.socket?.readyState === WebSocket.OPEN && size !== state.lastResize) {
      if (state.resizeTimer !== undefined) window.clearTimeout(state.resizeTimer);
      const socket = state.socket;
      const cols = state.terminal.cols;
      const rows = state.terminal.rows;
      state.resizeTimer = window.setTimeout(() => {
        state.resizeTimer = undefined;
        if (state.socket !== socket || socket.readyState !== WebSocket.OPEN) return;
        const currentSize = `${cols}x${rows}`;
        if (currentSize === state.lastResize) return;
        socket.send(JSON.stringify({ type: "resize", cols, rows }));
        state.lastResize = currentSize;
      }, 100);
    }
  } catch { /* A resize may be queued while changing views. */ }
}

function setConnectionState(label: string, kind: "connecting" | "online" | "offline" | "warning"): void {
  const bar = document.querySelector<HTMLElement>("#connection-state");
  if (!bar) return;
  bar.className = `connection-bar ${kind}`;
  const text = bar.querySelector<HTMLElement>(".connection-label");
  if (text) text.textContent = label;

  const sync = document.querySelector<HTMLElement>(".terminal-sync-indicator");
  if (sync) {
    sync.hidden = kind !== "connecting";
    const syncLabel = sync.querySelector<HTMLElement>(".terminal-sync-label");
    if (syncLabel) syncLabel.textContent = label.includes("waking") ? "Computer waking…" : label.includes("loading") ? "Loading terminal…" : "Reconnecting…";
  }

  const composer = document.querySelector<HTMLElement>(".terminal-composer");
  if (!composer) return;
  const connected = kind === "online";
  composer.dataset.connected = String(connected);
  const input = composer.querySelector<HTMLTextAreaElement>(".terminal-composer-input");
  const send = composer.querySelector<HTMLButtonElement>(".terminal-composer-send");
  const attach = composer.querySelector<HTMLButtonElement>(".terminal-attach-button");
  const composerState = composer.querySelector<HTMLElement>(".terminal-composer-state");
  if (send) send.disabled = !connected || !input?.value.length;
  if (attach) attach.disabled = !connected || !encryptedBridge || attach.dataset.uploading === "true";
  for (const button of composer.querySelectorAll<HTMLButtonElement>(".terminal-control-key")) button.disabled = !connected;
  if (composerState) {
    composerState.textContent = connected ? "Ready" : kind === "connecting" ? "Connecting…" : "Input unavailable";
  }
}

function closeConnection(): void {
  if (state.desktop) {
    try { state.desktop.disconnect(); } catch { /* The stream may already be closed. */ }
  }
  state.desktop = undefined;
  state.desktopLink?.close();
  state.desktopLink = undefined;
  state.windowLink?.close();
  state.windowLink = undefined;
  if (state.socket) {
    state.socket.close();
  }
  state.socket = undefined;
  if (state.resizeTimer !== undefined) window.clearTimeout(state.resizeTimer);
  state.resizeTimer = undefined;
  state.lastResize = undefined;
  if (state.terminalReconnectTimer !== undefined) window.clearTimeout(state.terminalReconnectTimer);
  state.terminalReconnectTimer = undefined;
  state.terminalReconnectAttempts = 0;
  state.touchCleanup?.();
  state.touchCleanup = undefined;
  state.touchSync = undefined;
  state.layoutCleanup?.();
  state.layoutCleanup = undefined;
  state.terminal?.dispose();
  state.terminal = undefined;
  state.terminalSessionID = undefined;
  state.terminalSnapshotApplied = false;
  state.terminalReplyGate?.reset();
  state.terminalReplyGate = undefined;
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

function savedAgeLabel(saved: SavedTerminal): string {
  const time = savedActivityTime(saved);
  if (!time) return "saved";
  const seconds = Math.max(0, Math.floor((Date.now() - time) / 1000));
  if (seconds < 60) return "just now";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days}d ago`;
  return new Date(time).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

if ("serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    void navigator.serviceWorker.register("/sw.js", { scope: "/", updateViaCache: "none" });
  }, { once: true });
}

document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible") void resumeEncryptedPortal();
});
window.addEventListener("pageshow", () => { void resumeEncryptedPortal(); });
window.addEventListener("online", () => { void resumeEncryptedPortal(); });

void boot();
