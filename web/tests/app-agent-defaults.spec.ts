import { expect, test, type Route } from "@playwright/test";

const rootId = "ge";

async function fulfillJSON(route: Route, body: unknown, status = 200) {
  await route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
}

test("defaults Pi GPT thinking mode to xhigh even when runtime reports off", async ({ page }) => {
  await page.routeWebSocket((url) => url.pathname === "/ws", (ws) => {
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
            current_mode_id: "off",
            models: [
              { id: "cch-responses/gpt-5.5", name: "cch-responses/GPT 5.5" },
              { id: "cch-responses/gpt-5.4-mini", name: "cch-responses/GPT 5.4 Mini" },
            ],
            modes: [
              { id: "off", name: "Thinking: off" },
              { id: "minimal", name: "Thinking: minimal" },
              { id: "low", name: "Thinking: low" },
              { id: "medium", name: "Thinking: medium" },
              { id: "high", name: "Thinking: high" },
              { id: "xhigh", name: "Thinking: xhigh" },
            ],
          },
        ],
        shells: [],
      });
      return;
    }
    if (path === "/api/sessions") {
      await fulfillJSON(route, []);
      return;
    }
    if (path === "/api/replying-sessions") {
      await fulfillJSON(route, { sessions: [] });
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

  await page.goto(`/?root=${rootId}`);

  await page.getByAltText("Pi").click();
  await page.getByText("pi", { exact: true }).click();

  await expect(page.getByRole("button", { name: /模式\s+xhigh/ })).toBeVisible();
});
