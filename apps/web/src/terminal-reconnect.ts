export const MAX_TERMINAL_SNAPSHOT_BYTES = 2 << 20;

export type TerminalStreamControl =
  | { type: "terminal_snapshot_start"; bytes: number }
  | { type: "terminal_snapshot_end" };

export type TerminalStreamAction =
  | { kind: "snapshot"; data: Uint8Array }
  | { kind: "live"; data: Uint8Array };

type StreamPhase = "awaiting" | "snapshot" | "live";

/**
 * Separates a complete reconnect snapshot from output produced after it.
 *
 * New daemons explicitly frame snapshots. Older daemons always send their
 * complete scrollback as the first binary frame, so that frame remains a safe
 * rolling-upgrade fallback.
 */
export class TerminalStreamReconciler {
  private phase: StreamPhase = "awaiting";
  private expectedBytes = 0;
  private receivedBytes = 0;
  private chunks: Uint8Array[] = [];

  get waitingForSnapshot(): boolean {
    return this.phase !== "live";
  }

  get framedSnapshotStarted(): boolean {
    return this.phase === "snapshot";
  }

  receiveControl(control: TerminalStreamControl): TerminalStreamAction | undefined {
    if (control.type === "terminal_snapshot_start") {
      if (this.phase !== "awaiting") throw new Error("Unexpected terminal snapshot start");
      if (!Number.isSafeInteger(control.bytes) || control.bytes < 0 || control.bytes > MAX_TERMINAL_SNAPSHOT_BYTES) {
        throw new Error("Invalid terminal snapshot size");
      }
      this.phase = "snapshot";
      this.expectedBytes = control.bytes;
      this.receivedBytes = 0;
      this.chunks = [];
      return undefined;
    }
    if (this.phase !== "snapshot") throw new Error("Unexpected terminal snapshot end");
    if (this.receivedBytes !== this.expectedBytes) throw new Error("Incomplete terminal snapshot");
    const snapshot = new Uint8Array(this.receivedBytes);
    let offset = 0;
    for (const chunk of this.chunks) {
      snapshot.set(chunk, offset);
      offset += chunk.byteLength;
    }
    this.chunks = [];
    this.phase = "live";
    return { kind: "snapshot", data: snapshot };
  }

  receiveBinary(data: Uint8Array): TerminalStreamAction | undefined {
    // Current and historical daemons send a scrollback snapshot as one frame.
    // Copy it because WebSocket-owned buffers must not outlive the callback.
    if (this.phase === "awaiting") {
      this.phase = "live";
      return { kind: "snapshot", data: data.slice() };
    }
    if (this.phase === "snapshot") {
      this.receivedBytes += data.byteLength;
      if (this.receivedBytes > this.expectedBytes || this.receivedBytes > MAX_TERMINAL_SNAPSHOT_BYTES) {
        throw new Error("Terminal snapshot exceeded its declared size");
      }
      this.chunks.push(data.slice());
      return undefined;
    }
    return { kind: "live", data };
  }
}

export function terminalStreamControl(value: unknown): TerminalStreamControl | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return undefined;
  const record = value as Record<string, unknown>;
  if (record.type === "terminal_snapshot_start" && typeof record.bytes === "number") {
    return { type: record.type, bytes: record.bytes };
  }
  if (record.type === "terminal_snapshot_end") return { type: record.type };
  return undefined;
}

