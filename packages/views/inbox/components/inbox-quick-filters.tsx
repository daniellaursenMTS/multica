"use client";

import { useMemo } from "react";
import { RotateCcw } from "lucide-react";
import {
  filterInboxItems,
  inboxFiltersForPrioritySupport,
  inboxFilterCount,
  type InboxPriorityFilterSupport,
  useInboxFilters,
  useInboxFilterStore,
} from "@multica/core/inbox/filter-store";
import type { InboxItem, IssueStatus } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { StatusIcon } from "../../issues/components/status-icon";
import { useStatusOptions } from "../../issues/utils/status-options";
import { useT } from "../../i18n";

// The statuses that turn the Inbox into a task list (Matas fork, MATA-229):
// what agents are working on, what waits for review, and what is finished and
// can be archived. They toggle the same status filter as the filter menu.
const QUICK_STATUSES: readonly IssueStatus[] = ["in_progress", "in_review", "done"];

/**
 * One-click status filters above the Inbox list, plus a way to clear every
 * filter, so the common filters do not need the dropdown.
 */
export function InboxQuickFilters({
  wsId,
  items,
  priorityFilterSupport,
}: {
  wsId: string;
  items: InboxItem[];
  priorityFilterSupport: InboxPriorityFilterSupport;
}) {
  const { t } = useT("inbox");
  const filters = useInboxFilters(wsId);
  const toggleStatus = useInboxFilterStore((state) => state.toggleStatusFilter);
  const clearFilters = useInboxFilterStore((state) => state.clearFilters);
  const statusOptions = useStatusOptions(wsId);
  const effectiveFilters = useMemo(
    () => inboxFiltersForPrioritySupport(filters, priorityFilterSupport),
    [filters, priorityFilterSupport],
  );
  const activeCount = inboxFilterCount(effectiveFilters);

  // Faceted like the filter menu: each count respects the other dimensions and
  // ignores the status selection, so it says what selecting it can reveal.
  const counts = useMemo(() => {
    const facetItems = filterInboxItems(items, { ...effectiveFilters, statuses: [] });
    const byStatus = new Map<string, number>();
    for (const item of facetItems) {
      if (item.issue_status == null) continue;
      byStatus.set(item.issue_status, (byStatus.get(item.issue_status) ?? 0) + 1);
    }
    return byStatus;
  }, [items, effectiveFilters]);

  const options = QUICK_STATUSES.flatMap((key) => {
    const option = statusOptions.find((candidate) => candidate.key === key);
    return option ? [option] : [];
  });

  return (
    <div
      role="group"
      aria-label={t(($) => $.filters.quick_filters)}
      className="flex shrink-0 flex-wrap items-center gap-1 border-b px-2 py-1.5"
    >
      {options.map((option) => {
        const active = effectiveFilters.statuses.includes(option.key);
        const count = counts.get(option.key) ?? 0;
        return (
          <button
            key={option.key}
            type="button"
            aria-pressed={active}
            onClick={() => toggleStatus(wsId, option.key)}
            className={cn(
              "flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-caption outline-none transition-colors focus-visible:ring-1 focus-visible:ring-ring",
              active
                ? "border-brand bg-brand/10 text-foreground"
                : "border-transparent text-muted-foreground hover:bg-accent hover:text-foreground",
            )}
          >
            <StatusIcon
              status={option.key}
              category={option.category}
              color={option.color}
              className="size-3.5"
            />
            <span>{option.label}</span>
            <span className="tabular-nums text-muted-foreground">{count}</span>
          </button>
        );
      })}
      {activeCount > 0 && (
        <button
          type="button"
          onClick={() => clearFilters(wsId)}
          className="ml-auto flex items-center gap-1 rounded-md px-1.5 py-0.5 text-caption text-muted-foreground outline-none transition-colors hover:bg-accent hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring"
        >
          <RotateCcw className="size-3.5" />
          {t(($) => $.filters.clear)}
        </button>
      )}
    </div>
  );
}
