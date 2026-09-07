import { describe, expect, it } from "vitest";
import { measuredMetricLabel } from "./workflow-detail";

describe("workflow telemetry measurement labels", () => {
  const format = (value: number) => String(value);

  it("does not render unknown zero values as measurements", () => {
    expect(measuredMetricLabel(0, { unknown: 1 }, format)).toBe("Desconocido");
    expect(measuredMetricLabel(0, {}, format)).toBe("Desconocido");
  });

  it("labels observed values with incomplete receipts as partial", () => {
    expect(measuredMetricLabel(12, { measured: 1, partial: 1 }, format)).toBe("12 · parcial");
    expect(measuredMetricLabel(0, { measured: 1, partial: 1 }, format)).toBe("0 · parcial");
  });

  it("keeps a fully measured zero as a measured zero", () => {
    expect(measuredMetricLabel(0, { measured: 1 }, format)).toBe("0");
  });
});
