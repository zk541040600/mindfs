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
) {
  let socket: WebSocketRoute | null = null;

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
