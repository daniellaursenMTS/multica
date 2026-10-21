"use client";

import { Suspense, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { useCurrentWorkspace, useWorkspacePaths } from "@multica/core/paths";
import { useAuthStore } from "@multica/core/auth";
import { memberListOptions } from "@multica/core/workspace/queries";
import { useQuery } from "@tanstack/react-query";
import { ProjectsPage } from "@multica/views/projects/components";
import { Diagram, Overview, ProjectForm, usePortfolio } from "./_portfolio/portfolio";

// One list of projects: Multica projects grouped by status, with Portfolio fields and ◇ for a
// diamond, then ClickUp intake that has not been moved yet. Multica's own list stays one click away.
function ProjectList() {
  const router = useRouter();
  const params = useSearchParams();
  const p = useWorkspacePaths();
  const { pf, index, rows, me, isMine, error, loading } = usePortfolio();
  const workspace = useCurrentWorkspace();
  const myId = useAuthStore((s) => s.user?.id ?? null);
  const { data: members } = useQuery({ ...memberListOptions(workspace?.id ?? ""), enabled: !!workspace?.id });
  const isAdmin = !!members?.some((m) => m.user_id === myId && (m.role === "owner" || m.role === "admin"));
  const [creating, setCreating] = useState(false);
  const mine = params.get("filter") === "mine";
  const open = (id: string) => router.push(p.projectDetail(id));
  const setFilter = (v: string | null) => router.push(v ? `?filter=${v}` : "?");

  if (params.get("view") === "classic") return <ProjectsPage />;

  const shown = mine ? rows.filter(isMine) : rows;
  const multica = shown.filter((r) => r.source !== "clickup");
  const clickup = shown.filter((r) => r.source === "clickup");
  const meSlug = index?.people.find((x) => x.name === me)?.slug;

  return (
    <div className="h-full min-h-0 flex-1 overflow-y-auto">
      <div className="flex flex-col gap-6 p-6">
        <header className="flex flex-wrap items-end justify-between gap-4">
          <div className="min-w-0">
            <h1 className="text-lg font-semibold">Projekter</h1>
            <p className="text-sm text-muted-foreground">
              Klik på et projekt for oversigt, kanban og diamant. ◇ betyder, at projektet har en diamant og projekt-chat.
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <nav className="flex gap-1 rounded-md border p-0.5 text-sm" aria-label="Filter">
              {([[null, "Alle"], ["mine", "Mine projekter"]] as const).map(([k, label]) => (
                <button key={label} type="button" aria-pressed={(k === "mine") === mine} onClick={() => setFilter(k)}
                  className={`rounded px-3 py-1 ${(k === "mine") === mine ? "bg-accent font-medium" : "text-muted-foreground hover:bg-accent/60"}`}>
                  {label}
                </button>
              ))}
            </nav>
            {isAdmin && (
              <a href={p.team()} className="rounded-md border px-3 py-1.5 text-sm font-medium hover:bg-accent">Team</a>
            )}
            {!creating && (
              <button type="button" onClick={() => setCreating(true)} className="rounded-md border px-3 py-1.5 text-sm font-medium hover:bg-accent">
                + Nyt projekt
              </button>
            )}
          </div>
        </header>

        {creating && <ProjectForm mode="create" onCancel={() => setCreating(false)} onDone={(id) => { setCreating(false); open(id); }} />}

        {mine && meSlug && <Diagram url={`/diamond-views/person-${meSlug}.svg`} label="dine projekter" />}

        {loading && <div className="h-64 animate-pulse rounded-lg bg-muted" aria-label="Henter projekter" />}
        {!loading && (multica.length ? <Overview rows={multica} open={open} /> : (
          <p className="text-sm text-muted-foreground">{mine ? "Du er ikke med i nogen projekter endnu." : "Ingen projekter endnu."}</p>
        ))}

        {clickup.length > 0 && (
          <section className="flex flex-col gap-3 border-t pt-6" aria-labelledby="from-clickup">
            <div>
              <h2 id="from-clickup" className="text-sm font-medium">Fra ClickUp <span className="font-normal text-muted-foreground">· {clickup.length} · kun læsning</span></h2>
              <p className="text-xs text-muted-foreground">
                Intake-projekter, der endnu ikke er flyttet. Åbn et projekt og vælg «Flyt til Multica» for at redigere det og give agenter opgaver.
              </p>
            </div>
            <Overview rows={clickup} open={open} />
          </section>
        )}

        <p className="text-xs text-muted-foreground">
          {error && `${error} · `}
          {pf?.synced_at && `ClickUp synkroniseret ${new Date(pf.synced_at).toLocaleString("da-DK")} (skrivebeskyttet) · `}
          <a href="?view=classic" className="underline underline-offset-4">Multicas standardliste</a>
        </p>
      </div>
    </div>
  );
}

// The page reads the URL's search params, which Next requires behind a Suspense boundary.
export default function Page() {
  return (
    <Suspense fallback={<div className="p-6"><div className="h-64 animate-pulse rounded-lg bg-muted" /></div>}>
      <ProjectList />
    </Suspense>
  );
}
