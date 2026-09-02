import { DurableObject } from "cloudflare:workers";

const MAX_ENCRYPTED_PACKET = 5 * 1024 * 1024;
const MAX_BROWSER_SOCKETS = 8;

type SocketAttachment =
  | { role: "connector" }
  | { role: "browser"; channel: string };

type ConnectorToBrowser = {
  type: "e2e_to_browser";
  id: string;
  data: string;
};

type ChannelClose = {
  type: "channel_close";
  id: string;
  code?: number;
  reason?: string;
};

export class TermlinksRelay extends DurableObject<Env> {
  async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url);
    if (url.pathname === "/connector") return this.acceptConnector(request);
    if (url.pathname === "/status" && request.method === "GET") {
      return Response.json({ online: this.connector() !== undefined }, { headers: securityHeaders() });
    }
    if (url.pathname === "/ws/bridge" && request.method === "GET") return this.acceptBrowser(request);
    return jsonError(404, "Not found");
  }

  async webSocketMessage(socket: WebSocket, message: string | ArrayBuffer): Promise<void> {
    const attachment = socketAttachment(socket);
    if (!attachment) {
      socket.close(1008, "Invalid socket state");
      return;
    }
    if (attachment.role === "browser") {
      this.forwardCiphertext(attachment.channel, message);
      return;
    }
    if (typeof message !== "string" || message.length > MAX_ENCRYPTED_PACKET * 2) {
      socket.close(1009, "Connector message too large");
      return;
    }
    let decoded: unknown;
    try {
      decoded = JSON.parse(message);
    } catch {
      socket.close(1007, "Invalid connector message");
      return;
    }
    this.handleConnectorMessage(socket, decoded);
  }

  async webSocketClose(socket: WebSocket, code: number, reason: string): Promise<void> {
    const attachment = socketAttachment(socket);
    if (attachment?.role === "connector") {
      const current = this.connector();
      if (current && current !== socket) return;
      this.disconnectBrowsers(code === 1000 ? 1012 : code, reason || "Computer disconnected");
      return;
    }
    if (attachment?.role === "browser") {
      this.sendToConnector({
        type: "channel_close",
        id: attachment.channel,
        code: normalizeCloseCode(code),
        reason: boundedReason(reason),
      });
    }
  }

  async webSocketError(socket: WebSocket): Promise<void> {
    const attachment = socketAttachment(socket);
    if (attachment?.role === "connector") {
      const current = this.connector();
      if (current && current !== socket) return;
      this.disconnectBrowsers(1012, "Computer connection failed");
    } else if (attachment?.role === "browser") {
      this.sendToConnector({ type: "channel_close", id: attachment.channel, code: 1011, reason: "Browser connection failed" });
    }
  }

  private acceptConnector(request: Request): Response {
    if (request.headers.get("Upgrade")?.toLowerCase() !== "websocket" || request.headers.get("X-Termlinks-Connector") !== "authorized") {
      return jsonError(400, "Invalid connector request");
    }
    for (const existing of this.ctx.getWebSockets("connector")) existing.close(1012, "Replaced by a new connector");
    this.disconnectBrowsers(1012, "Computer reconnected");

    const pair = new WebSocketPair();
    const browserSide = pair[0];
    const relaySide = pair[1];
    relaySide.serializeAttachment({ role: "connector" } satisfies SocketAttachment);
    this.ctx.acceptWebSocket(relaySide, ["connector"]);
    relaySide.send(JSON.stringify({ type: "connected", protocol: "e2e-v1" }));
    return new Response(null, { status: 101, webSocket: browserSide });
  }

  private acceptBrowser(request: Request): Response {
    const connector = this.connector();
    if (!connector) return jsonError(503, "Your computer is offline");
    if (request.headers.get("Upgrade")?.toLowerCase() !== "websocket") return jsonError(426, "WebSocket upgrade required");
    if (this.ctx.getWebSockets("browser").length >= MAX_BROWSER_SOCKETS) return jsonError(503, "Too many portal connections");

    const channel = crypto.randomUUID();
    const pair = new WebSocketPair();
    const browserSide = pair[0];
    const relaySide = pair[1];
    relaySide.serializeAttachment({ role: "browser", channel } satisfies SocketAttachment);
    this.ctx.acceptWebSocket(relaySide, ["browser", `channel:${channel}`]);
    try {
      connector.send(JSON.stringify({ type: "channel_open", id: channel }));
      relaySide.send(JSON.stringify({ type: "bridge_ready", id: channel, protocol: "e2e-v1" }));
    } catch {
      relaySide.close(1012, "Your computer is offline");
    }
    return new Response(null, { status: 101, webSocket: browserSide });
  }

  private forwardCiphertext(channel: string, message: string | ArrayBuffer): void {
    if (typeof message !== "string" || message.length === 0 || message.length > MAX_ENCRYPTED_PACKET || !isBase64URL(message)) {
      this.closeBrowser(channel, 1007, "Invalid encrypted packet");
      return;
    }
    if (!this.sendToConnector({ type: "e2e_from_browser", id: channel, data: message })) {
      this.closeBrowser(channel, 1012, "Your computer is offline");
    }
  }

  private handleConnectorMessage(socket: WebSocket, value: unknown): void {
    if (!isRecord(value) || typeof value.type !== "string") {
      socket.close(1007, "Invalid connector message");
      return;
    }
    if (value.type === "e2e_to_browser") {
      const message = parseConnectorToBrowser(value);
      if (!message) {
        socket.close(1007, "Invalid encrypted browser message");
        return;
      }
      const browser = this.browser(message.id);
      if (!browser) return;
      try {
        browser.send(message.data);
      } catch {
        browser.close(1011, "Encrypted forwarding failed");
      }
      return;
    }
    if (value.type === "channel_close") {
      const message = parseChannelClose(value);
      if (!message) {
        socket.close(1007, "Invalid channel close");
        return;
      }
      this.closeBrowser(message.id, normalizeCloseCode(message.code ?? 1000), boundedReason(message.reason ?? ""));
      return;
    }
    if (value.type === "ping") {
      socket.send(JSON.stringify({ type: "pong" }));
      return;
    }
    if (value.type !== "hello") socket.close(1007, "Unsupported connector message");
  }

  private connector(): WebSocket | undefined {
    return this.ctx.getWebSockets("connector").find((socket) => socket.readyState === WebSocket.OPEN);
  }

  private browser(channel: string): WebSocket | undefined {
    return this.ctx.getWebSockets(`channel:${channel}`).find((socket) => socket.readyState === WebSocket.OPEN);
  }

  private sendToConnector(message: { type: "e2e_from_browser"; id: string; data: string } | ChannelClose): boolean {
    const connector = this.connector();
    if (!connector) return false;
    try {
      connector.send(JSON.stringify(message));
      return true;
    } catch {
      return false;
    }
  }

  private closeBrowser(channel: string, code: number, reason: string): void {
    const socket = this.browser(channel);
    if (socket) socket.close(code, reason);
  }

  private disconnectBrowsers(code: number, reason: string): void {
    for (const socket of this.ctx.getWebSockets("browser")) socket.close(normalizeCloseCode(code), boundedReason(reason));
  }
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);
    try {
      const headers = new Headers(request.headers);
      headers.delete("Authorization");
      if (url.pathname === "/connector") {
        if (typeof env.CONNECTOR_TOKEN !== "string" || env.CONNECTOR_TOKEN.length < 32) {
          return jsonError(503, "Connector authentication is not configured");
        }
        const authorization = request.headers.get("Authorization") ?? "";
        const supplied = authorization.startsWith("Bearer ") ? authorization.slice(7) : "";
        if (!(await constantTimeEqual(supplied, env.CONNECTOR_TOKEN))) return jsonError(401, "Unauthorized");
        headers.set("X-Termlinks-Connector", "authorized");
      } else {
        headers.delete("X-Termlinks-Connector");
      }
      return env.RELAY.getByName("personal-computer", { locationHint: "apac" }).fetch(new Request(request, { headers }));
    } catch (error) {
      console.error(JSON.stringify({
        message: "relay request failed",
        path: url.pathname,
        error: error instanceof Error ? error.message : String(error),
      }));
      return jsonError(500, "Relay request failed");
    }
  },
} satisfies ExportedHandler<Env>;

