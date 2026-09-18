import { expect, test } from "@playwright/test";

for (const collection of ["sessions", "runs"] as const) {
  for (const pageSize of [25, 100]) {
    test(`${collection} loads all 101 records in pages of ${pageSize} and resets after search`, async ({ page }) => {
      const requests: string[] = [];
      await page.route(new RegExp(`/api/v1/${collection}(?:\\?|$)`), async (route) => {
        const url = new URL(route.request().url());
        requests.push(url.search);
        const searched = url.searchParams.has("q");
        const start = Number(url.searchParams.get("cursor") || "1");
        const indexes = searched ? [101] : Array.from({ length: Math.min(pageSize, 102 - start) }, (_, index) => start + index);
        const items = indexes.map((index) => collection === "sessions"
          ? { id: `session-${index}`, name: `Session ${index}`, state: "active" }
          : { id: `run-${index}`, task: `Run ${index}`, state: "ok" });
        const next = start + pageSize;
        await route.fulfill({ json: { data: { items, total: searched ? 1 : 101 }, next_cursor: searched || next > 101 ? undefined : String(next) } });
      });

      await page.goto(`/${collection}`);
      const first = collection === "sessions" ? "Session 1" : "Run 1";
      const last = collection === "sessions" ? "Session 101" : "Run 101";
      const firstRow = page.getByText(first, { exact: true }).filter({ visible: true });
      const lastRow = page.getByText(last, { exact: true }).filter({ visible: true });
      await expect(firstRow).toBeVisible();
      await expect(lastRow).toHaveCount(0);
      for (let next = 1 + pageSize; next <= 101; next += pageSize) {
        await page.getByRole("button", { name: collection === "sessions" ? "Cargar más sesiones" : "Cargar más runs" }).click();
      }
      await expect(lastRow).toBeVisible();
      await expect(page.getByText(`101 de 101 ${collection === "sessions" ? "sesiones" : "runs"}`)).toBeVisible();
      await expect(page.getByRole("button", { name: collection === "sessions" ? "Cargar más sesiones" : "Cargar más runs" })).toHaveCount(0);
      expect(requests.some((query) => new URLSearchParams(query).get("cursor") === String(1 + pageSize))).toBe(true);
      await page.getByRole("textbox", { name: collection === "sessions" ? "Buscar sesiones" : "Buscar runs" }).fill("needle");
      await expect(firstRow).toHaveCount(0);
      await expect(lastRow).toBeVisible();
      expect(requests.some((query) => {
        const params = new URLSearchParams(query);
        return params.get("q") === "needle" && !params.has("cursor");
      })).toBe(true);
    });
  }
}
