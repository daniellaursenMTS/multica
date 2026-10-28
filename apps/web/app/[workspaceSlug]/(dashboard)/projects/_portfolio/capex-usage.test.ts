// @vitest-environment node
import { describe, expect, it } from "vitest";
import { compactUsage, usageMetadata, usageTotal } from "./capex-usage";

const draft = { id: "stable", label: "Claude + Codex", capex_id: "1000", days: [
  { date: "2026-07-01", tokens: 10, usd: 0.1 }, { date: "2026-07-31", tokens: 20, usd: 0.2 },
  { date: "2026-09-13", tokens: 30, usd: 0.3 }, { date: "2026-09-14", tokens: 40, usd: 0.4 },
  { date: "2026-09-29", tokens: 50, usd: 0.5 }, { date: "2026-10-28", tokens: 60, usd: 0.6 },
] };
const entry = compactUsage(draft, "2026-10-28");

describe("compact usage accounting", () => {
  it("keeps all monthly history and only the latest 45 days", () => {
    expect(entry.months["2026-07"]).toEqual([30, 0.3]);
    expect(Object.keys(entry.days)).toEqual(["2026-09-14", "2026-09-29", "2026-10-28"]);
    expect(entry.daily_from).toBe("2026-09-14");
  });
  it("uses monthly history for old months, quarters and year-to-date without double counting", () => {
    expect(usageTotal(entry, "2026-07-01", "2026-07-31")).toEqual({ tokens: 30, usd: 0.3 });
    expect(usageTotal(entry, "2026-07-01", "2026-09-30")).toEqual({ tokens: 150, usd: 1.5 });
    expect(usageTotal(entry, "2026-01-01", "2026-10-28")).toEqual({ tokens: 210, usd: 2.1 });
  });
  it("calculates rolling 30 days exactly across a month boundary", () => {
    expect(usageTotal(entry, "2026-09-29", "2026-10-28")).toEqual({ tokens: 110, usd: 1.1 });
  });
  it("refuses invented precision for old partial months", () => {
    expect(() => usageTotal(entry, "2026-07-15", "2026-07-31")).toThrow("hele måneder");
  });
  it("supports fractional hand-entered amounts spread across month boundaries", () => {
    const manual = compactUsage({ ...draft, days: [
      { date: "2026-09-30", tokens: 1 / 3, usd: 1 / 3 },
      { date: "2026-10-01", tokens: 2 / 3, usd: 2 / 3 },
    ] }, "2026-10-28");
    expect(usageTotal(manual).usd).toBeCloseTo(1, 8);
    expect(usageTotal(manual).tokens).toBeCloseTo(1, 8);
  });
  it("stores allocation and stable id but strips UI issue ids", () => {
    const data = usageMetadata({ ...entry, issueId: "server-id" });
    expect(data.capex_usage).toBe(true);
    expect(data.capex_usage_id).toBe("stable");
    expect(JSON.parse(data.capex_usage_data).issueId).toBeUndefined();
    expect(JSON.parse(data.capex_usage_data).capex_id).toBe("1000");
  });
  it("guards size, invalid values, future dates and double allocation before writes", () => {
    expect(() => usageMetadata({ ...entry, note: "ø".repeat(6000) })).toThrow("6.000");
    expect(() => compactUsage({ ...draft, project_id: "project" }, "2026-10-28")).toThrow();
    expect(() => usageMetadata({ ...entry, project_id: "project" })).toThrow();
    expect(() => compactUsage({ ...draft, days: [{ date: "2026-10-29", tokens: 1, usd: 1 }] }, "2026-10-28")).toThrow();
    expect(() => compactUsage({ ...draft, days: [{ date: "2026-10-28", tokens: 1, usd: NaN }] }, "2026-10-28")).toThrow();
  });
});
