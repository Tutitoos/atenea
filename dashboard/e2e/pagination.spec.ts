import { expect, test } from "@playwright/test";

for (const collection of ["sessions", "runs"] as const) {
  test(`${collection} loads later pages and resets the cursor after search`, async ({ page }) => {
    const requests: string[] = [];
    await page.route(new RegExp(`/api/v1/${collection}(?:\\?|$)`), async (route) => {
      const url = new URL(route.request().url());
      requests.push(url.search);
      const searched = url.searchParams.has("q");
      const second = url.searchParams.get("cursor") === "page-2";
      const indexes = searched ? [101] : second ? [101] : Array.from({ length: 100 }, (_, index) => index + 1);
      const items = indexes.map((index) => collection === "sessions"
        ? { id: `session-${index}`, name: `Session ${index}`, state: "active" }
        : { id: `run-${index}`, task: `Run ${index}`, state: "ok" });
      await route.fulfill({ json: { data: { items, total: searched ? 1 : 101 }, next_cursor: searched || second ? undefined : "page-2" } });
    });

    await page.goto(`/${collection}`);
    const first = collection === "sessions" ? "Session 1" : "Run 1";
    const last = collection === "sessions" ? "Session 101" : "Run 101";
    const firstRow = page.getByText(first, { exact: true }).filter({ visible: true });
    const lastRow = page.getByText(last, { exact: true }).filter({ visible: true });
    await expect(firstRow).toBeVisible();
    await expect(lastRow).toHaveCount(0);
    await page.getByRole("button", { name: collection === "sessions" ? "Cargar más sesiones" : "Cargar más runs" }).click();
    await expect(lastRow).toBeVisible();
    expect(requests.some((query) => new URLSearchParams(query).get("cursor") === "page-2")).toBe(true);
    await page.getByRole("textbox", { name: collection === "sessions" ? "Buscar sesiones" : "Buscar runs" }).fill("needle");
    await expect(firstRow).toHaveCount(0);
    await expect(lastRow).toBeVisible();
    expect(requests.some((query) => {
      const params = new URLSearchParams(query);
      return params.get("q") === "needle" && !params.has("cursor");
    })).toBe(true);
  });
}
