import { build } from "esbuild";
import { resolve } from "node:path";

const portal = (process.env.TERMLINKS_E2E_PORTAL || "").replace(/\/$/, "");
const token = process.env.TERMLINKS_E2E_TOKEN || "";
const wantedSession = process.env.TERMLINKS_E2E_SESSION_NAME || "";
let input = process.env.TERMLINKS_E2E_SEND || "";
let expectedOutput = input;
const createShell = process.env.TERMLINKS_E2E_CREATE_SHELL === "1";
const testDesktopDisabled = process.env.TERMLINKS_E2E_DESKTOP_DISABLED === "1";
const testDesktopBridge = process.env.TERMLINKS_E2E_DESKTOP_BRIDGE === "1";
const testWindowCapture = process.env.TERMLINKS_E2E_WINDOW_CAPTURE === "1";
const wantedWindow = process.env.TERMLINKS_E2E_WINDOW_MATCH || "";
const windowText = process.env.TERMLINKS_E2E_WINDOW_TEXT || "";
const saveWindowText = process.env.TERMLINKS_E2E_WINDOW_SAVE === "1";
const testFileUpload = process.env.TERMLINKS_E2E_FILE_UPLOAD === "1";
const uploadName = process.env.TERMLINKS_E2E_UPLOAD_NAME || `termlinks-e2e-${Date.now()}.txt`;

if (!portal.startsWith("https://") || token.length < 32) {
  throw new Error("Set TERMLINKS_E2E_PORTAL and TERMLINKS_E2E_TOKEN");
}

const bundled = await build({
  entryPoints: [resolve(import.meta.dirname, "../src/e2e.ts")],
  bundle: true,
  write: false,
  format: "esm",
  platform: "browser",
  target: "es2022",
});
const moduleURL = `data:text/javascript;base64,${Buffer.from(bundled.outputFiles[0].contents).toString("base64")}`;
const { deriveEncryptionKey, encryptPacket, decryptPacket, bytesToBase64URL, base64URLToBytes } = await import(moduleURL);

const websocketURL = new URL(portal);
websocketURL.protocol = "wss:";
websocketURL.pathname = "/ws/bridge";
const socket = new WebSocket(websocketURL);
const queued = [];
const waiters = [];
let terminalError;
socket.addEventListener("message", (event) => {
  const waiter = waiters.shift();
  if (waiter) waiter.resolve(event.data);
  else queued.push(event.data);
});
socket.addEventListener("close", (event) => {
  terminalError = new Error(`Encrypted bridge closed (${event.code})`);
  for (const waiter of waiters.splice(0)) waiter.reject(terminalError);
});
socket.addEventListener("error", () => {
  terminalError = new Error("Encrypted bridge failed");
  for (const waiter of waiters.splice(0)) waiter.reject(terminalError);
});

await new Promise((resolveOpen, rejectOpen) => {
  const timer = setTimeout(() => rejectOpen(new Error("Encrypted bridge open timed out")), 15_000);
  socket.addEventListener("open", () => { clearTimeout(timer); resolveOpen(); }, { once: true });
  socket.addEventListener("error", () => { clearTimeout(timer); rejectOpen(new Error("Encrypted bridge could not open")); }, { once: true });
});

async function nextMessage() {
  if (queued.length) return queued.shift();
  if (terminalError) throw terminalError;
  return new Promise((resolve, reject) => {
    let timer;
    const waiter = {
      resolve: (value) => { clearTimeout(timer); resolve(value); },
      reject: (error) => { clearTimeout(timer); reject(error); },
    };
    timer = setTimeout(() => {
      const index = waiters.indexOf(waiter);
      if (index !== -1) waiters.splice(index, 1);
      reject(new Error("Encrypted response timed out"));
    }, 20_000);
    waiters.push(waiter);
  });
}

const ready = JSON.parse(await nextMessage());
if (ready.type !== "bridge_ready" || ready.protocol !== "e2e-v1" || typeof ready.id !== "string") {
  throw new Error("Invalid E2E bridge greeting");
}
const key = await deriveEncryptionKey(token);
let sendSequence = 0;
let receiveSequence = 0;
async function sendEncrypted(value) {
  socket.send(await encryptPacket(key, ready.id, "browser", sendSequence, value));
  sendSequence += 1;
}
async function receiveEncrypted() {
  const value = await decryptPacket(key, ready.id, "connector", receiveSequence, await nextMessage());
  receiveSequence += 1;
  return value;
}

const challengeBytes = new Uint8Array(24);
crypto.getRandomValues(challengeBytes);
const challenge = bytesToBase64URL(challengeBytes);
await sendEncrypted({ v: 1, type: "authenticate", challenge });
const authenticated = await receiveEncrypted();
if (authenticated.type !== "authenticated" || authenticated.challenge !== challenge) {
  throw new Error("Connector did not prove possession of the browser key");
}

