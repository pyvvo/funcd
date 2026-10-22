package gateway

import (
	"encoding/binary"
)

// tokenFieldID is the Quack handshake TLV field id of the token: the FIRST field, at offset 8 (after the
// 8-byte preamble), framed <id:1=0x01><0x00><uvarint len><value> (ADR-0137 feasibility spike, DuckDB 1.5.4).
const tokenFieldID = 0x01

// preambleLen is the fixed 8-byte handshake preamble before the first TLV field.
const preambleLen = 8

// tokenHdrLen is the token field header before its length: the id byte (0x01) + a constant 0x00 byte.
const tokenHdrLen = 2

// swapHandshakeToken decodes the Quack handshake's token field (the first field, id 0x01 at offset 8),
// returns the caller token, and rewrites the body to carry newToken. The token length is an UNSIGNED LEB128
// VARINT (encoding/binary.Uvarint) — 1 byte for a value < 128, 2 for >= 128 — so it MUST be decoded, not
// read at a fixed width: a JWT (always >= 128 bytes) uses a 2-byte length, and a fixed-width parse mis-reads
// the varint's second byte as token data. ok=false ⇒ the body is NOT the token-bearing handshake (a shape
// mismatch or a session-id-keyed follow-up); the original body is returned unchanged so the caller forwards
// it opaquely — FAIL-CLOSED, since an un-swapped body still carries the caller's token (≠ the shared engine
// token), which the engine rejects; no un-authorized request can smuggle the engine token.
func swapHandshakeToken(body []byte, newToken string) (rewritten []byte, callerToken string, ok bool) {
	// Need the 8-byte preamble + the 2-byte token-field header (id 0x01, then 0x00) to even parse.
	if len(body) < preambleLen+tokenHdrLen || body[preambleLen] != tokenFieldID || body[preambleLen+1] != 0x00 {
		return body, "", false
	}
	tlen64, n := binary.Uvarint(body[preambleLen+tokenHdrLen:])
	if n <= 0 { // 0 ⇒ buffer too small for the varint; <0 ⇒ varint overflow
		return body, "", false
	}
	valStart := preambleLen + tokenHdrLen + n
	tlen := int(tlen64)
	if tlen < 0 || valStart+tlen > len(body) {
		return body, "", false
	}
	callerToken = string(body[valStart : valStart+tlen])

	nt := []byte(newToken)
	var lenBuf [binary.MaxVarintLen64]byte
	ln := binary.PutUvarint(lenBuf[:], uint64(len(nt)))
	out := make([]byte, 0, valStart-n+ln+len(nt)+(len(body)-(valStart+tlen)))
	out = append(out, body[:preambleLen+tokenHdrLen]...) // preamble + id (0x01) + 0x00
	out = append(out, lenBuf[:ln]...)                    // the new token length as a uvarint
	out = append(out, nt...)                             // the swapped token
	out = append(out, body[valStart+tlen:]...)           // remaining fields, verbatim
	return out, callerToken, true
}
