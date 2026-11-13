"use client";

import { ListPlus, ListX } from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { useInboxTasks } from "@multica/core/inbox/queries";
import { useMarkInboxTask, useRemoveInboxTask } from "@multica/core/inbox/mutations";
import { Button } from "@multica/ui/components/ui/button";
import {
  Tooltip,
  TooltipTrigger,
  TooltipContent,
} from "@multica/ui/components/ui/tooltip";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

/**
 * Header toggle that puts the open issue on the member's task list under Inbox
 * (Matas fork), or takes it off. "On the list" covers every reason the server
 * reports — an issue a rule picked up shows as on the list too, so removing it
 * from here does the same as removing it from the Tasks page.
 */
export function IssueTaskToggle({ issueId }: { issueId: string }) {
  const { t } = useT("inbox");
  const wsId = useWorkspaceId();
  const tasks = useInboxTasks(wsId);
  const markTask = useMarkInboxTask();
  const removeTask = useRemoveInboxTask();
  const onList = tasks.some((task) => task.issue.id === issueId);
  const label = onList ? t(($) => $.tasks.remove) : t(($) => $.tasks.add);

  const toggle = () => {
    if (onList) {
      removeTask.mutate(issueId, {
        onSuccess: () => toast.success(t(($) => $.tasks.removed)),
        onError: () => toast.error(t(($) => $.tasks.remove_failed)),
      });
    } else {
      markTask.mutate(issueId, {
        onSuccess: () => toast.success(t(($) => $.tasks.added)),
        onError: () => toast.error(t(($) => $.tasks.add_failed)),
      });
    }
  };

  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={label}
            className={cn("text-muted-foreground", onList && "text-foreground")}
            disabled={markTask.isPending || removeTask.isPending}
            onClick={toggle}
          >
            {onList ? <ListX /> : <ListPlus />}
          </Button>
        }
      />
      <TooltipContent side="bottom">{label}</TooltipContent>
    </Tooltip>
  );
}
