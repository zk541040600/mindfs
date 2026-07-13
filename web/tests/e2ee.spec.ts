import { expect, test } from "@playwright/test";

test("rejects replayed encrypted websocket frames", async ({ page }) => {
  await page.route("**/api/**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ e2ee_required: false }),
    });
  });
  await page.goto("/");

  const result = await page.evaluate(async () => {
    const { e2eeService } = await import("/src/services/e2ee.ts");
    const service = e2eeService as any;
    const keyBytes = crypto.getRandomValues(new Uint8Array(32));
    const transportKey = await crypto.subtle.importKey(
      "raw",
      keyBytes,
      { name: "AES-GCM" },
      false,
      ["encrypt", "decrypt"],
    );
    const toBase64 = (bytes: Uint8Array) => btoa(String.fromCharCode(...bytes));
    const toArrayBuffer = (bytes: Uint8Array) => bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength);
    const fromBase64 = (value: string) => Uint8Array.from(atob(value), (char) => char.charCodeAt(0));
    const decryptFrame = async (raw: string) => {
      const envelope = JSON.parse(raw) as { nonce: string; ciphertext: string };
      const plaintext = await crypto.subtle.decrypt(
        { name: "AES-GCM", iv: toArrayBuffer(fromBase64(envelope.nonce)) },
        transportKey,
        toArrayBuffer(fromBase64(envelope.ciphertext)),
      );
      return JSON.parse(new TextDecoder().decode(plaintext)) as { sequence: number; message: unknown };
    };
    const encryptFrame = async (frame: { sequence: number; message: unknown }) => {
      const nonce = crypto.getRandomValues(new Uint8Array(12));
      const ciphertext = await crypto.subtle.encrypt(
        { name: "AES-GCM", iv: nonce },
        transportKey,
        new TextEncoder().encode(JSON.stringify(frame)),
      );
      return JSON.stringify({
        nonce: toBase64(nonce),
        ciphertext: toBase64(new Uint8Array(ciphertext)),
      });
    };

    service.required = true;
    service.session = {
      transportKey,
      transportKeyBytes: keyBytes,
      protocolVersion: 2,
      nextClientWSSequence: 0,
      lastServerWSSequence: 0,
    };
    const firstOutbound = await decryptFrame(await service.encodeWSMessage({ type: "first" }));
    const secondOutbound = await decryptFrame(await service.encodeWSMessage({ type: "second" }));
    const inbound = await encryptFrame({ sequence: 1, message: { type: "pong" } });
    const firstInbound = await service.decodeWSMessage(inbound);
    let replayCode = "";
    try {
      await service.decodeWSMessage(inbound);
    } catch (err) {
      replayCode = err instanceof Error ? err.message : String(err);
    }
    service.required = false;
    service.clearSession({ silent: true });
    return {
      outboundSequences: [firstOutbound.sequence, secondOutbound.sequence],
      firstInbound,
      replayCode,
    };
  });

  expect(result.outboundSequences).toEqual([1, 2]);
  expect(result.firstInbound).toEqual({ type: "pong" });
  expect(result.replayCode).toBe("e2ee_frame_replayed");
});

test("binds protected HTTP responses to the originating request proof", async ({ page }) => {
  await page.route("**/api/**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ e2ee_required: false }),
    });
  });
  await page.goto("/");

  const result = await page.evaluate(async () => {
    const { e2eeService } = await import("/src/services/e2ee.ts");
    const service = e2eeService as any;
    const keyBytes = crypto.getRandomValues(new Uint8Array(32));
    const transportKey = await crypto.subtle.importKey(
      "raw",
      keyBytes,
      { name: "AES-GCM" },
      false,
      ["encrypt", "decrypt"],
    );
    const toBase64 = (bytes: Uint8Array) => btoa(String.fromCharCode(...bytes));
    const toArrayBuffer = (bytes: Uint8Array) => bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength);
    const encryptJSON = async (value: unknown, proof: string) => {
      const nonce = crypto.getRandomValues(new Uint8Array(12));
      const ciphertext = await crypto.subtle.encrypt(
        {
          name: "AES-GCM",
          iv: nonce,
          additionalData: new TextEncoder().encode(proof),
        },
        transportKey,
        new TextEncoder().encode(JSON.stringify(value)),
      );
      return JSON.stringify({
        nonce: toBase64(nonce),
        ciphertext: toBase64(new Uint8Array(ciphertext)),
      });
    };
    const session = {
      transportKey,
      transportKeyBytes: keyBytes,
      protocolVersion: 2,
      nextClientWSSequence: 0,
      lastServerWSSequence: 0,
    };
    const firstProof = "request-proof-a";
    const secondProof = "request-proof-b";
    const encrypted = await encryptJSON({ status: "ok" }, firstProof);
    const responseHeaders = { "X-MindFS-E2EE": "1" };
    const expiresAt = Date.now() + 60_000;

    service.required = true;
    service.session = session;
    service.pendingResponseBindings.set(firstProof, { session, proof: firstProof, expiresAt });
    const firstResponse = new Response(encrypted, { headers: responseHeaders });
    service.bindProtectedResponse(firstResponse, new Headers({ "X-MindFS-Proof": firstProof }));
    const firstPayload = await service.parseProtectedJSONResponse(firstResponse);

    service.pendingResponseBindings.set(secondProof, { session, proof: secondProof, expiresAt });
    const replayedResponse = new Response(encrypted, { headers: responseHeaders });
    service.bindProtectedResponse(replayedResponse, new Headers({ "X-MindFS-Proof": secondProof }));
    let replayRejected = false;
    try {
      await service.parseProtectedJSONResponse(replayedResponse);
    } catch {
      replayRejected = true;
    }

    let unboundCode = "";
    try {
      await service.parseProtectedJSONResponse(new Response(encrypted, { headers: responseHeaders }));
    } catch (err) {
      unboundCode = err instanceof Error ? err.message : String(err);
    }
    service.required = false;
    service.clearSession({ silent: true });
    return { firstPayload, replayRejected, unboundCode };
  });

  expect(result.firstPayload).toEqual({ status: "ok" });
  expect(result.replayRejected).toBe(true);
  expect(result.unboundCode).toBe("e2ee_response_unbound");
});
