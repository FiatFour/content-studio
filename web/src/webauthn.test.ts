import { describe, expect, it } from "vitest";
import { b64urlToBytes, bytesToB64url, creationOptions, requestOptions } from "./webauthn";

describe("base64url", () => {
  it("round-trips arbitrary bytes without padding or +/", () => {
    const bytes = new Uint8Array([0xfb, 0xff, 0xfe, 0x00, 0x01, 0x7f, 0x80]);
    const s = bytesToB64url(bytes);
    expect(s).not.toMatch(/[+/=]/);
    expect(Array.from(b64urlToBytes(s))).toEqual(Array.from(bytes));
  });
  it("decodes the server's raw-url encoding", () => {
    expect(Array.from(b64urlToBytes("AQID"))).toEqual([1, 2, 3]);
    expect(Array.from(b64urlToBytes("AQI"))).toEqual([1, 2]);
  });
});

describe("options conversion", () => {
  it("turns challenge, user id and excluded ids into bytes", () => {
    const o = creationOptions({ publicKey: {
      challenge: "AQID", rp: { id: "localhost", name: "x" }, pubKeyCredParams: [{ type: "public-key", alg: -7 }],
      user: { id: "BAUG", name: "owner", displayName: "Owner" }, excludeCredentials: [{ id: "BwgJ", type: "public-key" }],
    } });
    expect(Array.from(new Uint8Array(o.challenge as ArrayBuffer))).toEqual([1, 2, 3]);
    expect(Array.from(new Uint8Array(o.user.id as ArrayBuffer))).toEqual([4, 5, 6]);
    expect(Array.from(new Uint8Array(o.excludeCredentials![0].id as ArrayBuffer))).toEqual([7, 8, 9]);
    expect(o.rp.id).toBe("localhost");
  });
  it("converts a discoverable login request that has no allow list", () => {
    const o = requestOptions({ publicKey: { challenge: "AQID", rpId: "localhost", userVerification: "required" } });
    expect(Array.from(new Uint8Array(o.challenge as ArrayBuffer))).toEqual([1, 2, 3]);
    expect(o.allowCredentials).toBeUndefined();
    expect(o.userVerification).toBe("required");
  });
});
