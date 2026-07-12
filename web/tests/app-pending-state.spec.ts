import { expect, test, type Page, type Route, type WebSocketRoute } from "@playwright/test";

const rootId = "ge";
const sessionKey = "s1";

const pendingSession = {
  key: sessionKey,
  session_key: sessionKey,
  root_id: rootId,
  type: "chat",
  agent: "pi",
  model: "cch-responses/gpt-5.5",
  mode: "xhigh",
  name: "Pending turn",
  created_at: "2026-06-15T05:00:00.000Z",
  updated_at: "2026-06-15T05:00:01.000Z",
  pending: true,
  exchanges: [
    {
      role: "user",
      content: "run delayed tool",
      timestamp: "2026-06-15T05:00:00.000Z",
    },
    {
      role: "assistant",
      content: "preparing",
      timestamp: "2026-06-15T05:00:01.000Z",
    },
  ],
};

async function fulfillJSON(route: Route, body: unknown, status = 200) {
  await route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
}

async function openPendingApp(
  page: Page,
  replyingState: { current: boolean },
  onWSMessage?: (message: string) => void,
  serverPendingState: { current: boolean } = { current: true },
) {
  let socket: WebSocketRoute | null = null;

  await page.routeWebSocket((url) => url.pathname === "/ws", (ws) => {
    socket = ws;
    ws.onMessage((message) => {
      onWSMessage?.(String(message));
    });
  });

  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;

    if (path === "/api/relay/status") {
      await fulfillJSON(route, { e2ee_required: false });
      return;
    }
    if (path === "/api/relay/tips") {
      await fulfillJSON(route, { tips: [] });
      return;
    }
    if (path === "/api/dirs") {
      await fulfillJSON(route, [
        {
          id: rootId,
          display_name: "ge",
          root_path: "/tmp/ge",
          is_git_repo: true,
        },
      ]);
      return;
    }
    if (path === "/api/agents") {
      await fulfillJSON(route, {
        agents: [
          {
            name: "pi",
            installed: true,
            available: true,
            current_model_id: "cch-responses/gpt-5.5",
            default_model_id: "cch-responses/gpt-5.5",
            current_mode_id: "xhigh",
            models: [{ id: "cch-responses/gpt-5.5", name: "GPT 5.5" }],
            modes: [{ id: "xhigh", name: "xhigh" }],
          },
        ],
        shells: [],
      });
      return;
    }
    if (path === "/api/replying-sessions") {
      await fulfillJSON(route, {
        sessions: replyingState.current
          ? [
              {
                root_id: rootId,
                session_key: sessionKey,
                status: "replying",
              },
            ]
          : [],
      });
      return;
    }
    if (path === "/api/sessions") {
      await fulfillJSON(route, [
        { ...pendingSession, pending: serverPendingState.current },
      ]);
      return;
    }
    if (path === `/api/sessions/${sessionKey}`) {
      await fulfillJSON(route, {
        ...pendingSession,
        pending: serverPendingState.current,
      });
      return;
    }
    if (path === `/api/sessions/${sessionKey}/related-files`) {
      await fulfillJSON(route, []);
      return;
    }
    if (path === "/api/tree") {
      await fulfillJSON(route, { path: ".", items: [] });
      return;
    }
    if (path === "/api/git/status") {
      await fulfillJSON(route, { available: false, dirty_count: 0, items: [] });
      return;
    }
    if (path === "/api/git/history") {
      await fulfillJSON(route, { items: [], has_more: false });
      return;
    }

    await fulfillJSON(route, {});
  });

  await page.goto(`/?root=${rootId}&session=${sessionKey}`);
  await expect.poll(() => socket !== null).toBe(true);

  return {
    get socket() {
      return socket;
    },
  };
}

