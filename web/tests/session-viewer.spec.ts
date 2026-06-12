import { expect, test } from "@playwright/test";

test("clears stale generating indicator when pending is cleared", async ({ page }) => {
  await page.goto("/session-viewer-harness.html");

  await page.getByTestId("emit-stream").click();
  await expect(page.getByText("正在生成...")).toBeVisible();

  await page.getByTestId("clear-pending").click();
  await expect(page.getByText("正在生成...")).toHaveCount(0);
  await expect(page.getByText("已发送，等待响应...")).toHaveCount(0);
});
