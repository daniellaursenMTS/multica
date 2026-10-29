// @vitest-environment node
import { describe, expect, it } from "vitest";
import { compactUsage, importedUsage, scaleUsage, usageMetadata, usageRange, usageTotal } from "./capex-usage";

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
    expect(usageTotal(entry, "2026-07-01", "2026-07-31")).toEqual({ tokens: 30, usd: 0.3, approx: false });
    expect(usageTotal(entry, "2026-07-01", "2026-09-30")).toEqual({ tokens: 150, usd: 1.5, approx: false });
    expect(usageTotal(entry, "2026-01-01", "2026-10-28")).toEqual({ tokens: 210, usd: 2.1, approx: false });
  });
  it("calculates rolling 30 days exactly across a month boundary", () => {
    expect(usageTotal(entry, "2026-09-29", "2026-10-28")).toEqual({ tokens: 110, usd: 1.1, approx: false });
  });
  it("estimates old partial months evenly from the month total and says so", () => {
    // July has no daily figures left: 17 of its 31 days get 17/31 of the month.
    const july = usageTotal(entry, "2026-07-15", "2026-07-31");
    expect(july.approx).toBe(true);
    expect(july.tokens).toBeCloseTo(30 * 17 / 31, 8);
    // September before daily_from (1-13): the month minus its kept days (30) spread over 13 days.
    const sep = usageTotal(entry, "2026-09-07", "2026-09-29");
    expect(sep.approx).toBe(true);
    expect(sep.tokens).toBeCloseTo(30 * 7 / 13 + 40 + 50, 8);
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
  it("finds the range of an entry, counting undated months from their first or last day", () => {
    // July is undated; October's last kept day is the end.
    expect(usageRange(entry)).toEqual({ from: "2026-07-01", to: "2026-10-28" });
    const recent = compactUsage({ ...draft, days: draft.days.slice(3) }, "2026-10-28");
    expect(usageRange(recent)).toEqual({ from: "2026-09-14", to: "2026-10-28" });
    const old = compactUsage({ ...draft, days: draft.days.slice(0, 2) }, "2026-10-28");
    expect(usageRange(old)).toEqual({ from: "2026-07-01", to: "2026-07-31" });
  });
});

describe("editing amounts", () => {
  it("scales months and kept days alike, so the distribution and months-include-days hold", () => {
    const scaled = scaleUsage({ ...entry, issueId: "server-id" }, 420, 4.2);
    expect(scaled).not.toHaveProperty("issueId");
    expect(scaled.months["2026-07"]).toEqual([60, 0.6]);
    expect(scaled.days["2026-09-14"]).toEqual([80, 0.8]);
    expect(scaled.daily_from).toBe(entry.daily_from);
    expect(scaled.through).toBe(entry.through);
    expect(usageTotal(scaled)).toEqual({ tokens: 420, usd: 4.2, approx: false });
    // Partial-month estimates follow: September 7-29 doubles.
    expect(usageTotal(scaled, "2026-09-07", "2026-09-29").tokens).toBeCloseTo(2 * (30 * 7 / 13 + 40 + 50), 8);
  });
  it("scales tokens and kroner independently", () => {
    const scaled = scaleUsage(entry, 210, 0);
    expect(usageTotal(scaled)).toEqual({ tokens: 210, usd: 0, approx: false });
    expect(scaled.days["2026-10-28"]).toEqual([60, 0]);
  });
  it("borrows the other dimension's distribution when one was zero", () => {
    const tokensOnly = compactUsage({ ...draft, days: [
      { date: "2026-10-01", tokens: 10, usd: 0 }, { date: "2026-10-02", tokens: 30, usd: 0 },
    ] }, "2026-10-28");
    const scaled = scaleUsage(tokensOnly, 40, 4);
    expect(scaled.days["2026-10-01"]).toEqual([10, 1]);
    expect(scaled.days["2026-10-02"]).toEqual([30, 3]);
  });
  it("refuses invalid totals and entries without usage", () => {
    expect(() => scaleUsage(entry, -1, 1)).toThrow();
    expect(() => scaleUsage(entry, NaN, 1)).toThrow();
    const empty = compactUsage({ ...draft, days: [{ date: "2026-10-01", tokens: 0, usd: 0 }] }, "2026-10-28");
    expect(() => scaleUsage(empty, 1, 1)).toThrow("intet forbrug");
  });
  it("marks entries owned by the nightly importers", () => {
    expect(importedUsage("claude-code-multica-setup")).toBe(true);
    expect(importedUsage("codex-vibe-hub")).toBe(true);
    expect(importedUsage("0b6c3f9e-1d2a-4c55-9a7e-3f1d2c4b5a69")).toBe(false);
  });
});
