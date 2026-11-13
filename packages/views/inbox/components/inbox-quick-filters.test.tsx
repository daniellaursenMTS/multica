// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { STATUS_ORDER } from "@multica/core/issues/config";
import { useInboxFilterStore } from "@multica/core/inbox/filter-store";
import type { InboxItem, IssueStatusEntry } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { InboxQuickFilters } from "./inbox-quick-filters";

function statusEntry(category: (typeof STATUS_ORDER)[number]): IssueStatusEntry {
  return {
    id: `status-${category}`,
    workspace_id: "ws-1",
    key: category,
    name: category,
    description: "",
    category,
    color: "#888888",
    is_system: true,
    position: 0,
    archived_at: null,
    created_at: "",
    updated_at: "",
  };
}

function item(id: string, issueStatus: InboxItem["issue_status"], read = false): InboxItem {
  return {
    id,
    workspace_id: "ws-1",
    recipient_type: "member",
    recipient_id: "member-1",
    actor_type: null,
    actor_id: null,
    type: "status_changed",
    severity: "info",
    issue_id: `issue-${id}`,
    title: id,
    body: null,
    issue_status: issueStatus,
    issue_priority: "none",
    read,
    archived: false,
    created_at: "2026-11-13T00:00:00Z",
    details: null,
  };
}

const ITEMS = [
  item("review-1", "in_review"),
  item("review-2", "in_review", true),
  item("done-1", "done"),
  item("todo-1", "todo"),
];

function renderQuickFilters() {
  setApiInstance({
    listIssueStatuses: async () => ({
      statuses: STATUS_ORDER.map(statusEntry),
      categories: [],
      total: STATUS_ORDER.length,
    }),
  } as unknown as ApiClient);
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  return renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <InboxQuickFilters wsId="ws-1" items={ITEMS} priorityFilterSupport="supported" />
    </QueryClientProvider>,
  );
}

function filters() {
  return useInboxFilterStore.getState().filtersByWorkspace["ws-1"];
}

beforeEach(() => {
  useInboxFilterStore.setState({ filtersByWorkspace: {} });
});

afterEach(() => {
  cleanup();
  document.body.innerHTML = "";
});

describe("InboxQuickFilters", () => {
  it("shows In Progress, In Review and Done with their counts", async () => {
    renderQuickFilters();
    expect(await screen.findByRole("button", { name: /In Progress\s*0/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /In Review\s*2/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Done\s*1/ })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Clear filters" })).toBeNull();
  });

  it("toggles the shared status filter and clears every filter", async () => {
    renderQuickFilters();
    const review = await screen.findByRole("button", { name: /In Review/ });
    fireEvent.click(review);
    expect(filters()?.statuses).toEqual(["in_review"]);
    expect(review.getAttribute("aria-pressed")).toBe("true");

    useInboxFilterStore.getState().toggleUnreadOnly("ws-1");
    fireEvent.click(await screen.findByRole("button", { name: "Clear filters" }));
    expect(filters()).toBeUndefined();
  });

  it("counts respect the other active filters", async () => {
    useInboxFilterStore.getState().toggleUnreadOnly("ws-1");
    renderQuickFilters();
    expect(await screen.findByRole("button", { name: /In Review\s*1/ })).toBeTruthy();
  });
});
