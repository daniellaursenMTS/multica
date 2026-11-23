import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const mockApi = vi.hoisted(() => ({
  createIssue: vi.fn(),
  listIssues: vi.fn().mockResolvedValue({ issues: [], total: 0 }),
  setIssueMetadataKey: vi.fn().mockResolvedValue(undefined),
}));
const members = vi.hoisted(() => ({ list: [] as { user_id: string; role: string }[] }));

vi.mock("@multica/core/api", () => ({ api: mockApi }));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { user: { id: string; email: string } }) => unknown) =>
    selector({ user: { id: "user-1", email: "Petra@Matas.dk" } }),
}));
vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "workspace-1", slug: "matas" }),
  useWorkspacePaths: () => ({ issueDetail: (id: string) => `/matas/issues/${id}` }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members"], queryFn: async () => members.list }),
}));
vi.mock("@multica/views/navigation", () => ({
  AppLink: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a>,
}));

import { canEditContext, diamondAccess, contextAge, contextProblem, ContextTab, parseContext, type ProjectContext } from "./project-context";
import type { Row } from "./portfolio";

const base: ProjectContext = {
  goals: ["Kortere svartid"], priorities: [], milestones: [], decision_rights: {}, sources: [], not_sources: [], watch_for: [], terms: [],
};

function row(over: Partial<Row> = {}): Row {
  return {
    source: "multica", id: "p1", name: "Pilot", url: null, status: "open", assignees: [], priority: null, start_date: null, due_date: null,
    updated: null, description: "", subtasks: 0, fields: {}, multica_project: "p1", intake_issue: "intake-1",
    dia_spec: { slug: "pilot", name: "Pilot", aliases: [], roles: {
      bo: { person: "Bent" }, po: { person: "Petra", email: "petra@matas.dk" }, plo: { person: null }, to: { person: null },
    } },
    ...over,
  };
}

function renderWithQuery(ui: React.ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>);
}

describe("project context fields", () => {
  it("needs 1–3 goals and complete milestones, sources and terms", () => {
    expect(contextProblem(base)).toBeNull();
    expect(contextProblem({ ...base, goals: [] })).toMatch(/1–3 mål/);
    expect(contextProblem({ ...base, goals: ["a", "b", "c", "d"] })).toMatch(/1–3 mål/);
    expect(contextProblem({ ...base, priorities: ["a", "b", "c", "d"] })).toMatch(/tre prioriteter/);
    expect(contextProblem({ ...base, milestones: [{ date: "", what: "Pilot live" }] })).toMatch(/milepæl/);
    expect(contextProblem({ ...base, sources: [{ type: "teams", name: "Kanal", url: "" }] })).toMatch(/link/);
    expect(contextProblem({ ...base, sources: [{ type: "teams", name: "Kanal", url: "https://teams.microsoft.com/x" }] })).toBeNull();
    expect(contextProblem({ ...base, not_sources: [{ type: "teams", name: "Gammel kanal" }] })).toBeNull();
    expect(contextProblem({ ...base, terms: [{ term: "KS", meaning: "" }] })).toMatch(/forklaring/);
  });

  it("reads only a context object and fills missing lists", () => {
    expect(parseContext('{"goals":["a"]}')?.sources).toEqual([]);
    expect(parseContext("{nope")).toBeNull();
    expect(parseContext('{"roles":{}}')).toBeNull();
    expect(parseContext(undefined)).toBeNull();
  });

  it("is stale after 30 days without a change", () => {
    const now = new Date("2026-11-20T12:00:00Z");
    expect(contextAge({ ...base, updated_at: "2026-10-22T12:00:00Z" }, now)).toEqual({ days: 29, stale: false });
    expect(contextAge({ ...base, updated_at: "2026-10-21T12:00:00Z" }, now)).toEqual({ days: 30, stale: true });
    expect(contextAge(base, now)).toEqual({ days: null, stale: false });
  });

  it("lets the diamond's owners and admins edit, nobody else", () => {
    expect(canEditContext(row(), { me: "Bent", email: null, admin: false })).toBe(true);
    expect(canEditContext(row(), { me: null, email: "PETRA@matas.dk", admin: false })).toBe(true);
    expect(canEditContext(row(), { me: "Otto", email: "otto@matas.dk", admin: false })).toBe(false);
    expect(canEditContext(row({ dia_spec: null }), { me: "Bent", email: null, admin: false })).toBe(false);
    expect(canEditContext(row({ dia_spec: null }), { me: null, email: null, admin: true })).toBe(true);
    const archived = row();
    archived.dia_spec = { ...archived.dia_spec!, archived: true };
    expect(canEditContext(archived, { me: "Bent", email: null, admin: false })).toBe(false);
  });
});