test("keeps pending after message_done until session.done arrives", async ({ page }) => {
  let socket: WebSocketRoute | null = null;
  let replying = true;

  await page.routeWebSocket((url) => url.pathname === "/ws", (ws) => {
    socket = ws;
    ws.onMessage(() => {});
  });

  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;

    if (path === "/api/relay/status") {
      await fulfillJSON(route, { e2ee_required: false });
      return;
    }
    if (path === "/api/relay/tips") {
      await fulfillJSON(route, { tips: [] });
      return;
    }
    if (path === "/api/dirs") {
      await fulfillJSON(route, [
        {
          id: rootId,
          display_name: "ge",
          root_path: "/tmp/ge",
          is_git_repo: true,
        },
      ]);
      return;
    }
    if (path === "/api/agents") {
      await fulfillJSON(route, {
        agents: [
          {
            name: "pi",
            installed: true,
            available: true,
            current_model_id: "cch-responses/gpt-5.5",
            default_model_id: "cch-responses/gpt-5.5",
            current_mode_id: "xhigh",
            models: [{ id: "cch-responses/gpt-5.5", name: "GPT 5.5" }],
            modes: [{ id: "xhigh", name: "xhigh" }],
          },
        ],
        shells: [],
      });
      return;
    }
    if (path === "/api/replying-sessions") {
      await fulfillJSON(route, {
        sessions: replying
          ? [
              {
                root_id: rootId,
                session_key: sessionKey,
                status: "replying",
              },
            ]
          : [],
      });
      return;
    }
    if (path === "/api/sessions") {
      await fulfillJSON(route, [pendingSession]);
      return;
    }
    if (path === `/api/sessions/${sessionKey}`) {
      await fulfillJSON(route, pendingSession);
      return;
    }
    if (path === `/api/sessions/${sessionKey}/related-files`) {
      await fulfillJSON(route, []);
      return;
    }
    if (path === "/api/tree") {
      await fulfillJSON(route, { path: ".", items: [] });
      return;
    }
    if (path === "/api/git/status") {
      await fulfillJSON(route, { available: false, dirty_count: 0, items: [] });
      return;
    }
    if (path === "/api/git/history") {
      await fulfillJSON(route, { items: [], has_more: false });
      return;
    }

    await fulfillJSON(route, {});
  });

  await page.goto(`/?root=${rootId}&session=${sessionKey}`);
  await expect(page.getByText("已发送，等待响应...")).toBeVisible();
  await expect(page.getByText("左滑蓝环开始新会话...")).toHaveCount(0);

  await expect.poll(() => socket !== null).toBe(true);
  socket?.send(
    JSON.stringify({
      type: "session.stream",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
        event: {
          type: "message_done",
          data: {
            contextWindow: { totalTokens: 12, modelContextWindow: 100 },
          },
        },
      },
    }),
  );

  await page.waitForTimeout(150);
  await expect(page.getByText("已发送，等待响应...")).toBeVisible();
  await expect(page.getByText("左滑蓝环开始新会话...")).toHaveCount(0);

  replying = false;
  socket?.send(
    JSON.stringify({
      id: "req-1",
      type: "session.done",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
      },
    }),
  );

  await expect(page.getByText("已发送，等待响应...")).toHaveCount(0);
  await expect(page.getByText("左滑蓝环开始新会话...")).toBeVisible();
});

test("ignores late stream events after cancelling a pending turn", async ({ page }) => {
  const replying = { current: true };
  let cancelSeen = false;
  const app = await openPendingApp(page, replying, (message) => {
    try {
      const parsed = JSON.parse(message);
      if (parsed?.type === "session.cancel") {
        cancelSeen = true;
      }
    } catch {
      // Ignore non-JSON websocket traffic in the harness.
    }
  });

  await expect(page.getByText("已发送，等待响应...")).toBeVisible();
  app.socket?.send(
    JSON.stringify({
      type: "session.stream",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
        event: {
          type: "message_chunk",
          data: { content: "running before cancel" },
        },
      },
    }),
  );
  await expect
    .poll(() =>
      page.evaluate(async (key) => {
        const { sessionService } = await import("/src/services/session.ts");
        return sessionService.isSessionStreaming(key);
      }, sessionKey),
    )
    .toBe(true);
  await page.keyboard.press("Escape");
  await expect.poll(() => cancelSeen).toBe(true);
  await expect(page.getByText("已发送，等待响应...")).toHaveCount(0);
  await expect(page.getByText("左滑蓝环开始新会话...")).toBeVisible();

  app.socket?.send(
    JSON.stringify({
      id: "cancelled-turn",
      type: "session.cancelled",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
        request_id: "cancelled-turn",
      },
    }),
  );
  await expect
    .poll(() =>
      page.evaluate(async (key) => {
        const { sessionService } = await import("/src/services/session.ts");
        return sessionService.isSessionStreaming(key);
      }, sessionKey),
    )
    .toBe(false);

  app.socket?.send(
    JSON.stringify({
      type: "session.stream",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
        event: {
          type: "message_chunk",
          data: { content: "late chunk after cancel" },
        },
      },
    }),
  );

  await page.waitForTimeout(150);
  await expect(page.getByText("已发送，等待响应...")).toHaveCount(0);
  await expect(page.getByText("左滑蓝环开始新会话...")).toBeVisible();
  await expect(page.getByText("late chunk after cancel")).toHaveCount(0);
});

