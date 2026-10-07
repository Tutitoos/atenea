import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { RedCauses, formatDuration, formatNumber, formatPercent } from "./page";

describe("dashboard formatters", () => {
  it("preserves unknown and unmeasured values", () => {
    expect(formatNumber(undefined)).toBe("No medido");
    expect(formatDuration(Number.NaN)).toBe("No medido");
    expect(formatPercent(Infinity)).toBe("No medido");
  });

  it("formats finite operational values", () => {
    expect(formatNumber(1234)).toBe("1234");
    expect(formatDuration(1250)).toContain("1,3");
    expect(formatPercent(95)).toBe("95%");
  });
});

describe("red status attribution", () => {
  it("shows a red cause and separate expired and unknown readings from the status contract", () => {
    const html = renderToStaticMarkup(<RedCauses overview={{ snapshot: { light: "red", redCauses: [
      { capability: "code.search", implementation: "graph.search", repository: "api", state: "down", evidence: "runtime observation", reason: "A runtime observation reports this implementation unavailable.", observedAt: "2026-10-07T07:00:00Z" },
    ], capabilities: [
      { id: "code.search", implementations: [{ id: "graph.search", repository: "api", state: "down", lastChecked: "2026-10-07T07:00:00Z" }] },
      { id: "code.context", implementations: [{ id: "old.context", state: "unknown", healthExpired: true, lastChecked: "2026-10-06T07:00:00Z" }, { id: "new.context", state: "unknown", healthSource: "configuration" }] },
    ] } }} />);
    expect(html).toContain("code.search / graph.search");
    expect(html).toContain("api");
    expect(html).toContain("Comprobado");
    expect(html).toContain("Observación caducada");
    expect(html).toContain("Hora de comprobación desconocida");
    expect(html).toContain("Otras lecturas; no son causas del rojo");
  });
});
