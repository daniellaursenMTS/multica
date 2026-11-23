import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent, Attachment, Issue } from "@multica/core/types";

const mockApi = vi.hoisted(() => ({
  uploadFile: vi.fn(),
  createIssue: vi.fn(),
  setIssueMetadataKey: vi.fn().mockResolvedValue(undefined),
  updateIssue: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({ api: mockApi }));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { user: { id: string } }) => unknown) =>
    selector({ user: { id: "member-1" } }),
}));
vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "workspace-1", slug: "matas" }),
  useWorkspacePaths: () => ({ issueDetail: (id: string) => `/matas/issues/${id}` }),
}));
vi.mock("@multica/views/navigation", () => ({
  AppLink: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a>,
}));
vi.mock("@multica/views/runtimes", () => ({ estimateCost: () => 0 }));
const members = vi.hoisted(() => ({ list: [] as { user_id: string; role: string; name: string; email: string }[] }));
vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members"], queryFn: async () => members.list }),
  agentListOptions: () => ({ queryKey: ["agents"], queryFn: async () => [] }),
}));

import { AddUpdate, CreateDiamond, NewTask, ago, byNumber, matchesSearch, numbered, projectActivity, renameInSpec, type DiaSpec, type Row } from "./portfolio";

const createdIssue = {
  id: "issue-1",
  identifier: "MATA-1",
  title: "Created issue",
} as Issue;

function attachment(file: File): Attachment {
  return {
    id: `attachment-${file.name}`,
    workspace_id: "workspace-1",
    issue_id: null,
    comment_id: null,
    chat_session_id: null,
    chat_message_id: null,
    uploader_type: "member",
    uploader_id: "member-1",
    filename: file.name,
    url: `https://storage.example/${file.name}`,
    download_url: `/api/attachments/attachment-${file.name}/download`,
    markdown_url: `/api/attachments/attachment-${file.name}/download`,
    content_type: file.type,
    size_bytes: file.size,
    created_at: "2026-11-04T00:00:00Z",
  };
}

function renderWithQuery(ui: React.ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(<QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>);
}

beforeEach(() => {
  vi.clearAllMocks();
  mockApi.uploadFile.mockImplementation(async (file: File) => attachment(file));
  mockApi.createIssue.mockResolvedValue(createdIssue);
  mockApi.updateIssue.mockResolvedValue(createdIssue);
  mockApi.setIssueMetadataKey.mockResolvedValue(undefined);
  members.list = [];
});

