package authtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

// Authenticator is a minimal software ES256 passkey for tests: it answers the JSON options the server
// sends and produces the JSON a browser's navigator.credentials would. It signs for real, so the
// server's verification (go-webauthn) runs unmodified. Attestation is "none".
type Authenticator struct {
	t          testing.TB
	key        *ecdsa.PrivateKey
	CredID     []byte
	UserHandle []byte
	Origin     string
	RPID       string
	Count      uint32 // signature counter reported on the next assertion (incremented before use)
}

func NewAuthenticator(t testing.TB, rpID, origin string) *Authenticator {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	return &Authenticator{t: t, key: k, CredID: id, Origin: origin, RPID: rpID}
}

var b64 = base64.RawURLEncoding

func (a *Authenticator) clientData(typ, challenge string) []byte {
	b, err := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": a.Origin, "crossOrigin": false})
	if err != nil {
		a.t.Fatal(err)
	}
	return b
}

func (a *Authenticator) authData(flags byte, count uint32, attested bool) []byte {
	h := sha256.Sum256([]byte(a.RPID))
	out := append([]byte{}, h[:]...)
	out = append(out, flags)
	out = binary.BigEndian.AppendUint32(out, count)
	if !attested {
		return out
	}
	out = append(out, make([]byte, 16)...) // AAGUID: zeros
	out = binary.BigEndian.AppendUint16(out, uint16(len(a.CredID)))
	out = append(out, a.CredID...)
	pub, err := a.key.PublicKey.ECDH()
	if err != nil {
		a.t.Fatal(err)
	}
	raw := pub.Bytes() // 0x04 || X || Y
	cose, err := cbor.CoreDetEncOptions().EncMode()
	if err != nil {
		a.t.Fatal(err)
	}
	key, err := cose.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: raw[1:33], -3: raw[33:65]})
	if err != nil {
		a.t.Fatal(err)
	}
	return append(out, key...)
}

type creationOptions struct {
	PublicKey struct {
		Challenge string `json:"challenge"`
		User      struct {
			ID string `json:"id"`
		} `json:"user"`
	} `json:"publicKey"`
}

// Create answers a registration options object (the "options" field of /auth/enroll/begin).
func (a *Authenticator) Create(options json.RawMessage) json.RawMessage {
	var o creationOptions
	if err := json.Unmarshal(options, &o); err != nil || o.PublicKey.Challenge == "" {
		a.t.Fatalf("bad creation options: %v", err)
	}
	if uh, err := b64.DecodeString(o.PublicKey.User.ID); err == nil {
		a.UserHandle = uh
	}
	att, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": a.authData(0x01|0x04|0x40, 0, true)})
	if err != nil {
		a.t.Fatal(err)
	}
	return a.encode(map[string]any{
		"id": b64.EncodeToString(a.CredID), "rawId": b64.EncodeToString(a.CredID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(a.clientData("webauthn.create", o.PublicKey.Challenge)),
			"attestationObject": b64.EncodeToString(att), "transports": []string{"internal"},
		},
	})
}

// Get answers a login options object (the "options" field of /auth/login/begin). tamper corrupts the signature.
func (a *Authenticator) Get(options json.RawMessage, tamper bool) json.RawMessage {
	var o struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(options, &o); err != nil || o.PublicKey.Challenge == "" {
		a.t.Fatalf("bad request options: %v", err)
	}
	a.Count++
	ad := a.authData(0x01|0x04, a.Count, false)
	cd := a.clientData("webauthn.get", o.PublicKey.Challenge)
	cdh := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), cdh[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		a.t.Fatal(err)
	}
	if tamper {
		sig[len(sig)-1] ^= 0xff
	}
	return a.encode(map[string]any{
		"id": b64.EncodeToString(a.CredID), "rawId": b64.EncodeToString(a.CredID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON": b64.EncodeToString(cd), "authenticatorData": b64.EncodeToString(ad),
			"signature": b64.EncodeToString(sig), "userHandle": b64.EncodeToString(a.UserHandle),
		},
	})
}

func (a *Authenticator) encode(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		a.t.Fatal(err)
	}
	return b
}