test("keeps a newer active stream when stale callbacks and terminals arrive", async ({ page }) => {
  const replying = { current: true };
  let cancelSeen = false;
  let newRequestId = "";
  const app = await openPendingApp(page, replying, (message) => {
    try {
      const parsed = JSON.parse(message);
      if (parsed?.type === "session.cancel") {
        cancelSeen = true;
      }
      if (parsed?.type === "session.message" && typeof parsed.id === "string") {
        newRequestId = parsed.id;
      }
    } catch {
      // Ignore non-JSON websocket traffic in the harness.
    }
  });

  await expect(page.getByText("已发送，等待响应...")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect.poll(() => cancelSeen).toBe(true);
  await expect(page.getByText("已发送，等待响应...")).toHaveCount(0);

  const editor = page.locator(".token-editor-input").first();
  await editor.click();
  await editor.fill("new turn after cancel");
  await page.keyboard.press("Enter");
  await expect.poll(() => newRequestId).not.toBe("");
  app.socket?.send(
    JSON.stringify({
      id: newRequestId,
      type: "session.accepted",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
        request_id: newRequestId,
      },
    }),
  );
  await expect(page.getByText("已发送，等待响应...")).toBeVisible();
  app.socket?.send(
    JSON.stringify({
      type: "session.stream",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
        request_id: newRequestId,
        event: {
          type: "message_chunk",
          data: { content: "new turn is streaming" },
        },
      },
    }),
  );
  await expect
    .poll(() =>
      page.evaluate(async (key) => {
        const { sessionService } = await import("/src/services/session.ts");
        return sessionService.isSessionStreaming(key);
      }, sessionKey),
    )
    .toBe(true);

  app.socket?.send(
    JSON.stringify({
      type: "session.stream",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
        request_id: "old-request",
        event: {
          type: "message_chunk",
          data: { content: "late old turn chunk" },
        },
      },
    }),
  );
  await page.waitForTimeout(150);
  await expect(page.getByText("late old turn chunk")).toHaveCount(0);
  await expect(page.getByText("new turn is streaming")).toBeVisible();

  app.socket?.send(
    JSON.stringify({
      id: "cancel-old",
      type: "session.cancelled",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
        request_id: "old-request",
        stale: true,
      },
    }),
  );

  await page.waitForTimeout(150);
  await expect(page.getByText("new turn is streaming")).toBeVisible();
  await expect(page.getByText("正在生成...")).toBeVisible();
  await expect
    .poll(() =>
      page.evaluate(async (key) => {
        const { sessionService } = await import("/src/services/session.ts");
        return sessionService.isSessionStreaming(key);
      }, sessionKey),
    )
    .toBe(true);

  app.socket?.send(
    JSON.stringify({
      id: "done-old",
      type: "session.done",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
        request_id: "old-request",
      },
    }),
  );

  await page.waitForTimeout(150);
  await expect(page.getByText("new turn is streaming")).toBeVisible();
  await expect(page.getByText("正在生成...")).toBeVisible();
  await expect
    .poll(() =>
      page.evaluate(async (key) => {
        const { sessionService } = await import("/src/services/session.ts");
        return sessionService.isSessionStreaming(key);
      }, sessionKey),
    )
    .toBe(true);

  replying.current = false;
  app.socket?.send(
    JSON.stringify({
      id: newRequestId,
      type: "session.done",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
        request_id: newRequestId,
      },
    }),
  );
  await expect(page.getByText("已发送，等待响应...")).toHaveCount(0);
  await expect(page.getByText("正在生成...")).toHaveCount(0);
  await expect
    .poll(() =>
      page.evaluate(async (key) => {
        const { sessionService } = await import("/src/services/session.ts");
        return sessionService.isSessionStreaming(key);
      }, sessionKey),
    )
    .toBe(false);
});