describe("portfolio document attachments", () => {
  it("binds a Word document to a newly created task", async () => {
    const { container } = renderWithQuery(
      <NewTask projectId="project-1" projectName="Project" agents={[]} />,
    );
    fireEvent.click(screen.getByRole("button", { name: "+ Ny opgave" }));
    fireEvent.change(screen.getByLabelText("Titel"), { target: { value: "Review brief" } });

    const document = new File(["brief"], "brief.docx", {
      type: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
    });
    fireEvent.change(container.querySelector("#portfolio-task-documents")!, {
      target: { files: [document] },
    });
    fireEvent.click(screen.getByRole("button", { name: "Opret opgave" }));

    await waitFor(() => expect(mockApi.createIssue).toHaveBeenCalled());
    expect(mockApi.createIssue).toHaveBeenCalledWith(expect.objectContaining({
      title: "Review brief",
      project_id: "project-1",
      attachment_ids: ["attachment-brief.docx"],
    }));
  });

  it("saves a document-only project update without presentation metadata", async () => {
    const row = {
      id: "row-1",
      name: "Project",
      number: "1004-2",
      multica_project: "project-1",
      dia_spec: { name: "Project", roles: { bo: { person: "Daniel" } } },
    } as Row;
    const orchestrator = {
      id: "orchestrator-1",
      name: "Orchestrator",
      archived_at: null,
    } as Agent;
    const { container } = renderWithQuery(<AddUpdate row={row} agents={[orchestrator]} />);
    fireEvent.click(screen.getByRole("button", { name: "+ Tilføj opdatering" }));
    fireEvent.change(screen.getByLabelText("Hvem kom opdateringen fra?"), {
      target: { value: "Daniel" },
    });

    const document = new File(["budget"], "budget.xlsx", {
      type: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    });
    fireEvent.change(container.querySelector("#upd-documents")!, {
      target: { files: [document] },
    });
    fireEvent.click(screen.getByRole("button", { name: "Send til hubben" }));

    await waitFor(() => expect(mockApi.updateIssue).toHaveBeenCalledWith("issue-1", { status: "todo" }));
    expect(mockApi.createIssue).toHaveBeenCalledWith(expect.objectContaining({
      title: expect.stringMatching(/^Opdatering: \[1004-2\] Project — Daniel, /),
      project_id: "project-1",
      attachment_ids: ["attachment-budget.xlsx"],
      description: expect.stringContaining("Bilagene må ikke læses eller publiceres i hubben"),
    }));
    expect(mockApi.setIssueMetadataKey).not.toHaveBeenCalledWith(
      "issue-1",
      "note_deck",
      expect.anything(),
    );
  });

  it("does not create a task when its document upload fails", async () => {
    mockApi.uploadFile.mockRejectedValueOnce(new Error("lageret svarer ikke"));
    const { container } = renderWithQuery(
      <NewTask projectId="project-1" projectName="Project" agents={[]} />,
    );
    fireEvent.click(screen.getByRole("button", { name: "+ Ny opgave" }));
    fireEvent.change(screen.getByLabelText("Titel"), { target: { value: "Review brief" } });
    fireEvent.change(container.querySelector("#portfolio-task-documents")!, {
      target: { files: [new File(["brief"], "brief.pdf", { type: "application/pdf" })] },
    });
    fireEvent.click(screen.getByRole("button", { name: "Opret opgave" }));

    expect(await screen.findByText(/Kunne ikke uploade brief\.pdf: lageret svarer ikke/)).toBeInTheDocument();
    expect(mockApi.createIssue).not.toHaveBeenCalled();
  });
});

describe("projectActivity", () => {
  const issue = (project_id: string | null, last_activity_at: string | null, metadata = {}) =>
    ({ project_id, last_activity_at, updated_at: "2026-01-01T00:00:00Z", metadata }) as unknown as Issue;

  it("keeps the newest activity per project and skips bookkeeping issues", () => {
    const activity = projectActivity([
      issue("p1", "2026-11-12T09:00:00Z", { pf_intake: true }),
      issue("p1", "2026-11-10T09:00:00Z"),
      issue("p1", "2026-11-11T09:00:00Z"),
      issue("p2", "2026-11-12T08:00:00Z", { capex_usage: true }),
      issue("p3", null),
      issue(null, "2026-11-12T10:00:00Z"),
    ]);
    expect(Object.fromEntries(activity)).toEqual({
      p1: "2026-11-11T09:00:00Z",
      p3: "2026-01-01T00:00:00Z",
    });
  });
});

describe("ago", () => {
  const now = new Date(2026, 10, 12, 14, 0);
  const at = (d: number, h: number, m = 0) => new Date(2026, 10, d, h, m).toISOString();

  it("counts calendar days and shows the time for today and yesterday", () => {
    expect(ago(at(12, 1, 5), now)).toBe("i dag 01.05");
    expect(ago(at(11, 23, 30), now)).toBe("i går 23.30");
    expect(ago(at(10, 15), now)).toBe("2 dage siden");
  });
});

describe("project numbers", () => {
  const row = (name: string, number: string | null, capex?: string) =>
    ({ id: name, name, number, fields: capex ? { capex } : {} }) as unknown as Row;

  it("sorts by number within a Capex, 1004-2 before 1004-12, unnumbered last", () => {
    const rows = [row("Uden", null), row("B", "1004-12"), row("A", "1004-2"), row("C", "1000-1"), row("Andet", null)];
    expect(rows.toSorted(byNumber).map((r) => r.name)).toEqual(["C", "A", "B", "Andet", "Uden"]);
  });

  it("finds a project by name, number or Capex", () => {
    const r = row("Video Transcriber", "1004-1", "1004 · Social Media");
    expect(matchesSearch(r, "1004-1")).toBe(true);
    expect(matchesSearch(r, "video 1004")).toBe(true);
    expect(matchesSearch(r, "social")).toBe(true);
    expect(matchesSearch(r, "1004-12")).toBe(false);
    expect(matchesSearch(r, "")).toBe(true);
  });

  it("writes the number in front of the name", () => {
    expect(numbered({ name: "Scrollspace", number: "1004-2" })).toBe("[1004-2] Scrollspace");
    expect(numbered({ name: "Scrollspace", number: null })).toBe("Scrollspace");
  });
});

