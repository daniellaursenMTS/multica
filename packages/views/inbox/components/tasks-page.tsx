"use client";

import { useCallback, useEffect, useState } from "react";
import { useDefaultLayout } from "react-resizable-panels";
import { useQuery } from "@tanstack/react-query";
import { ArrowLeft, Info, ListChecks, ListX } from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { inboxTaskListOptions } from "@multica/core/inbox/queries";
import { useRemoveInboxTask } from "@multica/core/inbox/mutations";
import type { InboxTask, InboxTaskReason } from "@multica/core/types";
import { ErrorBoundary } from "@multica/ui/components/common/error-boundary";
import { Button } from "@multica/ui/components/ui/button";
import {
  ResizablePanelGroup,
  ResizablePanel,
  ResizableHandle,
} from "@multica/ui/components/ui/resizable";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { NumberFlow } from "@multica/ui/components/ui/number-flow";
import {
  Tooltip,
  TooltipTrigger,
  TooltipContent,
} from "@multica/ui/components/ui/tooltip";
import { useIsCompact } from "@multica/ui/hooks/use-mobile";
import { cn } from "@multica/ui/lib/utils";
import { IssueDetail, StatusIcon } from "../../issues/components";
import { useStatusLabel } from "../../issues/utils/status-label";
import { handleRowActivationKey } from "../../common/row-actions-menu";
import { PAGE_GUTTER, PageHeader } from "../../layout/page-header";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { useTimeAgo } from "./inbox-list-item";

// Tasks under Inbox (Matas fork, MATA-229): the issues the member has to act
// on, laid out like the Inbox — list on the left, the issue's conversation on
// the right. Which issues are on the list is decided by the server.

function useReasonLabel() {
  const { t } = useT("inbox");
  return (reason: InboxTaskReason): string => {
    switch (reason) {
      case "blocked":
        return t(($) => $.tasks.reasons.blocked);
      case "review":
        return t(($) => $.tasks.reasons.review);
      case "assigned":
        return t(($) => $.tasks.reasons.assigned);
      case "mentioned":
        return t(($) => $.tasks.reasons.mentioned);
      case "manual":
        return t(($) => $.tasks.reasons.manual);
      default:
        return reason;
    }
  };
}

function TaskListItem({
  task,
  isSelected,
  onClick,
  onRemove,
}: {
  task: InboxTask;
  isSelected: boolean;
  onClick: () => void;
  onRemove: () => void;
}) {
  const { t } = useT("inbox");
  const timeAgo = useTimeAgo();
  const reasonLabel = useReasonLabel();
  const { issue } = task;
  const { categoryOf, colorOf } = useIssueStatuses(issue.workspace_id);
  const statusLabelOf = useStatusLabel(issue.workspace_id);
  const removeLabel = t(($) => $.tasks.remove);
  const activityAt = issue.last_activity_at ?? issue.updated_at;

  return (
    <div
      role="button"
      tabIndex={0}
      onClick={onClick}
      onKeyDown={(e) => handleRowActivationKey(e, onClick)}
      className={cn(
        "group flex w-full cursor-default select-none items-center gap-3 rounded-md px-2 py-2.5 text-left outline-none transition-colors focus-visible:ring-1 focus-visible:ring-ring",
        isSelected ? "bg-accent" : "hover:bg-accent/50",
      )}
    >
      <span title={statusLabelOf(issue.status)} className="flex shrink-0 items-center">
        <StatusIcon
          status={issue.status}
          category={categoryOf(issue.status)}
          color={colorOf(issue.status)}
          className="h-4 w-4 shrink-0"
        />
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex items-center justify-between gap-2">
          <span className="truncate text-body font-medium">{issue.title}</span>
          <button
            type="button"
            title={removeLabel}
            aria-label={removeLabel}
            onClick={(e) => {
              e.stopPropagation();
              onRemove();
            }}
            className="hidden shrink-0 rounded p-0.5 text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring [@media(hover:hover)]:group-hover:inline-flex [@media(hover:hover)]:group-focus-within:inline-flex"
          >
            <ListX className="h-3.5 w-3.5" />
          </button>
        </div>
        <div className="mt-0.5 flex items-center justify-between gap-2">
          <div className="flex min-w-0 items-center gap-1.5 overflow-hidden text-caption text-muted-foreground">
            <span className="shrink-0">{issue.identifier}</span>
            {task.reasons.map((reason) => (
              <span
                key={reason}
                className={cn(
                  "shrink-0 rounded px-1 py-px text-caption",
                  reason === "blocked"
                    ? "bg-destructive/10 text-destructive"
                    : "bg-muted text-muted-foreground",
                )}
              >
                {reasonLabel(reason)}
              </span>
            ))}
          </div>
          <span className="shrink-0 text-caption text-muted-foreground">
            {timeAgo(activityAt)}
          </span>
        </div>
      </div>
    </div>
  );
}

