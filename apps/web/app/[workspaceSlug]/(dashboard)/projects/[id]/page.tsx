"use client";

import { Suspense, use, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { useWorkspacePaths } from "@multica/core/paths";
import { AppLink } from "@multica/views/navigation";
import { ProjectDetail } from "@multica/views/projects/components";
import { DiamondTab, OverviewTab, usePortfolio } from "../_portfolio/portfolio";

type View = "overview" | "kanban" | "diamond";

// A project page: Oversigt (data, tasks, history), Kanban (Multica's own project view) and Diamant.
// ClickUp projects that are not moved yet only have Oversigt.
function ProjectPage({ id }: { id: string }) {
  const router = useRouter();
  const params = useSearchParams();
  const p = useWorkspacePaths();
  const { find, project, loading, agents, agentName, chatAgentId, index, rows, viewsKey } = usePortfolio();
  // Name suggestions for a new diamond: real people only — never the fictional demo cast.
  const demoHubs = new Set(rows.filter((r) => r.demo && r.hub).map((r) => r.hub as string));
  const people = (index?.people ?? []).filter((x) => x.roles.some((r) => !demoHubs.has(r.project))).map((x) => x.name);
  const row = find(id);
  const projectId = row?.multica_project ?? (row ? null : id);
  const tabs: [View, string][] = [["overview", "Oversigt"]];
  if (projectId) tabs.push(["kanban", "Kanban"]);
  if (row?.hub || row?.dia_spec) tabs.push(["diamond", "Diamant"]);
  const asked = params.get("view") as View | null;
  const view: View = tabs.some(([k]) => k === asked) ? (asked as View) : row || loading ? "overview" : "kanban";
  const go = (v: View) => router.push(v === "overview" ? "?" : `?view=${v}`);
  const [editing, setEditing] = useState(false);

  return (
    <div className="flex h-full min-h-0 flex-1 flex-col">
      <div className="flex flex-col gap-3 border-b px-6 pt-4">
        <AppLink href={p.projects()} className="self-start text-sm text-muted-foreground hover:text-foreground">← Projekter</AppLink>
        <div className="flex flex-wrap items-center gap-3">
          <h1 className="text-xl font-semibold">
            {row?.hub && <span className="mr-2 text-[#CF022D]" title="Har diamant">◇</span>}
            {row?.name ?? project(row)?.title ?? (loading ? "…" : "Projekt")}
          </h1>
          {row?.demo && <span className="rounded bg-muted px-1.5 py-0.5 text-xs uppercase">demo</span>}
          {row?.multica_project && !editing && (
            <button type="button" onClick={() => { setEditing(true); if (view !== "overview") go("overview"); }}
              className="rounded-md border px-3 py-1 text-sm font-medium hover:bg-accent">
              Opdater felter
            </button>
          )}
        </div>
        <nav className="-mb-px flex gap-4 text-sm" aria-label="Projektvisning">
          {tabs.map(([k, label]) => (
            <button key={k} type="button" aria-current={view === k ? "page" : undefined} onClick={() => go(k)}
              className={`border-b-2 pb-2 ${view === k ? "border-foreground font-medium" : "border-transparent text-muted-foreground hover:text-foreground"}`}>
              {k === "diamond" && <span className="mr-1 text-[#CF022D]">◇</span>}{label}
            </button>
          ))}
        </nav>
      </div>

      {view === "kanban" && projectId ? (
        <div className="flex min-h-0 flex-1 flex-col"><ProjectDetail projectId={projectId} /></div>
      ) : (
        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="p-6">
            {!row && <div className="h-64 animate-pulse rounded-lg bg-muted" aria-label="Henter projekt" />}
            {row && view === "overview" && (
              <OverviewTab row={row} chatAgentId={chatAgentId} agentName={agentName} agents={agents} project={project(row)}
                onMoved={(newId) => router.push(p.projectDetail(newId))} openDiamond={row.hub || row.dia_spec ? () => go("diamond") : null}
                editing={editing} setEditing={setEditing} people={people} rows={rows} />
            )}
            {row && view === "diamond" && <DiamondTab row={row} chatAgentId={chatAgentId} people={people} rows={rows} viewsKey={viewsKey} />}
          </div>
        </div>
      )}
    </div>
  );
}

export default function Page({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  return (
    <Suspense fallback={<div className="p-6"><div className="h-64 animate-pulse rounded-lg bg-muted" /></div>}>
      <ProjectPage id={id} />
    </Suspense>
  );
}