describe("project lead (MATA-286)", () => {
  const withLead = () => row({ dia_spec: { ...row().dia_spec!, project_lead: { person: "Lis", email: "lis@matas.dk" } } });

  it("lets the project lead edit the context", () => {
    expect(canEditContext(withLead(), { me: "Lis", email: null, admin: false })).toBe(true);
    expect(canEditContext(withLead(), { me: null, email: "LIS@matas.dk", admin: false })).toBe(true);
    expect(canEditContext(row(), { me: "Lis", email: null, admin: false })).toBe(false);
  });

  it("gives admins and the project lead the diamond, the product owner its stakeholders", () => {
    expect(diamondAccess(withLead(), { me: "Otto", email: null, admin: true })).toBe("admin");
    expect(diamondAccess(withLead(), { me: null, email: "lis@matas.dk", admin: false })).toBe("lead");
    expect(diamondAccess(withLead(), { me: null, email: "petra@matas.dk", admin: false })).toBe("po");
    expect(diamondAccess(withLead(), { me: "Bent", email: null, admin: false })).toBe(null);
    expect(diamondAccess(row({ dia_spec: null }), { me: "Lis", email: null, admin: false })).toBe(null);
  });
});

describe("ContextTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    members.list = [];
    mockApi.listIssues.mockResolvedValue({ issues: [], total: 0 });
  });

  it("shows the context, marks it stale and lets the product owner save a new one", async () => {
    const ctx = { ...base, updated_at: "2026-01-02T10:00:00Z", updated_by: "petra@matas.dk",
      sources: [{ type: "clickup", name: "Pilot-liste", url: "https://app.clickup.com/x" }] };
    renderWithQuery(<ContextTab row={row({ intake_meta: { ctx_spec: JSON.stringify(ctx), ctx_synced: ctx.updated_at } })} me={null} />);
    expect(screen.getByText("Kortere svartid")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Pilot-liste" }).getAttribute("href")).toBe("https://app.clickup.com/x");
    expect(screen.getByText(/Ikke ændret i \d+ dage/)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Ret kontekst" }));
    fireEvent.change(screen.getByLabelText("Mål 2"), { target: { value: "  Færre fejl  " } });
    fireEvent.click(screen.getByRole("button", { name: "Gem kontekst" }));
    await waitFor(() => expect(mockApi.setIssueMetadataKey).toHaveBeenCalled());
    const [issue, key, value] = mockApi.setIssueMetadataKey.mock.calls[0]!;
    expect([issue, key]).toEqual(["intake-1", "ctx_spec"]);
    const saved = JSON.parse(value);
    expect(saved.goals).toEqual(["Kortere svartid", "Færre fejl"]);
    expect(saved.updated_at).toBeUndefined(); // the server sets who and when
  });

  it("does not offer editing to someone outside the diamond", () => {
    // The signed-in user is Petra by e-mail; here the PO role has someone else's.
    const other = row();
    other.dia_spec!.roles.po = { person: "Pia", email: "pia@matas.dk" };
    renderWithQuery(<ContextTab row={other} me="Otto" />);
    expect(screen.queryByRole("button", { name: "Skriv kontekst" })).toBeNull();
    expect(screen.getByText(/Kun projektets diamant/)).toBeTruthy();
  });

  it("lists agents' open proposals", async () => {
    mockApi.listIssues.mockResolvedValue({ issues: [
      { id: "i1", identifier: "MATA-9", title: "Forslag til kontekst: Pilot — sources", status: "in_review" },
      { id: "i2", identifier: "MATA-8", title: "Gammelt forslag", status: "done" },
    ], total: 2 });
    renderWithQuery(<ContextTab row={row()} me="Petra" />);
    expect(await screen.findByText("Forslag fra agenter (1)")).toBeTruthy();
    expect(mockApi.listIssues).toHaveBeenCalledWith({ project_id: "p1", metadata: { ctx_proposal: true }, limit: 50 });
  });
});