describe("diamond stakeholders", () => {
  const row = {
    id: "row-1", name: "Project", multica_project: "project-1", intake_issue: "intake-1", hub: "project",
    dia_spec: { slug: "project", name: "Project", aliases: [], roles: { bo: { person: "Daniel" }, po: { person: null }, plo: { person: null }, to: { person: null } },
      stakeholders: [{ name: "Eva", title: "Jura" }] },
  } as unknown as Row;
  const saved = () => JSON.parse(mockApi.setIssueMetadataKey.mock.calls.find((c) => c[1] === "dia_spec")![2]);

  it("adds a stakeholder with a title and keeps the roles and the ones already there", async () => {
    renderWithQuery(<CreateDiamond row={row} people={[]} rows={[row]} mode="roles" />);
    fireEvent.click(screen.getByRole("button", { name: "Ret diamant" }));
    expect(screen.getByLabelText("Stakeholder 1, navn")).toHaveProperty("value", "Eva");
    fireEvent.click(screen.getByRole("button", { name: "+ Tilføj stakeholder" }));
    fireEvent.change(screen.getByLabelText("Stakeholder 2, navn"), { target: { value: " Kundeservice " } });
    fireEvent.click(screen.getByRole("button", { name: "Gem" }));

    await waitFor(() => expect(mockApi.setIssueMetadataKey).toHaveBeenCalledWith("intake-1", "dia_spec", expect.any(String)));
    expect(saved().stakeholders).toEqual([{ name: "Eva", title: "Jura" }, { name: "Kundeservice" }]);
    expect(saved().roles.bo).toEqual({ person: "Daniel" });
  });

  it("removes the key when the last stakeholder goes, and refuses a title without a name", async () => {
    renderWithQuery(<CreateDiamond row={row} people={[]} rows={[row]} mode="roles" />);
    fireEvent.click(screen.getByRole("button", { name: "Ret diamant" }));
    fireEvent.click(screen.getByRole("button", { name: "+ Tilføj stakeholder" }));
    fireEvent.change(screen.getByLabelText("Stakeholder 2, titel"), { target: { value: "Styregruppe" } });
    expect(screen.getByText("En stakeholder har en titel men intet navn.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Gem" })).toHaveProperty("disabled", true);

    for (const b of screen.getAllByRole("button", { name: "Fjern" }).reverse()) fireEvent.click(b);
    fireEvent.click(screen.getByRole("button", { name: "Gem" }));
    await waitFor(() => expect(mockApi.setIssueMetadataKey).toHaveBeenCalled());
    expect(saved()).not.toHaveProperty("stakeholders");
  });
});

