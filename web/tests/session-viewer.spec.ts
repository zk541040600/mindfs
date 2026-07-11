import { expect, test } from "@playwright/test";

test("clears stale generating indicator when pending is cleared", async ({ page }) => {
  await page.goto("/session-viewer-harness.html");

  await page.getByTestId("emit-stream").click();
  await expect(page.getByText("正在生成...")).toBeVisible();

  await page.getByTestId("clear-pending").click();
  await expect(page.getByText("正在生成...")).toHaveCount(0);
  await expect(page.getByText("已发送，等待响应...")).toHaveCount(0);
});

test("renders persisted Pi goal state without exposing raw custom data", async ({ page }) => {
  await page.goto("/session-viewer-harness.html");

  await expect(page.getByText("目标已暂停")).toBeVisible();
  await expect(page.getByText("repair web session history")).toBeVisible();
  await expect(page.getByText("原因：waiting for restart approval")).toBeVisible();
  await expect(page.getByText("建议：approve restart")).toBeVisible();
  await expect(page.getByText("42 tokens · 8 秒活跃时间")).toBeVisible();
});
