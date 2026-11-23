"use client";

import { z } from "zod";
import { api } from "@multica/core/api";
import { useWorkspacePaths } from "@multica/core/paths";
import { AppLink } from "@multica/views/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { AREAS } from "./project-context";

// Beslutningslog («Beslutninger»): the decisions that hold, each with the chain of decisions it
// replaced, from the project's typed memory (MATA-264). The posts are files in the project's hub,
// written by matas-ai-automation/memory/memory.py from released hub notes only; the server reads
// them and shows them to the diamond's owners, and to everyone when the whole project is shared.
// The one thing done here (MATA-282): whoever holds the decision right for a proposal's area
// answers it with their own account, «Bekræft» or «Ikke en beslutning». The server checks who may
// and stores the answer in Multica; memory.py applies it to the hub at the next sync.

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
  confirmed_by_user: z.string().optional(),
  confirmed_via: z.string().optional(),
  declined_by: z.string().optional(),
  declined_by_user: z.string().optional(),
  declined_at: z.string().optional(),
  rev: z.number().optional(),
  review: z.string().optional(),
});
const proposal = post.extend({
  holder: z.object({ name: z.string().optional(), role: z.string().optional(), linked: z.boolean(), reason: z.string().optional() }).optional(),
  can_confirm: z.boolean().optional(),
  confirmation: z.object({ action: z.string(), name: z.string(), role: z.string(), at: z.string(), state: z.string() }).optional(),
});
export type Proposal = z.infer<typeof proposal>;
export type MemoryPost = z.infer<typeof post>;
export const decisionLogSchema = z.object({
  project: z.object({ id: z.string(), title: z.string(), number: z.string().optional() }),
  shared: z.boolean(),
  note: z.string().optional(),
  ask: z.string().optional(),
  generated_at: z.string().optional(),
  decisions: z.array(post.extend({ chain: z.array(post) })).optional(),
  proposals: z.array(proposal).optional(),
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
      {p.status === "confirmed" && p.confirmed_by && (p.confirmed_via === "project-page"
        ? <> · Bekræftet af {p.confirmed_by_user} ({ROLE_LABELS[p.confirmed_by] ?? p.confirmed_by}) {p.confirmed_at} på projektsiden</>
        : <> · bekræftet af {ROLE_LABELS[p.confirmed_by] ?? p.confirmed_by} {p.confirmed_at}</>)}
    </span>
  );
}

function Id({ id }: { id: string }) {
  return <span className="mr-2 rounded border px-1 py-0.5 font-mono text-xs tabular-nums" title={id}>{localId(id)}</span>;
}

export type Answer = { post: string; rev: number; action: "confirm" | "decline" };

const who = (name?: string, role?: string) => `${name ?? "?"} (${ROLE_LABELS[role ?? ""] ?? role ?? "?"})`;
const day = (at: string) => at.slice(0, 10);

/** Who may answer a proposal, what was answered, and the buttons for the one who holds the right. */
function ProposalAnswer({ p, onAnswer, busy }: { p: Proposal; onAnswer?: (a: Answer) => void; busy?: boolean }) {
  const [asking, setAsking] = useState<Answer["action"] | null>(null);
  const c = p.confirmation;
  if (p.declined_at) {
    return <span className="text-xs">Ikke en beslutning ifølge {who(p.declined_by_user, p.declined_by)} {p.declined_at}.</span>;
  }
  if (c && c.state === "pending") {
    return (
      <span className="text-xs">
        {c.action === "confirm" ? <>Bekræftet af {who(c.name, c.role)} {day(c.at)}</> : <>Ikke en beslutning ifølge {who(c.name, c.role)} {day(c.at)}</>}
        <span className="text-muted-foreground"> · opdateres ved næste sync</span>
      </span>
    );
  }
  const stale = c && c.state === "stale" && (
    <span className="text-xs text-muted-foreground">Svaret fra {day(c.at)} gjaldt en ældre udgave af forslaget og tæller ikke længere.</span>
  );
  if (!p.holder?.linked) {
    return <>{stale}<span className="text-xs text-muted-foreground">Ingen kan bekræfte forslaget endnu: {p.holder?.reason ?? "ukendt, hvem der har retten."}</span></>;
  }
  if (!p.can_confirm || !onAnswer || p.rev === undefined) {
    return <>{stale}<span className="text-xs text-muted-foreground">Kan bekræftes af {who(p.holder.name, p.holder.role)}.</span></>;
  }
  const rev = p.rev;
  return (
    <>
      {stale}
      {asking ? (
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <span>{asking === "confirm" ? `Gør ${localId(p.id)} til en beslutning i dit navn?` : `Markér ${localId(p.id)} som ikke en beslutning?`}</span>
          <button type="button" disabled={busy} className={btn} onClick={() => onAnswer({ post: p.id, rev, action: asking })}>
            {asking === "confirm" ? "Ja, bekræft" : "Ja, ikke en beslutning"}
          </button>
          <button type="button" disabled={busy} className={btn} onClick={() => setAsking(null)}>Fortryd</button>
        </div>
      ) : (
        <div className="flex flex-wrap gap-2">
          <button type="button" className={btn} onClick={() => setAsking("confirm")}>Bekræft</button>
          <button type="button" className={btn} onClick={() => setAsking("decline")}>Ikke en beslutning</button>
        </div>
      )}
    </>
  );
}

const btn = "rounded-md border px-2.5 py-1 text-xs font-medium hover:bg-accent disabled:opacity-50";

/** The log as people read it; agents get the same through multica_decision_log. */
export function DecisionLogView({ log, onAnswer, busy, error }: { log: DecisionLog; onAnswer?: (a: Answer) => void; busy?: boolean; error?: string }) {
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
                <ProposalAnswer p={p} onAnswer={onAnswer} busy={busy} />
              </li>
            ))}
          </ul>
        ) : <p className="text-sm text-muted-foreground">—</p>}
        {error && <p className="text-sm text-[#CF022D]">{error}</p>}
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
        {log.generated_at && <>, senest {log.generated_at.slice(0, 16).replace("T", " ")}</>}. Kun den, der har beslutningsretten for et forslags område, kan bekræfte det her, med sin egen konto.
      </p>
    </div>
  );
}

export function DecisionsTab({ projectId }: { projectId: string }) {
  const qc = useQueryClient();
  const key = ["decision-log", projectId];
  const { data, error, isLoading } = useQuery({
    queryKey: key,
    queryFn: async () => decisionLogSchema.parse(await api.getProjectDecisionLog(projectId)),
  });
  const answer = useMutation({
    mutationFn: async (a: Answer) => decisionLogSchema.parse(await api.confirmProjectDecision(projectId, a)),
    onSuccess: (log) => qc.setQueryData(key, log),
    // A refusal (someone else answered, the post changed) is best shown with what holds now.
    onError: () => qc.invalidateQueries({ queryKey: key }),
  });
  if (isLoading) return <div className="h-32 animate-pulse rounded-lg bg-muted" aria-label="Henter beslutninger" />;
  if (error || !data) return <p className="text-sm text-[#CF022D]">Beslutningsloggen kunne ikke hentes.</p>;
  return <DecisionLogView log={data} onAnswer={(a) => answer.mutate(a)} busy={answer.isPending}
    error={answer.error ? `Svaret blev ikke gemt: ${(answer.error as Error).message}` : undefined} />;
}
