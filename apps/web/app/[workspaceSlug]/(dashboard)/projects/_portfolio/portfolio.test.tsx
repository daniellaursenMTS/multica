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

import { AddUpdate, NewTask, ago, byNumber, matchesSearch, numbered, projectActivity, type Row } from "./portfolio";

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
