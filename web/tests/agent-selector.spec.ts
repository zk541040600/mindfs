import { expect, test } from "@playwright/test";

declare global {
  interface Window {
    __agentSelectorTest: {
      refreshes: string[];
    };
    __agentSelectorResolvePiRefresh?: () => void;
  }
}

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

test("selects GPT 5.6 with max thinking mode", async ({ page }) => {
  await page.getByAltText("OpenCode").click();
  await page.getByText("pi", { exact: true }).click();
  await page.getByText("cch-responses/GPT 5.6 Sol").click();

  await page.getByAltText("Pi").click();
  await page.getByText("pi", { exact: true }).click();
  await page.getByRole("button", { name: /模式/ }).click();
  await page.getByText("Thinking: max").click();

  await expect(page.getByTestId("selected-state")).toHaveText(
    "pi:cch-responses/gpt-5.6-sol:max",
  );
});

test("opens Pi model choices after a stale Pi row refreshes", async ({ page }) => {
  await page.goto("/agent-selector-harness.html?scenario=stale-pi");

  await page.getByAltText("OpenCode").click();
  await expect(page.getByRole("button", { name: "查看 pi 错误信息" })).toBeVisible();
  await expect
    .poll(() =>
      page.evaluate(
        () => window.__agentSelectorTest.refreshes.filter((name) => name === "pi").length,
      ),
    )
    .toBeGreaterThanOrEqual(1);
  const piRefreshesAfterOpen = await page.evaluate(
    () => window.__agentSelectorTest.refreshes.filter((name) => name === "pi").length,
  );

  await page.getByText("pi", { exact: true }).click();
  await expect
    .poll(() =>
      page.evaluate(
        () => window.__agentSelectorTest.refreshes.filter((name) => name === "pi").length,
      ),
    )
    .toBe(piRefreshesAfterOpen + 1);
  await expect(page.getByTestId("selected-state")).toHaveText(
    "opencode:(none):(none)",
  );
  await expect(page.getByText("cch-responses/GPT 5.4 Mini")).toHaveCount(0);

  await page.evaluate(() => window.__agentSelectorResolvePiRefresh?.());

  await expect(page.getByRole("button", { name: "收起 pi 模型列表" })).toBeVisible();
  await expect(page.getByText("cch-responses/GPT 5.4 Mini")).toBeVisible();

  await page.getByText("cch-responses/GPT 5.4 Mini").click();

  await expect(page.getByTestId("selected-state")).toHaveText(
    "pi:cch-responses/gpt-5.4-mini:(none)",
  );
});
