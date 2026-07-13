import { appURL } from "./base";
import { getStoredString, removeStoredString, setStoredString } from "./storage";

const SECRET_STORAGE_PREFIX = "mindfs.e2ee.secret.";
const E2EE_PROTOCOL_VERSION = 2;
export const E2EE_HEADER = "X-MindFS-E2EE";
export const CLIENT_ID_HEADER = "X-MindFS-Client-ID";
export const PROOF_HEADER = "X-MindFS-Proof";
export const TS_HEADER = "X-MindFS-TS";

type E2EEState = {
  configured: boolean;
  required: boolean;
  nodeId: string;
  secretPresent: boolean;
  unlocked: boolean;
};

type E2EEListener = (state: E2EEState) => void;

type OpenResponse = {
  ok?: boolean;
  node_eph_pk?: string;
  server_nonce?: string;
  server_proof?: string;
  proto_version?: number;
};

type CipherEnvelope = {
  nonce: string;
  ciphertext: string;
};

type SessionContext = {
  transportKey: CryptoKey;
  transportKeyBytes: Uint8Array;
  protocolVersion: number;
  nextClientWSSequence: number;
  lastServerWSSequence: number;
};

type E2EEWSFrame<T> = {
  sequence: number;
  message: T;
};

type ProtectedRequest = {
  init: RequestInit;
  session: SessionContext;
  headers: Headers;
};

type ProtectedResponseBinding = {
  session: SessionContext;
  proof: string;
  expiresAt: number;
};

export type NativeE2EESession = {
  required: boolean;
  nodeId: string;
  clientId: string;
  transportKey: string;
};

class E2EEService {
  private configured = false;
  private required = false;
  private nodeId = "";
  private clientId = "";
  private session: SessionContext | null = null;
  private listeners = new Set<E2EEListener>();
  private openingPromise: Promise<SessionContext> | null = null;
  private lastProofTimestampMs = 0;
  private pendingResponseBindings = new Map<string, ProtectedResponseBinding>();
  private responseBindings = new WeakMap<Response, ProtectedResponseBinding>();

  subscribe(listener: E2EEListener) {
    this.listeners.add(listener);
    listener(this.snapshot());
    return () => {
      this.listeners.delete(listener);
    };
  }

  configure(required: boolean, nodeId: string) {
    const nextRequired = required === true;
    const nextNodeId = String(nodeId || "").trim();
    const changed = !this.configured || this.required !== nextRequired || this.nodeId !== nextNodeId;
    this.configured = true;
    this.required = nextRequired;
    if (this.nodeId !== nextNodeId) {
      this.zeroSession();
      this.nodeId = nextNodeId;
      this.openingPromise = null;
    }
    if (changed) {
      this.emit();
    }
  }

  setClientId(clientId: string) {
    const nextClientId = String(clientId || "").trim();
    if (this.clientId === nextClientId) {
      return;
    }
    this.zeroSession();
    this.clientId = nextClientId;
    this.openingPromise = null;
    this.emit();
  }

  snapshot(): E2EEState {
    return {
      configured: this.configured,
      required: this.required,
      nodeId: this.nodeId,
      secretPresent: this.hasSecret(),
      unlocked: !!this.session,
    };
  }

  hasSecret(): boolean {
    if (!this.nodeId) {
      return false;
    }
    return !!getStoredString(this.secretStorageKey());
  }

  getSecret(): string {
    if (!this.nodeId) {
      return "";
    }
    return String(getStoredString(this.secretStorageKey()) || "").trim();
  }

  setSecret(secret: string) {
    const trimmed = String(secret || "").trim();
    if (!this.nodeId) {
      return;
    }
    if (!trimmed) {
      removeStoredString(this.secretStorageKey());
    } else {
      setStoredString(this.secretStorageKey(), trimmed);
    }
    this.session = null;
    this.openingPromise = null;
    this.emit();
  }

  clearSession(options: { silent?: boolean } = {}) {
    this.zeroSession();
    this.openingPromise = null;
    if (!options.silent) {
      this.emit();
    }
  }

