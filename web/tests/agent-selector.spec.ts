import { expect, test } from "@playwright/test";

test.beforeEach(async ({ page }) => {
  await page.goto("/agent-selector-harness.html");
});

test("opens Pi model choices when the Pi row is clicked", async ({ page }) => {
  await page.getByAltText("OpenCode").click();
  await page.getByText("pi", { exact: true }).click();

  await expect(page.getByRole("button", { name: "收起 pi 模型列表" })).toBeVisible();
  await expect(page.getByText("cch-responses/GPT 5.4 Mini")).toBeVisible();

  await page.getByText("cch-responses/GPT 5.4 Mini").click();

  await expect(page.getByTestId("selected-state")).toHaveText(
    "pi:cch-responses/gpt-5.4-mini:(none)",
  );
  await expect(page.getByText("cch-responses/GPT 5.4 Mini")).toHaveCount(0);
});
