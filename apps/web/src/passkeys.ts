import { base64URLToBytes, bytesToBase64URL } from "./e2e";

export type AuthCapabilities = {
  configured: boolean;
  supported: boolean;
  enrolled: boolean;
  origin: string;
  count?: number;
};

export type Passkey = {
  id: string;
  label: string;
  createdAt: string;
  lastUsedAt?: string;
};

export const noPasskeySupport: AuthCapabilities = { configured: false, supported: false, enrolled: false, origin: "" };

type Descriptor = { id: string; type: string; transports?: string[] };

/**
 * webAuthnAvailable reports whether this browser can run a passkey ceremony at
 * all. A page opened over plain http on localhost or a raw LAN address is not a
 * secure context, so token login stays the only option there.
 */
export function webAuthnAvailable(): boolean {
  return typeof window !== "undefined"
    && window.isSecureContext === true
    && typeof window.PublicKeyCredential === "function"
    && typeof navigator !== "undefined"
    && typeof navigator.credentials?.get === "function";
}

/** passkeyLoginOffered reports whether the login page should lead with a passkey. */
export function passkeyLoginOffered(capabilities: AuthCapabilities): boolean {
  return capabilities.supported && capabilities.enrolled && webAuthnAvailable();
}

/**
 * toCreationOptions converts the base64url fields of the daemon's registration
 * challenge into the binary buffers navigator.credentials.create() expects.
 */
export function toCreationOptions(publicKey: Record<string, unknown>): PublicKeyCredentialCreationOptions {
  const user = publicKey.user as { id: string; name: string; displayName: string };
  return {
    ...publicKey,
    challenge: bytes(publicKey.challenge as string),
    user: { ...user, id: bytes(user.id) },
    excludeCredentials: descriptors(publicKey.excludeCredentials as Descriptor[] | undefined),
  } as unknown as PublicKeyCredentialCreationOptions;
}

/** toRequestOptions does the same for the daemon's login challenge. */
export function toRequestOptions(publicKey: Record<string, unknown>): PublicKeyCredentialRequestOptions {
  return {
    ...publicKey,
    challenge: bytes(publicKey.challenge as string),
    allowCredentials: descriptors(publicKey.allowCredentials as Descriptor[] | undefined),
  } as unknown as PublicKeyCredentialRequestOptions;
}

type AttestationCredential = {
  id: string;
  rawId: ArrayBuffer;
  type: string;
  response: { clientDataJSON: ArrayBuffer; attestationObject: ArrayBuffer; getTransports?: () => string[] };
  getClientExtensionResults?: () => unknown;
};

type AssertionCredential = {
  id: string;
  rawId: ArrayBuffer;
  type: string;
  response: { clientDataJSON: ArrayBuffer; authenticatorData: ArrayBuffer; signature: ArrayBuffer; userHandle: ArrayBuffer | null };
  getClientExtensionResults?: () => unknown;
};

/** serializeAttestation renders a new credential for the register/finish endpoint. */
export function serializeAttestation(credential: AttestationCredential): string {
  return JSON.stringify({
    id: credential.id,
    rawId: encode(credential.rawId),
    type: credential.type,
    response: {
      clientDataJSON: encode(credential.response.clientDataJSON),
      attestationObject: encode(credential.response.attestationObject),
      transports: credential.response.getTransports?.() ?? [],
    },
    clientExtensionResults: credential.getClientExtensionResults?.() ?? {},
  });
}

/** serializeAssertion renders a login response for the login/finish endpoint. */
export function serializeAssertion(credential: AssertionCredential): string {
  return JSON.stringify({
    id: credential.id,
    rawId: encode(credential.rawId),
    type: credential.type,
    response: {
      clientDataJSON: encode(credential.response.clientDataJSON),
      authenticatorData: encode(credential.response.authenticatorData),
      signature: encode(credential.response.signature),
      userHandle: credential.response.userHandle ? encode(credential.response.userHandle) : undefined,
    },
    clientExtensionResults: credential.getClientExtensionResults?.() ?? {},
  });
}

/**
 * ceremonyMessage turns a WebAuthn DOMException into copy the owner can act on.
 * A cancelled prompt is the common case and is not an error worth alarming over.
 */
export function ceremonyMessage(caught: unknown, fallback: string): string {
  if (caught instanceof Error) {
    if (caught.name === "NotAllowedError") return "The passkey prompt was dismissed or timed out. Try again.";
    if (caught.name === "InvalidStateError") return "This device already has a passkey for Termlinks.";
    if (caught.name === "SecurityError") return "This page's address does not match the configured passkey origin.";
    if (caught.message) return caught.message;
  }
  return fallback;
}

function descriptors(list: Descriptor[] | undefined): PublicKeyCredentialDescriptor[] {
  return (list ?? []).map((descriptor) => ({
    ...descriptor,
    id: bytes(descriptor.id),
  })) as unknown as PublicKeyCredentialDescriptor[];
}

function bytes(value: string): ArrayBuffer {
  const decoded = base64URLToBytes(value);
  return decoded.buffer.slice(decoded.byteOffset, decoded.byteOffset + decoded.byteLength) as ArrayBuffer;
}

function encode(buffer: ArrayBuffer): string {
  return bytesToBase64URL(new Uint8Array(buffer));
}
