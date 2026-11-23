"use client";

import { z } from "zod";
import { api } from "@multica/core/api";
import { useWorkspacePaths } from "@multica/core/paths";
import { AppLink } from "@multica/views/navigation";
import { useQuery } from "@tanstack/react-query";
import { AREAS } from "./project-context";

// Beslutningslog («Beslutninger»): the decisions that hold, each with the chain of decisions it
// replaced, from the project's typed memory (MATA-264). The posts are files in the project's hub,
// written by matas-ai-automation/memory/memory.py from released hub notes only; the server reads
// them and shows them to the diamond's owners, and to everyone when the whole project is shared.
// Nothing is edited here: a person confirms or replaces a post with memory.py.

const post = z.object({
  id: z.string(),
  type: z.string(),
  text: z.string(),
  area: z.string().optional(),
  said_at: z.string(),
  speaker_role: z.string().optional(),
  speaker: z.string().optional(),
  source: z.string(),
  source_url: z.string().optional(),
  status: z.string(),
  basis: z.string().optional(),
  supersedes: z.string().optional(),
  confirmed_by: z.string().optional(),
  confirmed_at: z.string().optional(),
  review: z.string().optional(),
});
export type MemoryPost = z.infer<typeof post>;
export const decisionLogSchema = z.object({
  project: z.object({ id: z.string(), title: z.string(), number: z.string().optional() }),
  shared: z.boolean(),
  note: z.string().optional(),
  ask: z.string().optional(),
  generated_at: z.string().optional(),
  decisions: z.array(post.extend({ chain: z.array(post) })).optional(),
  proposals: z.array(post).optional(),
  reviews: z.array(post).optional(),
});
export type DecisionLog = z.infer<typeof decisionLogSchema>;

const ROLE_LABELS: Record<string, string> = {
  bo: "Business Owner", po: "Product Owner", plo: "Platform Owner", to: "Technical Owner", librarian: "Librarian",
};

/** "plo to" → "Platform Owner, Technical Owner"; someone outside the diamond is said as such. */
export function roleLabel(roles?: string): string {
  const parts = (roles ?? "").split(/\s+/).filter(Boolean);
  if (!parts.length || parts.includes("none")) return "uden rolle i diamanten";
  return parts.map((r) => ROLE_LABELS[r] ?? r).join(", ");
}

const areaLabel = (a?: string) => AREAS.find(([k]) => k === a)?.[1] ?? "—";
const localId = (id: string) => id.split("/").pop() ?? id;

function Source({ p }: { p: MemoryPost }) {
  const paths = useWorkspacePaths();
  if (p.source_url) return <a href={p.source_url} target="_blank" rel="noreferrer" className="underline underline-offset-4">{p.source}</a>;
  return <AppLink href={paths.issueDetail(p.source)} className="underline underline-offset-4">{p.source}</AppLink>;
}

function Meta({ p }: { p: MemoryPost }) {
  return (
    <span className="text-xs text-muted-foreground">
      <span className="tabular-nums">{p.said_at}</span> · {roleLabel(p.speaker_role)}{p.speaker ? ` (${p.speaker})` : ""} · <Source p={p} />
      {p.status === "confirmed" && p.confirmed_by && <> · bekræftet af {ROLE_LABELS[p.confirmed_by] ?? p.confirmed_by} {p.confirmed_at}</>}
    </span>
  );
}

function Id({ id }: { id: string }) {
  return <span className="mr-2 rounded border px-1 py-0.5 font-mono text-xs tabular-nums" title={id}>{localId(id)}</span>;
}

/** The log as people read it; agents get the same through multica_decision_log. */
export function DecisionLogView({ log }: { log: DecisionLog }) {
  if (!log.shared) {
    return (
      <p className="text-sm text-muted-foreground">
        Projektets hukommelse er ikke delt med dig. Den kan ses af diamantens ejere, og af alle når hele projektet er delt med Knowledge Hub.
        {log.ask && <> Spørg {log.ask}.</>}
      </p>
    );
  }
  const decisions = log.decisions ?? [];
  const proposals = log.proposals ?? [];
  const reviews = log.reviews ?? [];
  return (
    <div className="flex flex-col gap-6">
      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-medium">Gældende beslutninger</h3>
        {decisions.length ? (
          <ul className="flex flex-col gap-3">
            {decisions.map((d) => (
              <li key={d.id} className="flex flex-col gap-1 text-sm">
                <div><Id id={d.id} /><span className="mr-2 text-xs text-muted-foreground">{areaLabel(d.area)}</span>{d.text}</div>
                <Meta p={d} />
                {d.chain.length > 0 && (
                  <ul className="ml-4 flex flex-col gap-1 border-l pl-3">
                    {d.chain.map((old) => (
                      <li key={old.id} className="flex flex-col text-sm text-muted-foreground">
                        <span>erstatter <Id id={old.id} /><span className="line-through">{old.text}</span></span>
                        <Meta p={old} />
                      </li>
                    ))}
                  </ul>
                )}
              </li>
            ))}
          </ul>
        ) : <p className="text-sm text-muted-foreground">{log.note ? "Projektet har ingen typet hukommelse endnu." : "Ingen beslutninger endnu."}</p>}
      </section>
      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-medium">Forslag, der venter på den med beslutningsretten</h3>
        {proposals.length ? (
          <ul className="flex flex-col gap-3">
            {proposals.map((p) => (
              <li key={p.id} className="flex flex-col gap-1 text-sm">
                <div><Id id={p.id} /><span className="mr-2 text-xs text-muted-foreground">{areaLabel(p.area)}</span>{p.text}</div>
                <Meta p={p} />
                {p.basis && <span className="text-xs italic text-muted-foreground">{p.basis}</span>}
              </li>
            ))}
          </ul>
        ) : <p className="text-sm text-muted-foreground">—</p>}
      </section>
      {reviews.length > 0 && (
        <section className="flex flex-col gap-2">
          <h3 className="text-sm font-medium">Til afklaring</h3>
          <ul className="flex flex-col gap-1 text-sm">
            {reviews.map((p) => <li key={p.id}><Id id={p.id} />{p.review}</li>)}
          </ul>
        </section>
      )}
      <p className="text-xs text-muted-foreground">
        Bygget ud fra projektets hukommelse (posterne i hubbens <code>memory/</code>, kun fra GDPR-frigivne noter)
        {log.generated_at && <>, senest {log.generated_at.slice(0, 16).replace("T", " ")}</>}. Rettes ikke her: en ejer bekræfter eller erstatter en post.
      </p>
    </div>
  );
}

export function DecisionsTab({ projectId }: { projectId: string }) {
  const { data, error, isLoading } = useQuery({
    queryKey: ["decision-log", projectId],
    queryFn: async () => decisionLogSchema.parse(await api.getProjectDecisionLog(projectId)),
  });
  if (isLoading) return <div className="h-32 animate-pulse rounded-lg bg-muted" aria-label="Henter beslutninger" />;
  if (error || !data) return <p className="text-sm text-[#CF022D]">Beslutningsloggen kunne ikke hentes.</p>;
  return <DecisionLogView log={data} />;
}