test("ignores request-less stale cancel acknowledgement after a new turn starts", async ({ page }) => {
  const replying = { current: true };
  let cancelSeen = false;
  let newRequestId = "";
  const app = await openPendingApp(page, replying, (message) => {
    try {
      const parsed = JSON.parse(message);
      if (parsed?.type === "session.cancel") {
        cancelSeen = true;
      }
      if (parsed?.type === "session.message" && typeof parsed.id === "string") {
        newRequestId = parsed.id;
      }
    } catch {
      // Ignore non-JSON websocket traffic in the harness.
    }
  });

  await expect(page.getByText("已发送，等待响应...")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect.poll(() => cancelSeen).toBe(true);
  await expect(page.getByText("已发送，等待响应...")).toHaveCount(0);

  const editor = page.locator(".token-editor-input").first();
  await editor.click();
  await editor.fill("new turn after request-less cancel");
  await page.keyboard.press("Enter");
  await expect.poll(() => newRequestId).not.toBe("");
  app.socket?.send(
    JSON.stringify({
      id: newRequestId,
      type: "session.accepted",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
        request_id: newRequestId,
      },
    }),
  );
  await expect(page.getByText("已发送，等待响应...")).toBeVisible();

  app.socket?.send(
    JSON.stringify({
      id: "cancel-old-no-request",
      type: "session.cancelled",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
      },
    }),
  );

  await page.waitForTimeout(150);
  await expect(page.getByText("已发送，等待响应...")).toBeVisible();

  replying.current = false;
  app.socket?.send(
    JSON.stringify({
      id: newRequestId,
      type: "session.done",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
        request_id: newRequestId,
      },
    }),
  );
  await expect(page.getByText("已发送，等待响应...")).toHaveCount(0);
});

test("ignores request-less stale done acknowledgement after a new turn starts", async ({ page }) => {
  const replying = { current: true };
  let cancelSeen = false;
  let newRequestId = "";
  const app = await openPendingApp(page, replying, (message) => {
    try {
      const parsed = JSON.parse(message);
      if (parsed?.type === "session.cancel") {
        cancelSeen = true;
      }
      if (parsed?.type === "session.message" && typeof parsed.id === "string") {
        newRequestId = parsed.id;
      }
    } catch {
      // Ignore non-JSON websocket traffic in the harness.
    }
  });

  await expect(page.getByText("已发送，等待响应...")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect.poll(() => cancelSeen).toBe(true);
  await expect(page.getByText("已发送，等待响应...")).toHaveCount(0);

  const editor = page.locator(".token-editor-input").first();
  await editor.click();
  await editor.fill("new turn after request-less done");
  await page.keyboard.press("Enter");
  await expect.poll(() => newRequestId).not.toBe("");
  app.socket?.send(
    JSON.stringify({
      id: newRequestId,
      type: "session.accepted",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
        request_id: newRequestId,
      },
    }),
  );
  await expect(page.getByText("已发送，等待响应...")).toBeVisible();

  app.socket?.send(
    JSON.stringify({
      id: "done-old-no-request",
      type: "session.done",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
      },
    }),
  );

  await page.waitForTimeout(150);
  await expect(page.getByText("已发送，等待响应...")).toBeVisible();

  replying.current = false;
  app.socket?.send(
    JSON.stringify({
      id: newRequestId,
      type: "session.done",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
        request_id: newRequestId,
      },
    }),
  );
  await expect(page.getByText("已发送，等待响应...")).toHaveCount(0);
});

