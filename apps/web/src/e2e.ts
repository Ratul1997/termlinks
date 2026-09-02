const encoder = new TextEncoder();
const decoder = new TextDecoder();

export type E2EDirection = "browser" | "connector";

export async function deriveEncryptionKey(token: string): Promise<CryptoKey> {
  const material = await crypto.subtle.digest("SHA-256", encoder.encode(`termlinks-e2e-v1\u0000${token}`));
  return crypto.subtle.importKey("raw", material, { name: "AES-GCM" }, false, ["encrypt", "decrypt"]);
}

export async function encryptPacket(
  key: CryptoKey,
  channel: string,
  direction: E2EDirection,
  sequence: number,
  value: Record<string, unknown>,
): Promise<string> {
  if (!Number.isInteger(sequence) || sequence < 0 || sequence >= 0xffff_ffff) {
    throw new Error("Encrypted portal sequence is invalid");
  }
  const sequenceBytes = new Uint8Array(4);
  new DataView(sequenceBytes.buffer).setUint32(0, sequence);
  const iv = new Uint8Array(12);
  crypto.getRandomValues(iv);
  const plaintext = encoder.encode(JSON.stringify(value));
  const ciphertext = await crypto.subtle.encrypt(
    { name: "AES-GCM", iv, additionalData: encryptionAAD(channel, direction, sequenceBytes) },
    key,
    plaintext,
  );
  const packet = new Uint8Array(sequenceBytes.byteLength + iv.byteLength + ciphertext.byteLength);
  packet.set(sequenceBytes);
  packet.set(iv, sequenceBytes.byteLength);
  packet.set(new Uint8Array(ciphertext), sequenceBytes.byteLength + iv.byteLength);
  return bytesToBase64URL(packet);
}

export async function decryptPacket(
  key: CryptoKey,
  channel: string,
  direction: E2EDirection,
  expectedSequence: number,
  packet: string,
): Promise<unknown> {
  const bytes = base64URLToBytes(packet);
  if (bytes.byteLength < 32) throw new Error("Encrypted packet is too short");
  const sequenceBytes = bytes.slice(0, 4);
  const sequence = new DataView(sequenceBytes.buffer, sequenceBytes.byteOffset, sequenceBytes.byteLength).getUint32(0);
  if (sequence !== expectedSequence) throw new Error("Encrypted packet was replayed or reordered");
  const plaintext = await crypto.subtle.decrypt(
    { name: "AES-GCM", iv: bytes.slice(4, 16), additionalData: encryptionAAD(channel, direction, sequenceBytes) },
    key,
    bytes.slice(16),
  );
  return JSON.parse(decoder.decode(plaintext)) as unknown;
}

export function bytesToBase64URL(bytes: Uint8Array): string {
  let binary = "";
  for (let offset = 0; offset < bytes.length; offset += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000));
  }
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
}

export function base64URLToBytes(value: string): Uint8Array {
  const padded = value.replaceAll("-", "+").replaceAll("_", "/") + "===".slice((value.length + 3) % 4);
  const binary = atob(padded);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index);
  return bytes;
}

function encryptionAAD(channel: string, direction: E2EDirection, sequence: Uint8Array): Uint8Array {
  const prefix = encoder.encode(`termlinks-e2e-v1:${channel}:${direction}:`);
  const aad = new Uint8Array(prefix.byteLength + sequence.byteLength);
  aad.set(prefix);
  aad.set(sequence, prefix.byteLength);
  return aad;
}
