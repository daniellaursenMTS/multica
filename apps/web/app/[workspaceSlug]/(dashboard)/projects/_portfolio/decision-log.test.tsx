import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const mockApi = vi.hoisted(() => ({ getProjectDecisionLog: vi.fn(), confirmProjectDecision: vi.fn() }));

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

  it("names who confirmed a decision on the project page", async () => {
    const decision = { ...shared.decisions[0], confirmed_by: "bo", confirmed_by_user: "Kristian Rossing", confirmed_via: "project-page", confirmed_at: "2026-12-03" };
    renderTab({ ...shared, decisions: [decision] });
    expect(await screen.findByText(/Bekræftet af Kristian Rossing \(Business Owner\) 2026-12-03 på projektsiden/)).toBeTruthy();
  });

  const proposal = (extra: Record<string, unknown>) => ({ ...shared.proposals[0], rev: 1, ...extra });
  const holder = { name: "Kristian Rossing", role: "bo", linked: true };

  it("shows the buttons only to the one who holds the decision right", async () => {
    renderTab({ ...shared, reviews: [], proposals: [
      proposal({ id: "1000-9/B-003", holder, can_confirm: true }),
      proposal({ id: "1000-9/B-004", text: "Et andet forslag.", holder, can_confirm: false }),
      proposal({ id: "1000-9/B-005", text: "Et tredje forslag.", can_confirm: false,
        holder: { linked: false, reason: "Kristian Rossing er ikke koblet til en Multica-bruger." } }),
    ] });
    expect(await screen.findAllByRole("button", { name: "Bekræft" })).toHaveLength(1);
    expect(screen.getAllByRole("button", { name: "Ikke en beslutning" })).toHaveLength(1);
    expect(screen.getByText("Kan bekræftes af Kristian Rossing (Business Owner).")).toBeTruthy();
    expect(screen.getByText(/Ingen kan bekræfte forslaget endnu: Kristian Rossing er ikke koblet til en Multica-bruger/)).toBeTruthy();
  });

  it("confirms after asking, and shows the confirmation waiting for the sync", async () => {
    renderTab({ ...shared, reviews: [], proposals: [proposal({ holder, can_confirm: true })] });
    fireEvent.click(await screen.findByRole("button", { name: "Bekræft" }));
    expect(mockApi.confirmProjectDecision).not.toHaveBeenCalled();
    mockApi.confirmProjectDecision.mockResolvedValueOnce({ ...shared, reviews: [], proposals: [proposal({ holder, can_confirm: false,
      confirmation: { action: "confirm", name: "Kristian Rossing", role: "bo", at: "2026-12-03T09:00:00Z", rev: 1, state: "pending" } })] });
    fireEvent.click(screen.getByRole("button", { name: "Ja, bekræft" }));
    expect(await screen.findByText(/Bekræftet af Kristian Rossing \(Business Owner\) 2026-12-03/)).toBeTruthy();
    expect(screen.getByText(/opdateres ved næste sync/)).toBeTruthy();
    expect(mockApi.confirmProjectDecision).toHaveBeenCalledWith("p1", { post: "1000-9/B-003", rev: 1, action: "confirm" });
    expect(screen.queryByRole("button", { name: "Bekræft" })).toBeNull();
  });

  it("declines, and the proposal stays a proposal", async () => {
    renderTab({ ...shared, reviews: [], proposals: [proposal({ holder, can_confirm: true })] });
    fireEvent.click(await screen.findByRole("button", { name: "Ikke en beslutning" }));
    mockApi.confirmProjectDecision.mockResolvedValueOnce({ ...shared, reviews: [], proposals: [proposal({ holder, can_confirm: false,
      declined_by: "bo", declined_by_user: "Kristian Rossing", declined_at: "2026-12-03" })] });
    fireEvent.click(screen.getByRole("button", { name: "Ja, ikke en beslutning" }));
    expect(await screen.findByText("Ikke en beslutning ifølge Kristian Rossing (Business Owner) 2026-12-03.")).toBeTruthy();
    expect(mockApi.confirmProjectDecision).toHaveBeenLastCalledWith("p1", { post: "1000-9/B-003", rev: 1, action: "decline" });
    expect(screen.getByText("Capex-andelen beregnes i kroner.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Bekræft" })).toBeNull();
  });

  it("says so when the server refuses the answer", async () => {
    renderTab({ ...shared, reviews: [], proposals: [proposal({ holder, can_confirm: true })] });
    mockApi.getProjectDecisionLog.mockResolvedValueOnce({ ...shared, reviews: [], proposals: [proposal({ holder, can_confirm: true })] });
    mockApi.confirmProjectDecision.mockRejectedValueOnce(new Error("1000-9/B-003 is at rev 2, not 1"));
    fireEvent.click(await screen.findByRole("button", { name: "Bekræft" }));
    fireEvent.click(screen.getByRole("button", { name: "Ja, bekræft" }));
    expect(await screen.findByText(/Svaret blev ikke gemt: 1000-9\/B-003 is at rev 2, not 1/)).toBeTruthy();
  });

  it("says so when the project has no memory yet", async () => {
    renderTab({ project: { id: "p1", title: "Pilot" }, shared: true, note: "This project has no typed memory yet.", decisions: [], proposals: [], reviews: [] });
    expect(await screen.findByText("Projektet har ingen typet hukommelse endnu.")).toBeTruthy();
  });
});
