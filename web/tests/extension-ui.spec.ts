import { expect, test, type Page } from "@playwright/test";

type CapturedResponse = {
  type: "session.extension_ui_response";
  payload: {
    request_id: string;
    method: string;
    value?: string;
    confirmed?: boolean;
    cancelled: boolean;
  };
};

async function capturedResponses(page: Page): Promise<CapturedResponse[]> {
  return page.evaluate(
    () =>
      (
        window as unknown as {
          __extensionUITest: { responses: CapturedResponse[] };
        }
      ).__extensionUITest.responses,
  );
}

test.beforeEach(async ({ page }) => {
  await page.goto("/extension-ui-harness.html");
  await page.getByTestId("request-reset").click();
});

test("submits extension UI dialog responses through the mocked WS path", async ({ page }) => {
  const dialog = page.getByTestId("extension-ui-dialog");

  await page.getByTestId("request-select").click();
  await expect(dialog).toBeVisible();
  await expect(page.getByTestId("extension-ui-title")).toHaveText("Choose bridge route");
  await page.getByTestId("extension-ui-option").filter({ hasText: "sdk-bridge" }).click();
  await expect(dialog).toHaveCount(0);

  await page.getByTestId("request-confirm").click();
  await expect(dialog).toBeVisible();
  await expect(page.getByTestId("extension-ui-message")).toHaveText(
    "Continue deterministic smoke?",
  );
  await page.getByTestId("extension-ui-confirm-yes").click();
  await expect(dialog).toHaveCount(0);

  await page.getByTestId("request-input").click();
  await expect(dialog).toBeVisible();
  await page.getByTestId("extension-ui-input").fill("typed from browser test");
  await page.getByTestId("extension-ui-submit").click();
  await expect(dialog).toHaveCount(0);

  await page.getByTestId("request-editor").click();
  await expect(dialog).toBeVisible();
  await expect(page.getByTestId("extension-ui-editor")).toHaveValue("initial text");
  await page.getByTestId("extension-ui-editor").fill("line one\nline two");
  await page.getByTestId("extension-ui-submit").click();
  await expect(dialog).toHaveCount(0);

  await expect.poll(() => capturedResponses(page)).toEqual([
    {
      type: "session.extension_ui_response",
      payload: {
        root_id: "harness-root",
        session_key: "harness-session",
        agent: "pi",
        request_id: "select-1",
        method: "select",
        value: "sdk-bridge",
        cancelled: false,
      },
    },
    {
      type: "session.extension_ui_response",
      payload: {
        root_id: "harness-root",
        session_key: "harness-session",
        agent: "pi",
        request_id: "confirm-1",
        method: "confirm",
        confirmed: true,
        cancelled: false,
      },
    },
    {
      type: "session.extension_ui_response",
      payload: {
        root_id: "harness-root",
        session_key: "harness-session",
        agent: "pi",
        request_id: "input-1",
        method: "input",
        value: "typed from browser test",
        cancelled: false,
      },
    },
    {
      type: "session.extension_ui_response",
      payload: {
        root_id: "harness-root",
        session_key: "harness-session",
        agent: "pi",
        request_id: "editor-1",
        method: "editor",
        value: "line one\nline two",
        cancelled: false,
      },
    },
  ]);
});

test("keeps fire-and-forget extension UI events non-blocking", async ({ page }) => {
  const dialog = page.getByTestId("extension-ui-dialog");

  for (const requestKey of [
    "notify",
    "setStatus",
    "setWidget",
    "setTitle",
    "set_editor_text",
    "setEditorText",
  ]) {
    await page.getByTestId(`request-${requestKey}`).click();
    await expect(dialog).toHaveCount(0);
  }

  const snapshot = await page.evaluate(
    () =>
      (
        window as unknown as {
          __extensionUITest: {
            chrome: {
              statuses: Record<string, string>;
              widgets: Record<string, { lines: string[]; placement?: string }>;
              title: string;
            };
            editorText: string;
            notifications: string[];
          };
        }
      ).__extensionUITest,
  );

  expect(snapshot.notifications).toContain("ui-demo notification");
  expect(snapshot.chrome.statuses["mindfs.pi_sdk_bridge"]).toBe("running");
  expect(snapshot.chrome.widgets["mindfs.pi_sdk_bridge"]).toEqual({
    lines: ["SDK bridge widget"],
    placement: "aboveEditor",
  });
  expect(snapshot.chrome.title).toBe("MindFS Pi SDK Bridge Smoke");
  expect(snapshot.editorText).toBe("prefilled by camel alias");
  await expect(page).toHaveTitle("MindFS Pi SDK Bridge Smoke");
});