export function TasksPage() {
  const { t } = useT("inbox");
  const { searchParams, replace } = useNavigation();
  const wsPaths = useWorkspacePaths();
  const wsId = useWorkspaceId();
  const isCompact = useIsCompact();
  const urlIssue = searchParams.get("issue") ?? "";
  const [selectedId, setSelectedIdState] = useState(urlIssue);
  useEffect(() => {
    setSelectedIdState(urlIssue);
  }, [urlIssue]);

  const { data: tasks = [], isLoading } = useQuery(inboxTaskListOptions(wsId));
  const removeTask = useRemoveInboxTask();
  const { defaultLayout, onLayoutChanged } = useDefaultLayout({
    id: "multica_tasks_layout",
  });

  const setSelectedId = useCallback(
    (id: string) => {
      setSelectedIdState(id);
      const base = wsPaths.tasks();
      replace(id ? `${base}?issue=${encodeURIComponent(id)}` : base);
    },
    [replace, wsPaths],
  );

  const handleRemove = (issueId: string) => {
    if (issueId === selectedId) setSelectedId("");
    removeTask.mutate(issueId, {
      onError: () => toast.error(t(($) => $.tasks.remove_failed)),
    });
  };

  const listHeader = (
    <PageHeader>
      <div className="flex flex-1 items-center gap-2">
        <h1 className="text-body font-semibold">{t(($) => $.tasks.title)}</h1>
        {tasks.length > 0 && (
          <NumberFlow
            value={tasks.length}
            animated={false}
            format={{ maximumFractionDigits: 0 }}
            aria-label={String(tasks.length)}
            className="text-caption text-muted-foreground"
          />
        )}
      </div>
      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={t(($) => $.tasks.info)}
              className="text-muted-foreground"
            />
          }
        >
          <Info className="h-4 w-4" />
        </TooltipTrigger>
        <TooltipContent side="bottom" className="max-w-xs">
          {t(($) => $.tasks.info)}
        </TooltipContent>
      </Tooltip>
    </PageHeader>
  );

  const loadingRows = (
    <div className="flex-1 min-h-0 overflow-y-auto space-y-1 p-2">
      {Array.from({ length: 5 }).map((_, i) => (
        <div key={i} className="flex items-center gap-3 px-2 py-2.5">
          <Skeleton className="h-4 w-4 shrink-0 rounded-full" />
          <div className="flex-1 space-y-2">
            <Skeleton className="h-4 w-3/4" />
            <Skeleton className="h-3 w-1/2" />
          </div>
        </div>
      ))}
    </div>
  );

  const list = isLoading ? (
    loadingRows
  ) : tasks.length === 0 ? (
    <div className="flex flex-1 flex-col items-center justify-center px-6 py-16 text-center text-muted-foreground">
      <ListChecks className="mb-3 h-8 w-8 text-faint-foreground" />
      <p className="text-body">{t(($) => $.tasks.empty)}</p>
      <p className="mt-2 max-w-xs text-caption">{t(($) => $.tasks.info)}</p>
    </div>
  ) : (
    <div className="flex-1 min-h-0 overflow-y-auto space-y-0.5 p-2">
      {tasks.map((task) => (
        <TaskListItem
          key={task.issue.id}
          task={task}
          isSelected={task.issue.id === selectedId}
          onClick={() => setSelectedId(task.issue.id)}
          onRemove={() => handleRemove(task.issue.id)}
        />
      ))}
    </div>
  );

  const listPanel = (
    <>
      {listHeader}
      {list}
    </>
  );

  // Compact widths show the detail full screen, so the way back to the list
  // lives in the detail header.
  const compactBackAction = isCompact ? (
    <Button
      variant="ghost"
      size="sm"
      onClick={() => setSelectedId("")}
      className="-ml-2 shrink-0 gap-1.5 text-muted-foreground"
    >
      <ArrowLeft className="h-4 w-4" />
      {t(($) => $.tasks.back)}
    </Button>
  ) : undefined;

  const detailContent = selectedId ? (
    <ErrorBoundary
      resetKeys={[selectedId]}
      fallback={compactBackAction ? ({ error }) => (
        <div className="flex flex-1 min-h-0 flex-col">
          <div className={cn("flex h-12 shrink-0 items-center gap-2 border-b", PAGE_GUTTER)}>
            {compactBackAction}
          </div>
          <div className="flex flex-1 min-h-0 items-center justify-center px-4 text-center text-body text-muted-foreground">
            {error.message}
          </div>
        </div>
      ) : undefined}
    >
      <IssueDetail
        key={selectedId}
        issueId={selectedId}
        defaultSidebarOpen={false}
        layoutId="multica_tasks_issue_detail_layout"
        leadingAction={compactBackAction ?? false}
        onDelete={() => setSelectedId("")}
      />
    </ErrorBoundary>
  ) : null;

  if (isCompact) {
    if (detailContent) {
      return <div className="flex flex-1 flex-col min-h-0">{detailContent}</div>;
    }
    return <div className="flex flex-1 flex-col min-h-0">{listPanel}</div>;
  }

  return (
    <ResizablePanelGroup orientation="horizontal" className="flex-1 min-h-0" defaultLayout={defaultLayout} onLayoutChanged={onLayoutChanged}>
      <ResizablePanel id="list" defaultSize={280} minSize={240} maxSize={480} groupResizeBehavior="preserve-pixel-size">
        <div className="flex flex-col border-r h-full">
          {listPanel}
        </div>
      </ResizablePanel>
      <ResizableHandle />
      <ResizablePanel id="detail" minSize="40%">
        <div className="flex flex-col min-h-0 h-full">
          {detailContent ?? (
            <div className="flex h-full flex-col items-center justify-center text-muted-foreground">
              <ListChecks className="mb-3 h-10 w-10 text-faint-foreground" />
              <p className="text-body">
                {tasks.length === 0
                  ? t(($) => $.tasks.empty)
                  : t(($) => $.tasks.select_prompt)}
              </p>
            </div>
          )}
        </div>
      </ResizablePanel>
    </ResizablePanelGroup>
  );
}
