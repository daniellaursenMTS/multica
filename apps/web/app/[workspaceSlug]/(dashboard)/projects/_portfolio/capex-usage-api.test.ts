// @vitest-environment node
import { beforeEach, describe, expect, it, vi } from "vitest";
const api = vi.hoisted(() => ({ listIssues: vi.fn(), listProjects: vi.fn(), createIssue: vi.fn(),
  getIssue: vi.fn(), updateIssue: vi.fn(), setIssueMetadataKey: vi.fn(), deleteIssue: vi.fn() }));
vi.mock("@multica/core/api", () => ({ api }));
import { loadUsage, removeUsage, saveUsage } from "./capex-usage-api";
import { compactUsage, scaleUsage, usageMetadata } from "./capex-usage";

const entry = compactUsage({ id: "manual-id", label: "Manuel post", capex_id: "1000",
  days: [{ date: "2026-10-28", tokens: 100, usd: 2 }] }, "2026-10-28");
const issue = { id: "issue-id", status: "done", project_id: "storage", title: "Capex-forbrug · manual-id", metadata: usageMetadata(entry) };

beforeEach(() => {
  vi.resetAllMocks();
  api.listProjects.mockResolvedValue({ projects: [{ id: "storage", title: "Multica-opsætning" }] });
  api.listIssues.mockResolvedValue({ issues: [] });
  api.createIssue.mockResolvedValue(issue);
  api.getIssue.mockResolvedValue(issue);
});

describe("usage issue persistence", () => {
  it("creates manual entries as done in the storage project and publishes the marker last", async () => {
    await saveUsage(entry);
    expect(api.createIssue).toHaveBeenCalledWith({ title: "Capex-forbrug · manual-id", project_id: "storage", status: "done" });
    expect(api.setIssueMetadataKey.mock.calls.map((c) => c[1])).toEqual(["capex_usage_id", "capex_usage_data", "capex_usage"]);
  });
  it("resumes an interrupted manual save without creating another issue", async () => {
    api.listIssues.mockResolvedValueOnce({ issues: [] }).mockResolvedValueOnce({ issues: [{ ...issue, metadata: {} }] });
    await saveUsage(entry);
    expect(api.createIssue).not.toHaveBeenCalled();
  });
  it("changes allocation without discarding a fresher imported snapshot", async () => {
    const fresh = { ...entry, months: { "2026-10": [200, 4] as [number, number] } };
    api.listIssues.mockResolvedValue({ issues: [{ ...issue, metadata: usageMetadata(fresh) }] });
    await saveUsage({ ...entry, capex_id: "1005", issueId: issue.id });
    const call = api.setIssueMetadataKey.mock.calls.find((c) => c[1] === "capex_usage_data");
    expect(JSON.parse(call?.[2] as string)).toMatchObject({ capex_id: "1005", months: { "2026-10": [200, 4] } });
  });
  it("Ret replaces the whole entry when the stored one is unchanged", async () => {
    api.listIssues.mockResolvedValue({ issues: [issue] });
    const loaded = { ...entry, issueId: issue.id };
    await saveUsage({ ...scaleUsage(loaded, 300, 6), label: "Rettet" }, loaded);
    const call = api.setIssueMetadataKey.mock.calls.find((c) => c[1] === "capex_usage_data");
    expect(JSON.parse(call?.[2] as string)).toMatchObject({ label: "Rettet", months: { "2026-10": [300, 6] }, days: { "2026-10-28": [300, 6] } });
  });
  it("Ret refuses a stale entry and imported entries", async () => {
    const fresh = { ...entry, months: { "2026-10": [200, 4] as [number, number] } };
    api.listIssues.mockResolvedValue({ issues: [{ ...issue, metadata: usageMetadata(fresh) }] });
    const loaded = { ...entry, issueId: issue.id };
    await expect(saveUsage(scaleUsage(loaded, 300, 6), loaded)).rejects.toThrow("genindlæs");
    const imported = { ...entry, id: "codex-vibe-hub", issueId: issue.id };
    await expect(saveUsage(imported, imported)).rejects.toThrow();
    expect(api.setIssueMetadataKey).not.toHaveBeenCalled();
  });
  it("Remove deletes only the matching usage issue", async () => {
    await removeUsage({ ...entry, issueId: issue.id });
    expect(api.deleteIssue).toHaveBeenCalledWith(issue.id);
    api.getIssue.mockResolvedValue({ ...issue, metadata: { capex_usage_id: "someone-else", capex_usage: true } });
    await expect(removeUsage({ ...entry, issueId: issue.id })).rejects.toThrow();
    expect(api.deleteIssue).toHaveBeenCalledTimes(1);
  });
  it("paginates beyond 100 entries and rejects duplicates", async () => {
    const rows = Array.from({ length: 105 }, (_, n) => ({ ...issue, id: `issue-${n}`, metadata: usageMetadata({ ...entry, id: `usage-${n}` }) }));
    api.listIssues.mockResolvedValueOnce({ issues: rows.slice(0, 100) }).mockResolvedValueOnce({ issues: rows.slice(100) });
    expect(await loadUsage()).toHaveLength(105);
    expect(api.listIssues.mock.calls[1]?.[0].offset).toBe(100);
    api.listIssues.mockResolvedValue({ issues: [issue, issue] });
    await expect(loadUsage()).rejects.toThrow("dubleret");
  });
  it("includes existing metadata in the size guard", async () => {
    api.listIssues.mockResolvedValue({ issues: [{ ...issue, metadata: { ...issue.metadata, extra: "x".repeat(6000) } }] });
    await expect(saveUsage({ ...entry, issueId: issue.id })).rejects.toThrow("6.000");
    expect(api.setIssueMetadataKey).not.toHaveBeenCalled();
  });
  it("does not silently replace corrupt records with zeros", async () => {
    api.listIssues.mockResolvedValue({ issues: [{ ...issue, metadata: { ...issue.metadata, capex_usage_data: "{}" } }] });
    await expect(loadUsage()).rejects.toThrow();
  });
});