  async ensureSession(): Promise<SessionContext | null> {
    if (!this.required) {
      return null;
    }
    if (this.session) {
      return this.session;
    }
    if (this.openingPromise) {
      return this.openingPromise;
    }
    const secret = this.getSecret();
    if (!secret || !this.nodeId || !this.clientId) {
      this.emit();
      throw new Error("e2ee_secret_missing");
    }
    this.openingPromise = this.open(secret);
    try {
      this.session = await this.openingPromise;
      this.emit();
      return this.session;
    } finally {
      this.openingPromise = null;
    }
  }

  async encryptEnvelope(value: unknown): Promise<CipherEnvelope | null> {
    const session = await this.ensureSession();
    if (!session) return null;
    return encryptJSON(session.transportKey, value);
  }

  async encryptEnvelopeBytes(bytes: Uint8Array): Promise<CipherEnvelope> {
    const session = await this.ensureSession();
    if (!session) {
      throw new Error("e2ee_required");
    }
    return encryptBytes(session.transportKey, bytes);
  }

  async decryptEnvelopeBytes(envelope: CipherEnvelope): Promise<Uint8Array> {
    const session = await this.ensureSession();
    if (!session) {
      throw new Error("e2ee_required");
    }
    return decryptBytes(session.transportKey, envelope);
  }

  async encodeWSMessage(value: unknown): Promise<string> {
    const session = await this.ensureSession();
    if (!session) {
      throw new Error("e2ee_required");
    }
    const frame: E2EEWSFrame<unknown> = {
      sequence: ++session.nextClientWSSequence,
      message: value,
    };
    return JSON.stringify(await encryptJSON(session.transportKey, frame));
  }

  async decodeWSMessage<T>(raw: string): Promise<T> {
    const session = await this.ensureSession();
    if (!session) {
      throw new Error("e2ee_required");
    }
    const envelope = JSON.parse(raw) as CipherEnvelope;
    const frame = await decryptJSON<unknown>(session.transportKey, envelope);
    if (!isE2EEWSFrame(frame)) {
      throw new Error("e2ee_frame_invalid");
    }
    if (frame.sequence <= session.lastServerWSSequence) {
      throw new Error("e2ee_frame_replayed");
    }
    session.lastServerWSSequence = frame.sequence;
    return frame.message as T;
  }

  async wsProofParams(method: string, path: string): Promise<URLSearchParams> {
    const session = await this.ensureSession();
    if (!session) {
      throw new Error("e2ee_required");
    }
    const ts = this.nextProofTimestamp();
    const proofPath = canonicalProofPath(path);
    const proof = await buildRequestProof(session.transportKeyBytes, method, proofPath, ts, this.clientId);
    return new URLSearchParams({
      e2ee_ts: ts,
      e2ee_proof: proof,
    });
  }

  sessionProtectedHeaders(headers?: HeadersInit): Headers {
    const next = new Headers(headers);
    next.set(E2EE_HEADER, "1");
    next.set(CLIENT_ID_HEADER, this.clientId);
    return next;
  }

  async fileProofHeaders(method: string, path: string, headers?: HeadersInit): Promise<Headers> {
    const session = await this.ensureSession();
    if (!session) {
      throw new Error("e2ee_required");
    }
    const ts = this.nextProofTimestamp();
    const proofPath = canonicalProofPath(path);
    const proof = await buildRequestProof(session.transportKeyBytes, method, proofPath, ts, this.clientId);
    const next = new Headers(headers);
    next.set(CLIENT_ID_HEADER, this.clientId);
    next.set(TS_HEADER, ts);
    next.set(PROOF_HEADER, proof);
    this.rememberResponseBinding(session, proof);
    return next;
  }

  private async requestProofHeaders(session: SessionContext, method: string, input: RequestInfo | URL, headers?: HeadersInit): Promise<Headers> {
    const ts = this.nextProofTimestamp();
    const requestURL = input instanceof Request ? input.url : String(input);
    const proofPath = canonicalProofPath(requestURL);
    const proof = await buildRequestProof(session.transportKeyBytes, method, proofPath, ts, this.clientId);
    const next = this.sessionProtectedHeaders(headers);
    next.set(TS_HEADER, ts);
    next.set(PROOF_HEADER, proof);
    this.rememberResponseBinding(session, proof);
    return next;
  }

