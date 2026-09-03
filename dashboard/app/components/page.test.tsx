import { describe, expect, it } from "vitest";
import { formatDuration, formatNumber, formatPercent } from "./page";

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