test("allows a resumed stream after cancel tombstone timeout without a terminal event", async ({ page }) => {
  await page.addInitScript(() => {
    (window as any).__mindfsCancelRequestTombstoneTTLMS = 50;
  });
  const replying = { current: true };
  let cancelSeen = false;
  const app = await openPendingApp(page, replying, (message) => {
    try {
      const parsed = JSON.parse(message);
      if (parsed?.type === "session.cancel") {
        cancelSeen = true;
      }
    } catch {
      // Ignore non-JSON websocket traffic in the harness.
    }
  });

  await expect(page.getByText("已发送，等待响应...")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect.poll(() => cancelSeen).toBe(true);
  await expect(page.getByText("已发送，等待响应...")).toHaveCount(0);

  await page.waitForTimeout(90);
  app.socket?.send(
    JSON.stringify({
      id: "resumed-stream",
      type: "session.stream",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
        event: {
          type: "message_chunk",
          data: { content: "resumed after reconnect" },
        },
      },
    }),
  );

  await expect(page.getByText("resumed after reconnect")).toBeVisible();
  await expect(page.getByText("正在生成...")).toBeVisible();
});

test("actively probes a stale open websocket while the page is visible", async ({ page }) => {
  const replying = { current: false };
  const serverPending = { current: false };
  const sentMessages: string[] = [];
  const app = await openPendingApp(
    page,
    replying,
    (message) => sentMessages.push(message),
    serverPending,
  );

  await page.evaluate(async () => {
    const { sessionService } = await import("/src/services/session.ts");
    const service = sessionService as any;
    service.activeProbeId = null;
    service.lastSocketActivityAt = Date.now() - service.probeIntervalMs - 1;
    service.ensureReconnectLoop();
  });

  await expect
    .poll(() =>
      sentMessages.some((message) => {
        try {
          return JSON.parse(message)?.type === "ping";
        } catch {
          return false;
        }
      }),
    )
    .toBe(true);

  const ping = sentMessages
    .map((message) => {
      try {
        return JSON.parse(message);
      } catch {
        return null;
      }
    })
    .find((message) => message?.type === "ping");
  app.socket?.send(
    JSON.stringify({ id: ping?.id, type: "pong", payload: {} }),
  );
});

test("clears stale pending when restarted server reports no active turn", async ({ page }) => {
  const replying = { current: true };
  const serverPending = { current: true };
  const app = await openPendingApp(page, replying, undefined, serverPending);

  await expect(page.getByText("已发送，等待响应...")).toBeVisible();
  app.socket?.send(
    JSON.stringify({
      type: "session.stream",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
        request_id: "restart-interrupted-request",
        event: {
          type: "message_chunk",
          data: { content: "partial response before restart" },
        },
      },
    }),
  );
  await expect(page.getByText("partial response before restart")).toBeVisible();

  replying.current = false;
  serverPending.current = false;
  const previousSocket = app.socket;
  await previousSocket?.close({ code: 1001, reason: "synthetic server restart" });
  await expect.poll(() => app.socket !== previousSocket).toBe(true);

  await expect(page.getByText("已发送，等待响应...")).toHaveCount(0, {
    timeout: 7000,
  });
  await expect(page.getByText("左滑蓝环开始新会话...")).toBeVisible();
});

test("keeps pending when replying-sessions drops before terminal event", async ({ page }) => {
  const replying = { current: true };
  const app = await openPendingApp(page, replying);

  await expect(page.getByText("已发送，等待响应...")).toBeVisible();
  await expect(page.getByText("左滑蓝环开始新会话...")).toHaveCount(0);

  app.socket?.send(
    JSON.stringify({
      type: "session.stream",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
        event: {
          type: "message_done",
          data: {
            contextWindow: { totalTokens: 12, modelContextWindow: 100 },
          },
        },
      },
    }),
  );

  replying.current = false;
  await page.waitForTimeout(4600);
  await expect(page.getByText("已发送，等待响应...")).toBeVisible();
  await expect(page.getByText("左滑蓝环开始新会话...")).toHaveCount(0);

  app.socket?.send(
    JSON.stringify({
      type: "session.stream",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
        event: {
          type: "tool_call_update",
          data: {
            callId: "tool-1",
            title: "execute bash",
            status: "running",
          },
        },
      },
    }),
  );
  await expect(page.getByText("左滑蓝环开始新会话...")).toHaveCount(0);

  app.socket?.send(
    JSON.stringify({
      id: "req-1",
      type: "session.done",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
      },
    }),
  );

  await expect(page.getByText("已发送，等待响应...")).toHaveCount(0);
  await expect(page.getByText("左滑蓝环开始新会话...")).toBeVisible();
});