  private nextProofTimestamp(): string {
    const next = Math.max(Date.now(), this.lastProofTimestampMs + 1);
    this.lastProofTimestampMs = next;
    return new Date(next).toISOString();
  }

  private rememberResponseBinding(session: SessionContext, proof: string) {
    const now = Date.now();
    for (const [value, binding] of this.pendingResponseBindings) {
      if (binding.expiresAt <= now) {
        this.pendingResponseBindings.delete(value);
      }
    }
    this.pendingResponseBindings.set(proof, { session, proof, expiresAt: now + 5 * 60 * 1000 });
  }

  private takeResponseBinding(response: Response): ProtectedResponseBinding {
    const binding = this.responseBindings.get(response);
    this.responseBindings.delete(response);
    if (!binding) {
      throw new Error("e2ee_response_unbound");
    }
    return binding;
  }

  isProtectedJSONResponse(response: Response): boolean {
    return String(response.headers.get(E2EE_HEADER) || "").trim() === "1";
  }

  async parseProtectedJSONResponse<T>(response: Response): Promise<T> {
    if (!this.isProtectedJSONResponse(response)) {
      return response.json() as Promise<T>;
    }
    const binding = this.takeResponseBinding(response);
    const envelope = JSON.parse(await response.text()) as CipherEnvelope;
    if (binding.session.protocolVersion < E2EE_PROTOCOL_VERSION) {
      return decryptJSON<T>(binding.session.transportKey, envelope);
    }
    return decryptJSON<T>(binding.session.transportKey, envelope, new TextEncoder().encode(binding.proof));
  }

  bindProtectedResponse(response: Response, headers?: HeadersInit): Response {
    const proof = String(new Headers(headers).get(PROOF_HEADER) || "").trim();
    if (!proof) {
      return response;
    }
    const binding = this.pendingResponseBindings.get(proof);
    this.pendingResponseBindings.delete(proof);
    if (binding && this.isProtectedJSONResponse(response)) {
      this.responseBindings.set(response, binding);
    }
    return response;
  }

  async decryptBoundResponseBytes(response: Response, envelope: CipherEnvelope, context = ""): Promise<Uint8Array> {
    const binding = this.takeResponseBinding(response);
    if (binding.session.protocolVersion < E2EE_PROTOCOL_VERSION) {
      return decryptBytes(binding.session.transportKey, envelope);
    }
    const aad = context ? `${binding.proof}\x1f${context}` : binding.proof;
    return decryptBytes(binding.session.transportKey, envelope, new TextEncoder().encode(aad));
  }