function parseConnectorToBrowser(value: Record<string, unknown>): ConnectorToBrowser | undefined {
  if (typeof value.id !== "string" || !isUUID(value.id) || typeof value.data !== "string" || value.data.length === 0 || value.data.length > MAX_ENCRYPTED_PACKET || !isBase64URL(value.data)) return undefined;
  return { type: "e2e_to_browser", id: value.id, data: value.data };
}

function parseChannelClose(value: Record<string, unknown>): ChannelClose | undefined {
  if (typeof value.id !== "string" || !isUUID(value.id)) return undefined;
  if (value.code !== undefined && (!Number.isInteger(value.code) || Number(value.code) < 1000 || Number(value.code) > 4999)) return undefined;
  if (value.reason !== undefined && typeof value.reason !== "string") return undefined;
  return { type: "channel_close", id: value.id, code: value.code === undefined ? undefined : Number(value.code), reason: value.reason };
}

function socketAttachment(socket: WebSocket): SocketAttachment | undefined {
  const value: unknown = socket.deserializeAttachment();
  if (!isRecord(value) || (value.role !== "connector" && value.role !== "browser")) return undefined;
  if (value.role === "browser") {
    if (typeof value.channel !== "string" || !isUUID(value.channel)) return undefined;
    return { role: "browser", channel: value.channel };
  }
  return { role: "connector" };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isUUID(value: string): boolean {
  return /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/.test(value);
}

function isBase64URL(value: string): boolean {
  return /^[A-Za-z0-9_-]+$/.test(value);
}

async function constantTimeEqual(provided: string, expected: string): Promise<boolean> {
  const encoder = new TextEncoder();
  const [providedHash, expectedHash] = await Promise.all([
    crypto.subtle.digest("SHA-256", encoder.encode(provided)),
    crypto.subtle.digest("SHA-256", encoder.encode(expected)),
  ]);
  return crypto.subtle.timingSafeEqual(providedHash, expectedHash);
}

function normalizeCloseCode(code: number): number {
  return code >= 1000 && code <= 4999 && ![1004, 1005, 1006, 1015].includes(code) ? code : 1012;
}

function boundedReason(reason: string): string {
  const bytes = new TextEncoder().encode(reason);
  if (bytes.byteLength <= 120) return reason;
  return new TextDecoder().decode(bytes.slice(0, 120));
}

function securityHeaders(): Record<string, string> {
  return {
    "Cache-Control": "no-store",
    "Referrer-Policy": "no-referrer",
    "X-Content-Type-Options": "nosniff",
    "X-Frame-Options": "DENY",
  };
}

function jsonError(status: number, message: string): Response {
  return new Response(JSON.stringify({ error: message }), {
    status,
    headers: { ...securityHeaders(), "Content-Type": "application/json; charset=utf-8" },
  });
}
