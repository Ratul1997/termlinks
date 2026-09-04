import assert from "node:assert/strict";
import {
  ceremonyMessage,
  passkeyLoginOffered,
  serializeAssertion,
  serializeAttestation,
  toCreationOptions,
  toRequestOptions,
  type AuthCapabilities,
} from "./passkeys";

const buffer = (...values: number[]): ArrayBuffer => new Uint8Array(values).buffer;

// The daemon speaks base64url; the browser APIs speak ArrayBuffers.
const creation = toCreationOptions({
  challenge: "AQID",
  rp: { id: "local.example.com", name: "Termlinks" },
  user: { id: "BAUG", name: "termlinks", displayName: "Termlinks owner" },
  pubKeyCredParams: [{ type: "public-key", alg: -7 }],
  excludeCredentials: [{ id: "BwgJ", type: "public-key", transports: ["internal"] }],
  authenticatorSelection: { residentKey: "required", userVerification: "required" },
  attestation: "none",
});
assert.deepEqual(new Uint8Array(creation.challenge as ArrayBuffer), new Uint8Array([1, 2, 3]));
assert.deepEqual(new Uint8Array(creation.user.id as ArrayBuffer), new Uint8Array([4, 5, 6]));
const [excluded] = creation.excludeCredentials ?? [];
assert.ok(excluded, "excludeCredentials should be converted");
assert.deepEqual(new Uint8Array(excluded.id as ArrayBuffer), new Uint8Array([7, 8, 9]));
assert.equal(creation.attestation, "none");
assert.equal(creation.authenticatorSelection?.userVerification, "required");

// A discoverable login carries no allowCredentials, which is what makes the
// browser offer "use a phone or tablet" and show its own QR code.
const request = toRequestOptions({ challenge: "AQID", userVerification: "required", rpId: "local.example.com" });
assert.deepEqual(new Uint8Array(request.challenge as ArrayBuffer), new Uint8Array([1, 2, 3]));
assert.deepEqual(request.allowCredentials, []);

const attestation = JSON.parse(serializeAttestation({
  id: "Y3JlZA",
  rawId: buffer(99, 114, 101, 100),
  type: "public-key",
  response: {
    clientDataJSON: buffer(1, 2),
    attestationObject: buffer(3, 4),
    getTransports: () => ["internal", "hybrid"],
  },
  getClientExtensionResults: () => ({ credProps: { rk: true } }),
}));
assert.deepEqual(attestation, {
  id: "Y3JlZA",
  rawId: "Y3JlZA",
  type: "public-key",
  response: { clientDataJSON: "AQI", attestationObject: "AwQ", transports: ["internal", "hybrid"] },
  clientExtensionResults: { credProps: { rk: true } },
});

const assertion = JSON.parse(serializeAssertion({
  id: "Y3JlZA",
  rawId: buffer(99, 114, 101, 100),
  type: "public-key",
  response: { clientDataJSON: buffer(1, 2), authenticatorData: buffer(3, 4), signature: buffer(5, 6), userHandle: buffer(7, 8) },
}));
assert.deepEqual(assertion, {
  id: "Y3JlZA",
  rawId: "Y3JlZA",
  type: "public-key",
  response: { clientDataJSON: "AQI", authenticatorData: "AwQ", signature: "BQY", userHandle: "Bwg" },
  clientExtensionResults: {},
});

// An authenticator that reports no user handle must not send an empty one.
const withoutHandle = JSON.parse(serializeAssertion({
  id: "Y3JlZA",
  rawId: buffer(99),
  type: "public-key",
  response: { clientDataJSON: buffer(1), authenticatorData: buffer(2), signature: buffer(3), userHandle: null },
}));
assert.equal("userHandle" in withoutHandle.response, false);

// Without a secure context there is no WebAuthn, so the login page must not
// offer a passkey even when the daemon reports credentials.
const enrolled: AuthCapabilities = { configured: true, supported: true, enrolled: true, origin: "https://local.example.com" };
assert.equal(passkeyLoginOffered(enrolled), false);
assert.equal(passkeyLoginOffered({ ...enrolled, supported: false }), false);
assert.equal(passkeyLoginOffered({ ...enrolled, enrolled: false }), false);

const cancelled = new Error("cancelled");
cancelled.name = "NotAllowedError";
assert.match(ceremonyMessage(cancelled, "fallback"), /dismissed or timed out/);
const duplicate = new Error("exists");
duplicate.name = "InvalidStateError";
assert.match(ceremonyMessage(duplicate, "fallback"), /already has a passkey/);
assert.equal(ceremonyMessage("string", "fallback"), "fallback");

console.log("passkeys tests passed");
