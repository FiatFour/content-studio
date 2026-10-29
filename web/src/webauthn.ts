// Browser WebAuthn glue. The server (go-webauthn) speaks the JSON form of the options and expects
// the JSON form of the result; these helpers convert between that and navigator.credentials.
// The conversion functions are pure so they can be tested without a browser.

export function b64urlToBytes(s: string): Uint8Array<ArrayBuffer> {
  const pad = "=".repeat((4 - (s.length % 4)) % 4);
  const bin = atob(s.replace(/-/g, "+").replace(/_/g, "/") + pad);
  const out = new Uint8Array(new ArrayBuffer(bin.length));
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

export function bytesToB64url(buf: ArrayBuffer | Uint8Array): string {
  const bytes = buf instanceof Uint8Array ? buf : new Uint8Array(buf);
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

type Descriptor = { id: string; type: PublicKeyCredentialType; transports?: AuthenticatorTransport[] };
type CreationJSON = { publicKey: Omit<PublicKeyCredentialCreationOptions, "challenge" | "user" | "excludeCredentials"> & { challenge: string; user: { id: string; name: string; displayName: string }; excludeCredentials?: Descriptor[] } };
type RequestJSON = { publicKey: Omit<PublicKeyCredentialRequestOptions, "challenge" | "allowCredentials"> & { challenge: string; allowCredentials?: Descriptor[] } };

const descriptors = (list?: Descriptor[]) => list?.map((d) => ({ ...d, id: b64urlToBytes(d.id) }));

export function creationOptions(o: CreationJSON): PublicKeyCredentialCreationOptions {
  const p = o.publicKey;
  return { ...p, challenge: b64urlToBytes(p.challenge), user: { ...p.user, id: b64urlToBytes(p.user.id) }, excludeCredentials: descriptors(p.excludeCredentials) };
}

export function requestOptions(o: RequestJSON): PublicKeyCredentialRequestOptions {
  const p = o.publicKey;
  return { ...p, challenge: b64urlToBytes(p.challenge), allowCredentials: descriptors(p.allowCredentials) };
}

/** True when this page can run a WebAuthn ceremony at all (secure context plus the API). */
export function webauthnSupported(): boolean {
  return typeof window !== "undefined" && window.isSecureContext && typeof PublicKeyCredential !== "undefined" && !!navigator.credentials;
}

export function serializeCreation(c: PublicKeyCredential): unknown {
  const r = c.response as AuthenticatorAttestationResponse;
  return {
    id: c.id, rawId: bytesToB64url(c.rawId), type: c.type, authenticatorAttachment: c.authenticatorAttachment ?? undefined,
    clientExtensionResults: c.getClientExtensionResults(),
    response: { clientDataJSON: bytesToB64url(r.clientDataJSON), attestationObject: bytesToB64url(r.attestationObject), transports: r.getTransports?.() ?? [] },
  };
}

export function serializeAssertion(c: PublicKeyCredential): unknown {
  const r = c.response as AuthenticatorAssertionResponse;
  return {
    id: c.id, rawId: bytesToB64url(c.rawId), type: c.type, authenticatorAttachment: c.authenticatorAttachment ?? undefined,
    clientExtensionResults: c.getClientExtensionResults(),
    response: {
      clientDataJSON: bytesToB64url(r.clientDataJSON), authenticatorData: bytesToB64url(r.authenticatorData),
      signature: bytesToB64url(r.signature), userHandle: r.userHandle ? bytesToB64url(r.userHandle) : undefined,
    },
  };
}

export async function createPasskey(options: CreationJSON): Promise<unknown> {
  const c = await navigator.credentials.create({ publicKey: creationOptions(options) });
  if (!(c instanceof PublicKeyCredential)) throw new Error("no credential");
  return serializeCreation(c);
}

export async function getPasskey(options: RequestJSON): Promise<unknown> {
  const c = await navigator.credentials.get({ publicKey: requestOptions(options) });
  if (!(c instanceof PublicKeyCredential)) throw new Error("no credential");
  return serializeAssertion(c);
}

/** True when the user dismissed or timed out the browser's passkey prompt. */
export const wasCancelled = (e: unknown) => e instanceof DOMException && (e.name === "NotAllowedError" || e.name === "AbortError");
