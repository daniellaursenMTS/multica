"use client";

import { Suspense, use, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { useWorkspacePaths } from "@multica/core/paths";
import { AppLink } from "@multica/views/navigation";
import { ProjectDetail } from "@multica/views/projects/components";
import { DiamondTab, OverviewTab, usePortfolio } from "../_portfolio/portfolio";
import { ContextTab, contextAge, parseContext } from "../_portfolio/project-context";
import { DecisionsTab } from "../_portfolio/decision-log";

type View = "overview" | "kanban" | "diamond" | "context" | "decisions";

// A project page: Oversigt (data, tasks, history), Kanban (Multica's own project view), Diamant,
// Kontekst (the project context agents read) and Beslutninger (the decision log from the project's
// typed memory). ClickUp projects that are not moved yet only have Oversigt.
function ProjectPage({ id }: { id: string }) {
  const router = useRouter();
  const params = useSearchParams();
  const p = useWorkspacePaths();
  const { find, project, loading, agents, agentName, chatAgentId, index, rows, viewsKey, me } = usePortfolio();
  // Name suggestions for a new diamond: real people only — never the fictional demo cast.
  // Stakeholders count too, so one person keeps one name whether in a role or beside the diamond.
  const demoHubs = new Set(rows.filter((r) => r.demo && r.hub).map((r) => r.hub as string));
  const people = Array.from(new Set([
    ...(index?.people ?? []).filter((x) => x.roles.some((r) => !demoHubs.has(r.project))).map((x) => x.name),
    ...rows.filter((r) => !r.demo && !r.dia_archived).flatMap((r) => r.dia_spec?.stakeholders?.map((x) => x.name) ?? []),
  ]));
  const row = find(id);
  const projectId = row?.multica_project ?? (row ? null : id);
  const tabs: [View, string][] = [["overview", "Oversigt"]];
  if (projectId) tabs.push(["kanban", "Kanban"]);
  if ((row?.hub || row?.dia_spec) && !row?.dia_archived) tabs.push(["diamond", "Diamant"]);
  if (row?.multica_project) tabs.push(["context", "Kontekst"], ["decisions", "Beslutninger"]);
  const contextStale = contextAge(parseContext(row?.intake_meta?.ctx_spec)).stale;
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
          {row?.number && <span className="rounded border px-1.5 py-0.5 text-sm tabular-nums text-muted-foreground" title="Projektnummer">{row.number}</span>}
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
              {k === "context" && contextStale && <span className="ml-1 text-[#CF022D]" title="Ikke ændret i 30 dage">•</span>}
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
                editing={editing} setEditing={setEditing} people={people} rows={rows} me={me} />
            )}
            {row && view === "diamond" && <DiamondTab row={row} chatAgentId={chatAgentId} people={people} rows={rows} viewsKey={viewsKey} me={me} />}
            {row && view === "context" && <ContextTab row={row} me={me} />}
            {row?.multica_project && view === "decisions" && <DecisionsTab projectId={row.multica_project} />}
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