// The project lead (MATA-286): only admins appoint them; the lead changes the rest of the diamond;
// the product owner only the stakeholders.
describe("diamond project lead", () => {
  const row = {
    id: "row-1", name: "Project", multica_project: "project-1", intake_issue: "intake-1", hub: "project",
    dia_spec: { slug: "project", name: "Project", aliases: [], roles: { bo: { person: "Daniel", email: "daniel@matas.dk" }, po: { person: "Petra" }, plo: { person: null }, to: { person: null } },
      stakeholders: [{ name: "Eva", title: "Jura" }], project_lead: { person: "Lis", email: "lis@matas.dk" } },
  } as unknown as Row;
  const saved = () => JSON.parse(mockApi.setIssueMetadataKey.mock.calls.find((c) => c[1] === "dia_spec")![2]);

  it("lets an admin appoint and change the project lead", async () => {
    members.list = [{ user_id: "member-1", role: "admin", name: "Alma", email: "alma@matas.dk" }];
    renderWithQuery(<CreateDiamond row={row} people={[]} rows={[row]} mode="roles" access="admin" />);
    fireEvent.click(screen.getByRole("button", { name: "Ret diamant" }));
    const lead = await screen.findByLabelText("Projektleder, navn");
    expect(lead).toHaveProperty("value", "Lis");
    fireEvent.change(lead, { target: { value: "Otto" } });
    fireEvent.change(screen.getByLabelText("Projektleder, email"), { target: { value: "Otto@Matas.dk" } });
    expect(screen.getByText("Projektleder: Lis → Otto")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Gem" }));

    await waitFor(() => expect(mockApi.setIssueMetadataKey).toHaveBeenCalled());
    expect(saved().project_lead).toEqual({ person: "Otto", email: "otto@matas.dk" });
  });

  it("lets the project lead change roles but not who the project lead is", async () => {
    renderWithQuery(<CreateDiamond row={row} people={[]} rows={[row]} mode="roles" access="lead" />);
    fireEvent.click(screen.getByRole("button", { name: "Ret diamant" }));
    expect(screen.queryByLabelText("Projektleder, navn")).toBeNull();
    expect(screen.getByText("(udpeges af en admin)")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Technical Owner, navn"), { target: { value: "Tue" } });
    fireEvent.click(screen.getByRole("button", { name: "Gem" }));

    await waitFor(() => expect(mockApi.setIssueMetadataKey).toHaveBeenCalled());
    expect(saved().roles.to).toEqual({ person: "Tue" });
    expect(saved().project_lead).toEqual({ person: "Lis", email: "lis@matas.dk" });
  });

  it("lets the product owner change only the stakeholders, on the saved diamond", async () => {
    renderWithQuery(<CreateDiamond row={row} people={[]} rows={[row]} mode="roles" access="po" />);
    fireEvent.click(screen.getByRole("button", { name: "Ret diamant" }));
    expect(screen.getByLabelText("Business Owner, navn")).toHaveProperty("disabled", true);
    fireEvent.click(screen.getByRole("button", { name: "+ Tilføj stakeholder" }));
    fireEvent.change(screen.getByLabelText("Stakeholder 2, navn"), { target: { value: "Kim" } });
    fireEvent.click(screen.getByRole("button", { name: "Gem" }));

    await waitFor(() => expect(mockApi.setIssueMetadataKey).toHaveBeenCalled());
    const { stakeholders, ...rest } = saved();
    expect(stakeholders).toEqual([{ name: "Eva", title: "Jura" }, { name: "Kim" }]);
    const { stakeholders: _before, ...before } = row.dia_spec as DiaSpec;
    expect(rest).toEqual(before);
  });

  it("leaves the project lead out when a member creates a diamond", async () => {
    const fresh = { ...row, hub: null, dia_spec: null } as unknown as Row;
    renderWithQuery(<CreateDiamond row={fresh} people={[]} rows={[fresh]} />);
    fireEvent.click(screen.getByRole("button", { name: /Opret diamant/ }));
    expect(screen.queryByLabelText("Projektleder, navn")).toBeNull();
    fireEvent.change(screen.getByLabelText("Business Owner, navn"), { target: { value: "Bent" } });
    fireEvent.click(screen.getAllByRole("button", { name: /Opret diamant/ }).at(-1)!);

    await waitFor(() => expect(mockApi.setIssueMetadataKey).toHaveBeenCalledWith("intake-1", "dia_spec", expect.any(String)));
    expect(saved()).not.toHaveProperty("project_lead");
  });
});

describe("renameInSpec", () => {
  const spec = {
    slug: "x", name: "X", aliases: [],
    roles: { bo: { person: "Eva" }, po: { person: null }, plo: { person: "Daniel" }, to: { person: null } },
    stakeholders: [{ name: "Eva", title: "Jura" }, { name: "Bo" }],
  } as DiaSpec;

  it("renames the person in roles and among the stakeholders, and carries the login", () => {
    const out = renameInSpec(spec, "Eva", "Eva Hansen", "eva@matas.dk");
    expect(out.roles.bo).toEqual({ person: "Eva Hansen", email: "eva@matas.dk", rename_from: "Eva" });
    expect(out.stakeholders).toEqual([{ name: "Eva Hansen", title: "Jura" }, { name: "Bo" }]);
    expect(spec.stakeholders?.[0]?.name).toBe("Eva");
  });

  it("merges onto a stakeholder already there and keeps the only title", () => {
    expect(renameInSpec(spec, "Eva", "Bo", null).stakeholders).toEqual([{ name: "Bo", title: "Jura" }]);
  });
});
