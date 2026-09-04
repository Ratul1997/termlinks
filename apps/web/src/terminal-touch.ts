export type TouchWheelResult = {
  directions: number[];
  remainder: number;
};

/**
 * Converts native touch movement into discrete wheel directions. A positive
 * direction matches wheel-down; a negative direction matches wheel-up.
 */
export function consumeTouchWheel(previousY: number, currentY: number, remainder: number, threshold: number): TouchWheelResult {
  if (![previousY, currentY, remainder, threshold].every(Number.isFinite) || threshold <= 0) {
    return { directions: [], remainder: 0 };
  }
  // One move event should never be able to allocate or send an unbounded
  // number of reports, even if a browser supplies a malformed coordinate.
  const distance = Math.max(-threshold * 12, Math.min(threshold * 12, remainder + previousY - currentY));
  const steps = Math.trunc(distance / threshold);
  const directions = Array.from({ length: Math.abs(steps) }, () => Math.sign(steps));
  return { directions, remainder: distance - steps * threshold };
}

export function binaryStringToBytes(value: string): Uint8Array {
  return Uint8Array.from(value, (character) => character.charCodeAt(0) & 0xff);
}