  async protectedFetch(input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> {
    if (!this.required) {
      return fetch(input, init);
    }
    let request = await this.buildProtectedRequest(input, init);
    let response = await fetch(input, request.init);
    this.bindProtectedResponse(response, request.headers);
    if (response.status === 401) {
      const payload = (await response.clone().json().catch(() => ({}))) as { error?: string };
      if (await this.recoverProtectedSession(String(payload.error || ""), request.session)) {
        request = await this.buildProtectedRequest(input, init);
        response = await fetch(input, request.init);
        this.bindProtectedResponse(response, request.headers);
      }
    }
    return response;
  }

  private async buildProtectedRequest(input: RequestInfo | URL, init: RequestInit): Promise<ProtectedRequest> {
    const session = await this.ensureSession();
    if (!session) {
      throw new Error("e2ee_required");
    }
    const method = String(init.method || (input instanceof Request ? input.method : "GET")).toUpperCase();
    const headers = await this.requestProofHeaders(session, method, input, init.headers || (input instanceof Request ? input.headers : undefined));
    const next: RequestInit = { ...init, method, headers };
    if (init.body !== undefined && init.body !== null && method !== "GET" && method !== "HEAD") {
      const plaintext = protectedBodyText(init.body);
      const envelope = await encryptBytes(session.transportKey, new TextEncoder().encode(plaintext));
      next.body = JSON.stringify(envelope);
      headers.set("Content-Type", "application/json");
    }
    return { init: next, session, headers };
  }

  private async recoverProtectedSession(code: string, failedSession: SessionContext): Promise<boolean> {
    const normalized = String(code || "").trim();
    if (!normalized) {
      return false;
    }
    if (
      this.session &&
      this.session !== failedSession &&
      (
        normalized === "e2ee_session_missing" ||
        normalized === "e2ee_session_expired" ||
        normalized === "e2ee_proof_invalid"
      )
    ) {
      return true;
    }
    if (normalized === "e2ee_session_missing" || normalized === "e2ee_session_expired") {
      this.clearSession({ silent: this.hasSecret() });
      await this.ensureSession();
      return true;
    }
    if (normalized === "e2ee_proof_invalid") {
      this.clearSession({ silent: this.hasSecret() });
      return false;
    }
    return false;
  }

  async protectedJSON<T>(input: RequestInfo | URL, init: RequestInit = {}): Promise<T> {
    const response = await this.protectedFetch(input, init);
    const payload = await this.parseProtectedJSONResponse<{ error?: string; message?: string } & T>(response);
    if (!response.ok) {
      throw new Error(String(payload.message || payload.error || `request failed: ${response.status}`));
    }
    return payload as T;
  }

  isRequired(): boolean {
    return this.required;
  }

  currentClientId(): string {
    return this.clientId;
  }

  nativeSession(): NativeE2EESession {
    return {
      required: this.required,
      nodeId: this.nodeId,
      clientId: this.required && this.session ? this.clientId : "",
      transportKey: this.required && this.session ? encodeBase64(this.session.transportKeyBytes) : "",
    };
  }

  handleServerError(code: string): boolean {
    const normalized = String(code || "").trim();
    if (!normalized) {
      return false;
    }
    if (
      normalized === "e2ee_session_missing" ||
      normalized === "e2ee_session_expired"
    ) {
      this.clearSession({ silent: this.hasSecret() });
      return true;
    }
    if (normalized === "e2ee_proof_invalid") {
      this.clearSession({ silent: this.hasSecret() });
      return false;
    }
    return false;
  }

  private async open(secret: string): Promise<SessionContext> {
    if (!globalThis.isSecureContext) {
      throw new Error("e2ee_secure_context_required");
    }
    if (!globalThis.crypto?.subtle) {
      throw new Error("e2ee_webcrypto_unavailable");
    }
    const clientKeys = await crypto.subtle.generateKey(
      {
        name: "ECDH",
        namedCurve: "P-256",
      },
      true,
      ["deriveBits"],
    );
    const rawPublicKey = new Uint8Array(
      await crypto.subtle.exportKey("raw", clientKeys.publicKey),
    );
    const clientNonceBytes = crypto.getRandomValues(new Uint8Array(16));
    const clientEphPK = encodeBase64(rawPublicKey);
    const clientNonce = encodeBase64(clientNonceBytes);
    const proof = await buildOpenProof(secret, this.nodeId, clientEphPK, clientNonce);

    const response = await fetch(appURL("/api/e2ee/open"), {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        client_id: this.clientId,
        node_id: this.nodeId,
        client_eph_pk: clientEphPK,
        client_nonce: clientNonce,
        proof,
        proto_version: E2EE_PROTOCOL_VERSION,
      }),
    });
    const payload = (await response.json().catch(() => ({}))) as OpenResponse & {
      error?: string;
    };
    if (!response.ok) {
      const code = String(payload?.error || `e2ee_open_failed_${response.status}`);
      this.handleServerError(code);
      throw new Error(code);
    }
    const protocolVersion = Number(payload.proto_version);
    if (protocolVersion !== E2EE_PROTOCOL_VERSION) {
      throw new Error("e2ee_protocol_unsupported");
    }
    const nodeEphPK = String(payload.node_eph_pk || "").trim();
    const serverNonce = String(payload.server_nonce || "").trim();
    const serverProof = String(payload.server_proof || "").trim();
    if (!nodeEphPK || !serverNonce || !serverProof) {
      throw new Error("e2ee_open_invalid_response");
    }
    const expectedServerProof = await buildAcceptProof(
      secret,
      this.nodeId,
      clientEphPK,
      nodeEphPK,
      clientNonce,
      serverNonce,
      protocolVersion,
    );
    if (expectedServerProof !== serverProof) {
      throw new Error("e2ee_proof_invalid");
    }
    const nodePub = await crypto.subtle.importKey(
      "raw",
      toArrayBuffer(decodeBase64(nodeEphPK)),
      {
        name: "ECDH",
        namedCurve: "P-256",
      },
      false,
      [],
    );
    const sharedSecret = new Uint8Array(
      await crypto.subtle.deriveBits(
        { name: "ECDH", public: nodePub },
        clientKeys.privateKey,
        256,
      ),
    );
    const sessionInfoParts = [this.nodeId, clientEphPK, nodeEphPK, clientNonce, serverNonce, "v2"];
    const sessionMaster = await hkdfBytes(
      sharedSecret,
      await sha256Bytes(secret),
      await sha256Bytes(sessionInfoParts.join("\x1f")),
      32,
    );
    const transportKeyBytes = await hkdfBytes(sessionMaster, null, new TextEncoder().encode("transport"), 32);
    const transportKey = await importAesKey(transportKeyBytes);
    return {
      transportKey,
      transportKeyBytes,
      protocolVersion,
      nextClientWSSequence: 0,
      lastServerWSSequence: 0,
    };
  }

  private secretStorageKey(): string {
    return `${SECRET_STORAGE_PREFIX}${this.nodeId}`;
  }

  private zeroSession() {
    if (this.session) {
      this.session.transportKeyBytes.fill(0);
      this.session = null;
    }
  }

  private emit() {
    const state = this.snapshot();
    for (const listener of this.listeners) {
      listener(state);
    }
  }
}