const requestID = crypto.randomUUID();
await sendEncrypted({ v: 1, type: "http_request", id: requestID, method: "GET", path: "/api/sessions", body: "" });
const response = await receiveEncrypted();
if (response.type !== "http_response" || response.id !== requestID || response.status !== 200) {
  throw new Error("Encrypted session list failed");
}
const sessions = JSON.parse(response.body).sessions;
let desktopDenied = false;
let desktopBridge = false;
if (testDesktopDisabled) {
  const desktopID = crypto.randomUUID();
  await sendEncrypted({ v: 1, type: "desktop_open", id: desktopID });
  let desktopResponse;
  do {
    desktopResponse = await receiveEncrypted();
  } while (desktopResponse.id !== desktopID);
  if (desktopResponse.type !== "desktop_close" || desktopResponse.code !== 1008 || !desktopResponse.reason) {
    throw new Error("Disabled remote desktop was not rejected by the connector");
  }
  desktopDenied = true;
}
if (testDesktopBridge) {
  const desktopID = crypto.randomUUID();
  await sendEncrypted({ v: 1, type: "desktop_open", id: desktopID });
  let opened = false;
  let greeting;
  while (!opened || !greeting) {
    const desktopResponse = await receiveEncrypted();
    if (desktopResponse.id !== desktopID) continue;
    if (desktopResponse.type === "desktop_close") throw new Error(`Remote desktop closed during bridge test: ${desktopResponse.reason || "unknown reason"}`);
    if (desktopResponse.type === "desktop_opened") opened = true;
    if (desktopResponse.type === "desktop_data") greeting = base64URLToBytes(desktopResponse.data);
  }
  if (new TextDecoder().decode(greeting) !== "RFB 003.008\n") throw new Error("Remote desktop returned an invalid RFB greeting");
  await sendEncrypted({
    v: 1,
    type: "desktop_data",
    id: desktopID,
    data: bytesToBase64URL(new TextEncoder().encode("RFB 003.008\n")),
  });
  await sendEncrypted({ v: 1, type: "desktop_close", id: desktopID, code: 1000, reason: "Smoke test complete" });
  desktopBridge = true;
}
let windowCapture = false;
if (testWindowCapture) {
  const listID = crypto.randomUUID();
  await sendEncrypted({ v: 1, type: "window_sources_request", id: listID });
  let listed;
  do {
    listed = await receiveEncrypted();
  } while (listed.id !== listID);
  if (listed.type !== "window_sources" || listed.error || !listed.permissions?.screenRecording || !Array.isArray(listed.sources) || listed.sources.length === 0) {
    throw new Error(`Selected-window list failed: ${listed.error || "no permitted sources"}`);
  }
  const source = wantedWindow
    ? listed.sources.find((item) => `${item.application} ${item.title}`.toLowerCase().includes(wantedWindow.toLowerCase()))
    : listed.sources[0];
  if (!source) throw new Error(`Selected-window source did not match: ${wantedWindow}`);
  const windowID = crypto.randomUUID();
  await sendEncrypted({ v: 1, type: "window_open", id: windowID, windowId: source.id, maxWidth: 960, maxHeight: 720 });
  let windowOpened = false;
  let frame;
  while (!windowOpened || !frame) {
    const windowResponse = await receiveEncrypted();
    if (windowResponse.id !== windowID) continue;
    if (windowResponse.type === "window_close") throw new Error(`Selected window closed during capture test: ${windowResponse.reason || "unknown reason"}`);
    if (windowResponse.type === "window_opened") windowOpened = true;
    if (windowResponse.type === "window_frame") frame = base64URLToBytes(windowResponse.data);
  }
  if (frame.length < 4 || frame[0] !== 0xff || frame[1] !== 0xd8 || frame[2] !== 0xff) throw new Error("Selected-window stream returned an invalid JPEG frame");
  if (windowText) {
    await sendEncrypted({ v: 1, type: "window_input", id: windowID, kind: "text", text: windowText });
    if (saveWindowText) {
      await sendEncrypted({ v: 1, type: "window_input", id: windowID, kind: "key", code: "KeyS", down: true, meta: true });
      await sendEncrypted({ v: 1, type: "window_input", id: windowID, kind: "key", code: "KeyS", down: false, meta: true });
    }
  }
  await sendEncrypted({ v: 1, type: "window_close", id: windowID, code: 1000, reason: "Smoke test complete" });
  windowCapture = true;
}
let fileUpload = false;
if (testFileUpload) {
  const uploadID = crypto.randomUUID();
  const content = new TextEncoder().encode(`Termlinks encrypted upload smoke test ${Date.now()}\n`);
  await sendEncrypted({ v: 1, type: "file_upload_start", id: uploadID, name: uploadName, size: content.length });
  let uploadResponse = await receiveEncrypted();
  if (uploadResponse.type !== "file_upload_ready" || uploadResponse.id !== uploadID) {
    throw new Error(`Encrypted file upload did not become ready: ${uploadResponse.reason || uploadResponse.type}`);
  }
  await sendEncrypted({
    v: 1, type: "file_upload_chunk", id: uploadID, offset: 0, data: bytesToBase64URL(content),
  });
  uploadResponse = await receiveEncrypted();
  if (uploadResponse.type !== "file_upload_progress" || uploadResponse.id !== uploadID || uploadResponse.received !== content.length) {
    throw new Error(`Encrypted file upload chunk failed: ${uploadResponse.reason || uploadResponse.type}`);
  }
  await sendEncrypted({ v: 1, type: "file_upload_finish", id: uploadID });
  uploadResponse = await receiveEncrypted();
  if (uploadResponse.type !== "file_upload_complete" || uploadResponse.id !== uploadID || !uploadResponse.path?.endsWith(uploadName)) {
    throw new Error(`Encrypted file upload did not complete: ${uploadResponse.reason || uploadResponse.type}`);
  }
  fileUpload = true;
}
let session = wantedSession ? sessions.find((item) => item.name === wantedSession) : sessions[0];
if (createShell) {
  const createID = crypto.randomUUID();
  await sendEncrypted({
    v: 1,
    type: "http_request",
    id: createID,
    method: "POST",
    path: "/api/sessions",
    body: JSON.stringify({ name: "portal-shell-smoke", cwd: "/tmp" }),
  });
  let created;
  do {
    created = await receiveEncrypted();
  } while (created.type !== "http_response" || created.id !== createID);
  if (created.type !== "http_response" || created.id !== createID || created.status !== 201) {
    throw new Error(`Encrypted interactive-shell creation failed (${created.status ?? "invalid response"})`);
  }
  session = JSON.parse(created.body);
  input = `cd /tmp && printf '__TERMLINKS_CWD__%s\\n' "$PWD"`;
  expectedOutput = "__TERMLINKS_CWD__/tmp";
}
if (!session) throw new Error("Requested smoke-test session was not found");

