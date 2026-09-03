import { expect, test } from "@playwright/test";

test.describe("Atenea dashboard shell", () => {
  test("renders the responsive shell and navigates clean URLs", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByText("Estado del sistema")).toBeVisible();
    await expect(page.locator("body")).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
    await page.goto("/sessions");
    await expect(page.locator("main").getByRole("heading", { name: "Sessions" })).toBeVisible();
    await page.goto("/metrics");
    await expect(page.locator("main").getByRole("heading", { name: "Metrics" })).toBeVisible();
  });

  test("keeps the mobile navigation usable", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/");
    await expect(page.getByRole("button", { name: "Abrir menú" })).toBeVisible();
    await expect(page.locator("nav").last()).toBeVisible();
  });
});