function isE2EEWSFrame(value: unknown): value is E2EEWSFrame<unknown> {
  if (!value || typeof value !== "object" || !("sequence" in value) || !("message" in value)) {
    return false;
  }
  const sequence = (value as E2EEWSFrame<unknown>).sequence;
  return Number.isSafeInteger(sequence) && sequence > 0;
}

async function buildOpenProof(secret: string, nodeId: string, clientEphPK: string, clientNonce: string): Promise<string> {
  return buildHmacProof(secret, "mindfs-e2ee-open", [nodeId, clientEphPK, clientNonce]);
}

async function buildAcceptProof(secret: string, nodeId: string, clientEphPK: string, nodeEphPK: string, clientNonce: string, serverNonce: string, protocolVersion: number): Promise<string> {
  const label = protocolVersion === E2EE_PROTOCOL_VERSION ? "mindfs-e2ee-accept-v2" : "mindfs-e2ee-accept";
  return buildHmacProof(secret, label, [nodeId, clientEphPK, nodeEphPK, clientNonce, serverNonce]);
}

async function buildHmacProof(secret: string, label: string, parts: string[]): Promise<string> {
    const secretKey = await crypto.subtle.importKey(
      "raw",
      toArrayBuffer(new TextEncoder().encode(secret)),
      { name: "HMAC", hash: "SHA-256" },
      false,
      ["sign"],
  );
  const digest = await sha256Bytes([label, ...parts].join("\x1f"));
  const signature = await crypto.subtle.sign("HMAC", secretKey, toArrayBuffer(digest));
  return encodeBase64(new Uint8Array(signature));
}

async function encryptJSON(key: CryptoKey, value: unknown): Promise<CipherEnvelope> {
  return encryptBytes(key, new TextEncoder().encode(JSON.stringify(value)));
}

async function decryptJSON<T>(key: CryptoKey, envelope: CipherEnvelope, aad?: Uint8Array): Promise<T> {
  const plaintext = await decryptBytes(key, envelope, aad);
  return JSON.parse(new TextDecoder().decode(plaintext)) as T;
}