const terminalID = crypto.randomUUID();
await sendEncrypted({ v: 1, type: "terminal_open", id: terminalID, sessionId: session.id });
let opened = false;
let output = new Uint8Array();
while (!opened || output.length === 0 || (expectedOutput && !new TextDecoder().decode(output).includes(expectedOutput))) {
  const message = await receiveEncrypted();
  if (message.id !== terminalID) continue;
  if (message.type === "terminal_opened") {
    opened = true;
    if (input) {
      await sendEncrypted({
        v: 1,
        type: "terminal_data",
        id: terminalID,
        binary: true,
        // Match xterm's Enter key. A PTY expects carriage return here; line
        // feed can be treated as pasted data by interactive line editors.
        data: bytesToBase64URL(new TextEncoder().encode(`${input}\r`)),
      });
    }
    continue;
  }
  if (message.type === "terminal_close") throw new Error("Terminal closed during encrypted smoke test");
  if (message.type === "terminal_data") {
    const next = message.binary ? base64URLToBytes(message.data) : new TextEncoder().encode(message.data);
    const combined = new Uint8Array(output.length + next.length);
    combined.set(output);
    combined.set(next, output.length);
    output = combined;
  }
}
await sendEncrypted({ v: 1, type: "terminal_close", id: terminalID, code: 1000, reason: "Smoke test complete" });
if (createShell) {
  const stopID = crypto.randomUUID();
  await sendEncrypted({ v: 1, type: "http_request", id: stopID, method: "POST", path: `/api/sessions/${session.id}/stop`, body: "" });
  let stopped;
  do {
    stopped = await receiveEncrypted();
  } while (stopped.type !== "http_response" || stopped.id !== stopID);
  if (stopped.type !== "http_response" || stopped.id !== stopID || stopped.status !== 202) {
    throw new Error(`Encrypted smoke shell cleanup failed (${stopped.status ?? "invalid response"})`);
  }
}
socket.close(1000, "Smoke test complete");
console.log(JSON.stringify({ authenticated: true, sessions: sessions.length, terminalOutput: true, keyboardInput: Boolean(input), interactiveShell: createShell, desktopDenied, desktopBridge, windowCapture, fileUpload, encryption: "AES-256-GCM e2e-v1" }));
// Node's built-in WebSocket can retain the Cloudflare close handshake handle
// after the protocol assertions have completed successfully.
setTimeout(() => process.exit(0), 50);
