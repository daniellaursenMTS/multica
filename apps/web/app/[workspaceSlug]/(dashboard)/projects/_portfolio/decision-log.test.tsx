import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const mockApi = vi.hoisted(() => ({ getProjectDecisionLog: vi.fn() }));

vi.mock("@multica/core/api", () => ({ api: mockApi }));
vi.mock("@multica/core/auth", () => ({ useAuthStore: () => null }));
vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "workspace-1", slug: "matas" }),
  useWorkspacePaths: () => ({ issueDetail: (id: string) => `/matas/issues/${id}` }),
}));
vi.mock("@multica/core/workspace/queries", () => ({ memberListOptions: () => ({ queryKey: ["members"], queryFn: async () => [] }) }));
vi.mock("@multica/views/navigation", () => ({
  AppLink: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a>,
}));

import { DecisionsTab, decisionLogSchema, roleLabel } from "./decision-log";

// The shape the server returns: Go flattens the post into each decision next to its chain.
const shared = {
  project: { id: "p1", title: "Pilot", number: "1000-9" }, shared: true, generated_at: "2026-12-02T08:00:00+01:00",
  decisions: [{
    id: "1000-9/B-002", type: "decision", area: "technical", text: "Multica flytter til VibeHub.", said_at: "2026-12-01",
    speaker_role: "to", speaker: "Oskar Wilhelmsson", source: "MATA-310", status: "confirmed", confirmed_by: "to", confirmed_at: "2026-12-02",
    supersedes: "1000-9/B-001", rev: 2,
    chain: [{ id: "1000-9/B-001", type: "decision", area: "technical", text: "Multica flytter til Azure.", said_at: "2026-11-20",
      speaker_role: "plo to", source: "MATA-300", source_url: "https://example.test/MATA-300", status: "superseded" }],
  }],
  proposals: [{ id: "1000-9/B-003", type: "decision", area: "business", text: "Capex-andelen beregnes i kroner.", said_at: "2026-12-01",
    speaker_role: "plo", source: "MATA-310", status: "proposal", basis: "decision_rights.business = Kristian Rossing; taleren (plo) har ikke retten" }],
  reviews: [{ id: "1000-9/B-003", type: "decision", text: "Capex-andelen beregnes i kroner.", said_at: "2026-12-01", source: "MATA-310",
    status: "proposal", review: "Samme emne som B-001." }],
};

function renderTab(data: unknown) {
  mockApi.getProjectDecisionLog.mockResolvedValueOnce(data);
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={queryClient}><DecisionsTab projectId="p1" /></QueryClientProvider>);
}

describe("decision log", () => {
  it("parses what the server returns", () => {
    expect(decisionLogSchema.parse(shared).decisions?.[0]?.chain[0]?.id).toBe("1000-9/B-001");
    expect(decisionLogSchema.parse({ project: { id: "p1", title: "Pilot" }, shared: false, ask: "Product Owner: Ina" }).shared).toBe(false);
  });

  it("names roles, and someone outside the diamond as such", () => {
    expect(roleLabel("plo to")).toBe("Platform Owner, Technical Owner");
    expect(roleLabel("none")).toBe("uden rolle i diamanten");
    expect(roleLabel(undefined)).toBe("uden rolle i diamanten");
  });

  it("shows each decision with the chain it replaced, its role, date and source", async () => {
    renderTab(shared);
    expect(await screen.findByText("Multica flytter til VibeHub.")).toBeTruthy();
    expect(screen.getByText("Multica flytter til Azure.")).toBeTruthy();
    expect(screen.getByText(/bekræftet af Technical Owner 2026-12-02/)).toBeTruthy();
    expect(screen.getByText(/Technical Owner \(Oskar Wilhelmsson\)/)).toBeTruthy();
    expect(screen.getAllByRole("link", { name: "MATA-310" })[0]?.getAttribute("href")).toBe("/matas/issues/MATA-310");
    expect(screen.getByRole("link", { name: "MATA-300" }).getAttribute("href")).toBe("https://example.test/MATA-300");
    expect(screen.getByText(/har ikke retten/)).toBeTruthy();
    expect(screen.getByText("Samme emne som B-001.")).toBeTruthy();
  });

  it("says whom to ask when the memory is not shared, and shows no content", async () => {
    renderTab({ project: { id: "p1", title: "Pilot" }, shared: false, ask: "Product Owner: Ina" });
    expect(await screen.findByText(/ikke delt med dig/)).toBeTruthy();
    expect(screen.getByText(/Spørg Product Owner: Ina/)).toBeTruthy();
    expect(screen.queryByText("Gældende beslutninger")).toBeNull();
  });

  it("says so when the project has no memory yet", async () => {
    renderTab({ project: { id: "p1", title: "Pilot" }, shared: true, note: "This project has no typed memory yet.", decisions: [], proposals: [], reviews: [] });
    expect(await screen.findByText("Projektet har ingen typet hukommelse endnu.")).toBeTruthy();
  });
});