async function encryptBytes(key: CryptoKey, bytes: Uint8Array): Promise<CipherEnvelope> {
  const nonce = crypto.getRandomValues(new Uint8Array(12));
  const ciphertext = await crypto.subtle.encrypt(
    { name: "AES-GCM", iv: nonce },
    key,
    toArrayBuffer(bytes),
  );
  return {
    nonce: encodeBase64(nonce),
    ciphertext: encodeBase64(new Uint8Array(ciphertext)),
  };
}

async function decryptBytes(key: CryptoKey, envelope: CipherEnvelope, aad?: Uint8Array): Promise<Uint8Array> {
  const plaintext = await crypto.subtle.decrypt(
    aad
      ? { name: "AES-GCM", iv: toArrayBuffer(decodeBase64(envelope.nonce)), additionalData: toArrayBuffer(aad) }
      : { name: "AES-GCM", iv: toArrayBuffer(decodeBase64(envelope.nonce)) },
    key,
    toArrayBuffer(decodeBase64(envelope.ciphertext)),
  );
  return new Uint8Array(plaintext);
}

async function importAesKey(raw: Uint8Array): Promise<CryptoKey> {
  return crypto.subtle.importKey("raw", toArrayBuffer(raw), { name: "AES-GCM" }, false, ["encrypt", "decrypt"]);
}

async function buildRequestProof(key: Uint8Array, method: string, path: string, ts: string, clientId: string): Promise<string> {
  const hmacKey = await crypto.subtle.importKey(
    "raw",
    toArrayBuffer(key),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const digest = await sha256Bytes(["mindfs-request-proof", method, path, ts, clientId].join("\x1f"));
  const signature = await crypto.subtle.sign("HMAC", hmacKey, toArrayBuffer(digest));
  return encodeBase64(new Uint8Array(signature));
}

async function hkdfBytes(secret: Uint8Array, salt: Uint8Array | ArrayBuffer | null, info: Uint8Array | ArrayBuffer, length: number): Promise<Uint8Array> {
  const baseKey = await crypto.subtle.importKey("raw", toArrayBuffer(secret), "HKDF", false, ["deriveBits"]);
  const bits = await crypto.subtle.deriveBits(
    {
      name: "HKDF",
      hash: "SHA-256",
      salt: salt ? toArrayBuffer(toUint8Array(salt)) : new Uint8Array(),
      info: toArrayBuffer(toUint8Array(info)),
    },
    baseKey,
    length * 8,
  );
  return new Uint8Array(bits);
}

async function sha256Bytes(value: string): Promise<Uint8Array>;
async function sha256Bytes(value: Uint8Array): Promise<Uint8Array>;
async function sha256Bytes(value: string | Uint8Array): Promise<Uint8Array> {
  const bytes = typeof value === "string" ? new TextEncoder().encode(value) : value;
  return new Uint8Array(await crypto.subtle.digest("SHA-256", toArrayBuffer(bytes)));
}

function encodeBase64(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary);
}

function decodeBase64(value: string): Uint8Array {
  const binary = atob(value);
  const out = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) {
    out[i] = binary.charCodeAt(i);
  }
  return out;
}

function toUint8Array(value: Uint8Array | ArrayBuffer): Uint8Array {
  return value instanceof Uint8Array ? value : new Uint8Array(value);
}

function toArrayBuffer(value: Uint8Array): ArrayBuffer {
  return value.buffer.slice(
    value.byteOffset,
    value.byteOffset + value.byteLength,
  ) as ArrayBuffer;
}

function canonicalProofPath(path: string): string {
  const raw = String(path || "").trim();
  if (!raw) {
    return "";
  }
  const target = new URL(raw, typeof window !== "undefined" ? window.location.origin : "http://localhost");
  const pathname = target.pathname.replace(/^\/n\/[^/]+/, "") || "/";
  return target.search ? `${pathname}${target.search}` : pathname;
}

function protectedBodyText(body: BodyInit): string {
  if (typeof body === "string") {
    return body;
  }
  if (body instanceof URLSearchParams) {
    return body.toString();
  }
  throw new Error("e2ee_unsupported_body");
}

export const e2eeService = new E2EEService();
export type { CipherEnvelope, E2EEState };
