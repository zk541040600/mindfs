# E2EE Security Contracts

## Scenario: Versioned E2EE transport, replay protection, and pairing-secret handling

### 1. Scope / Trigger

- Trigger: an E2EE handshake, protected HTTP request/response, protected raw-file response, or WebSocket frame crosses the browser/server boundary.
- The server supports protocol versions 1 and 2. The current browser requests version 2 and rejects a downgraded response; version 1 remains only for already-published clients.
- Replay metadata must contain only validated proof values, handshake nonces, or per-session counters. Never persist session keys, request bodies, plaintext, or pairing secrets in replay state.

### 2. Signatures

- `func DeriveKeyForProtocol(..., protocolVersion int) (DerivedKey, error)`
- `func BuildAcceptProofForProtocol(..., protocolVersion int) string`
- `func (m *Manager) ConsumeRequestProof(clientID, proof string, expiresAt time.Time) bool`
- `func (m *Manager) ConsumeOpenNonce(clientID, nonce string) bool`
- `func (m *Manager) ConsumeClientWSSequence(clientID, sessionID string, sequence uint64) error`
- `func (m *Manager) NextServerWSSequence(clientID, sessionID string) (uint64, error)`
- `func EncryptJSONWithAAD(key []byte, value any, aad []byte) (*CipherEnvelope, error)`
- `func DecryptJSONWithAAD(key []byte, envelope *CipherEnvelope, aad []byte, out any) error`
- `func writeProtectedJSON(w http.ResponseWriter, status int, sess *e2ee.Session, requestProof string, value any) error`
- HTTP proof fields: `X-MindFS-Client-ID`, `X-MindFS-TS` (RFC3339), `X-MindFS-Proof`; WebSocket upgrade equivalents are `client_id`, `e2ee_ts`, and `e2ee_proof` query parameters.

### 3. Contracts

- A version-2 key derivation includes the protocol version in HKDF info, and the server-acceptance HMAC uses the version-specific label. A v1 transcript must not derive a v2 transport key or validate a v2 acceptance proof.
- Verify the E2EE session, timestamp window, and HMAC before consuming an HTTP or WebSocket-upgrade proof. A valid proof is accepted once per client ID until `timestamp + requestProofMaxSkew`; repeat use returns `e2ee_proof_replayed` and must not refresh `LastSeenAt`.
- A valid `/api/e2ee/open` nonce is accepted once per client while the resulting session remains active. A replay returns HTTP 409 `e2ee_open_replayed` and must not replace the current session.
- Version-2 WebSocket plaintext is an encrypted `{sequence, message}` frame. Client and server maintain independent monotonically increasing counters in the session; a received zero, stale, or repeated sequence is rejected before dispatch. Frame counters renew only the session that owns the frame. Version 1 retains its legacy encrypted payload format.
- A version-2 protected JSON response is AES-GCM encrypted with the exact request proof as additional authenticated data (AAD). A raw-file envelope uses `requestProof + "\\x1f" + contentType` as AAD. Captured ciphertext therefore cannot authenticate as the response to a different valid request. Version 1 preserves its no-AAD response format for compatibility.
- The browser keeps an in-memory, five-minute pending proof-to-session binding only until the associated `Response` is received. Protected response parsing consumes that binding exactly once; an unbound encrypted response fails with `e2ee_response_unbound`.
- A plaintext E2EE error is not authenticated. Browser recovery may discard the current session for `e2ee_proof_invalid`, but must not delete the stored pairing secret from a plaintext HTTP or WebSocket error. A pairing secret is only replaced by explicit user input.
- `e2ee.json` contains the pairing secret. `EnsureConfigAtPath` must apply mode `0600` both when generating it and when accepting an existing valid configuration.

### 4. Validation & Error Matrix

| Condition | Result |
| --- | --- |
| Unsupported open protocol version | `e2ee_protocol_unsupported` |
| Version-2 acceptance proof or key transcript does not match | handshake fails; no usable session |
| Missing proof fields | `e2ee_proof_required` |
| Non-RFC3339 timestamp | `invalid_e2ee_ts` |
| Timestamp outside +/- five minutes | `e2ee_proof_expired` |
| HMAC mismatch | `e2ee_proof_invalid` |
| Previously consumed HTTP/WS-upgrade proof | `e2ee_proof_replayed` |
| Previously consumed open nonce | HTTP 409 `e2ee_open_replayed` |
| Version-2 duplicate or non-positive WebSocket sequence | `e2ee_frame_replayed` or `e2ee_frame_invalid` |
| Response encrypted for a different proof or raw content type | AES-GCM authentication failure |
| Encrypted browser response without its request binding | `e2ee_response_unbound` |
| No active E2EE session | `e2ee_session_missing` or `e2ee_session_expired` |

### 5. Good / Base / Bad Cases

- Good: the server validates and consumes a new proof, then emits a v2 response whose GCM tag authenticates that proof; the browser consumes the matching response binding and decrypts it.
- Base: an E2EE-disabled server preserves non-E2EE transport behavior, while a version-1 client continues using legacy encrypted messages and no-AAD responses.
- Bad: adding an unbounded cache of received ciphertexts for response replay prevention. It increases memory pressure and still misses cross-request substitution; request-proof AAD directly expresses the required security property.
- Bad: trusting a plaintext `e2ee_proof_invalid` error enough to erase the stored pairing secret. An on-path response can cause a persistent local denial of service even though it cannot prove knowledge of the secret.

### 6. Tests Required

- `server/internal/e2ee/crypto_test.go`: v1 and v2 derive distinct transport keys; AAD decrypts only with its original context.
- `server/internal/e2ee/manager_test.go`: request proofs and open nonces reject replay; version-2 inbound/outbound counters reject duplicates and are scoped to the active session.
- `server/internal/api/http_test.go`: version-2 protected JSON and raw-file responses decrypt with only their original request proof; version-1 JSON remains decryptable with the legacy format.
- `server/internal/api/ws_test.go` and `server/internal/api/stream_hub_test.go`: replayed v2 frames are rejected, outbound frames are ordered, and active-session E2EE errors are encrypted.
- `web/tests/e2ee.spec.ts`: browser WebSocket replay rejection and protected-response substitution rejection.
- `server/internal/e2ee/config_test.go`: a valid existing `e2ee.json` with permissive mode is tightened to `0600`.

### 7. Wrong vs Correct

#### Wrong

```go
envelope, err := e2ee.EncryptJSON(sess.Key, payload)
```

This lets a valid ciphertext be replayed as the response to another request in the same session because the GCM tag has no request context.

#### Correct

```go
proof := strings.TrimSpace(r.Header.Get(e2eeProofHeaderName))
envelope, err := e2ee.EncryptJSONWithAAD(sess.Key, payload, []byte(proof))
```

The client must use the exact proof that created the `Response` as AAD while decrypting. A response captured for another proof then fails GCM authentication without an extra ciphertext cache.
