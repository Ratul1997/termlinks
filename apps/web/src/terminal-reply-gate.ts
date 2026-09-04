const MAX_HELD_TERMINAL_REPLY_BYTES = 4096;

/**
 * Prevents xterm from sending protocol replies for every control query in a
 * retained scrollback snapshot. Only the final small reply is useful: it can
 * satisfy an application that was waiting for a cursor/device report when the
 * snapshot was captured. Live output continues to pass through immediately.
 */
export class TerminalReplyGate {
  private generation = 0;
  private replaying = false;
  private held?: Uint8Array;

  beginSnapshot(): number {
    this.generation += 1;
    this.replaying = true;
    this.held = undefined;
    return this.generation;
  }

  receive(data: Uint8Array): Uint8Array | undefined {
    if (!this.replaying) return data;
    if (data.byteLength > 0 && data.byteLength <= MAX_HELD_TERMINAL_REPLY_BYTES) {
      this.held = data.slice();
    }
    return undefined;
  }

  finishSnapshot(generation: number): Uint8Array | undefined {
    if (!this.replaying || generation !== this.generation) return undefined;
    this.replaying = false;
    const held = this.held;
    this.held = undefined;
    return held;
  }

  reset(): void {
    this.generation += 1;
    this.replaying = false;
    this.held = undefined;
  }
}