test("keeps the latest Pi stream and goal state across background refresh", async ({ page }) => {
  const replying = { current: true };
  const app = await openPendingApp(page, replying);

  app.socket?.send(
    JSON.stringify({
      type: "session.stream",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
        event: {
          type: "message_chunk",
          data: { content: "live Pi answer survives refresh" },
        },
      },
    }),
  );
  await expect(page.getByText("live Pi answer survives refresh")).toBeVisible();

  app.socket?.send(
    JSON.stringify({
      type: "session.stream",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
        event: {
          type: "goal_state",
          data: {
            objective: "repair browser history",
            status: "active",
            autoContinue: true,
            usage: { tokensUsed: 10, activeSeconds: 2 },
          },
        },
      },
    }),
  );
  await expect(page.getByText("目标执行中")).toBeVisible();

  app.socket?.send(
    JSON.stringify({
      type: "session.stream",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
        event: {
          type: "goal_state",
          data: {
            objective: "repair browser history",
            status: "paused",
            autoContinue: false,
            pauseReason: "restart approval required",
            pauseSuggestedAction: "approve restart",
            usage: { tokensUsed: 20, activeSeconds: 4 },
          },
        },
      },
    }),
  );

  await expect(page.getByText("目标已暂停")).toBeVisible();
  await page.waitForTimeout(5200);
  await expect(page.getByText("目标执行中")).toHaveCount(0);
  await expect(page.getByText("repair browser history")).toHaveCount(1);
  await expect(page.getByText("原因：restart approval required")).toBeVisible();
  await expect(page.getByText("live Pi answer survives refresh")).toBeVisible();
});

test("rolls back optimistic pending when websocket send throws", async ({ page }) => {
  const replying = { current: true };
  const app = await openPendingApp(page, replying);
  await expect(page.getByText("已发送，等待响应...")).toBeVisible();

  replying.current = false;
  app.socket?.send(
    JSON.stringify({
      id: "initial-request",
      type: "session.done",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
      },
    }),
  );
  await expect(page.getByText("已发送，等待响应...")).toHaveCount(0);

  await page.evaluate(async () => {
    const { sessionService } = await import("/src/services/session.ts");
    const socket = (sessionService as any).ws;
    socket.send = () => {
      throw new Error("synthetic websocket send failure");
    };
  });

  const editor = page.locator(".token-editor-input").first();
  await editor.click();
  await editor.fill("message whose socket send throws");
  await page.keyboard.press("Enter");

  await expect(page.getByText("消息发送失败：连接未就绪，请稍后重试")).toBeVisible();
  await expect(page.getByText("已发送，等待响应...")).toHaveCount(0);
});

test("treats top-level session.error as terminal for reconnecting clients", async ({ page }) => {
  const replying = { current: true };
  const app = await openPendingApp(page, replying);
  await expect(page.getByText("已发送，等待响应...")).toBeVisible();

  replying.current = false;
  app.socket?.send(
    JSON.stringify({
      id: "request-failed",
      type: "session.error",
      payload: {
        root_id: rootId,
        session_key: sessionKey,
        request_id: "request-failed",
      },
      error: {
        code: "session.message_failed",
        message: "upstream unavailable",
      },
    }),
  );

  await expect(page.getByText("已发送，等待响应...")).toHaveCount(0);
  await expect(page.getByText("左滑蓝环开始新会话...")).toBeVisible();
  await expect(page.getByText("upstream unavailable")).toBeVisible();
});
