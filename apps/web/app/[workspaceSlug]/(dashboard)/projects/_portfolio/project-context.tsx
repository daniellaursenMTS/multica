"use client";

import { useState } from "react";
import { useAuthStore } from "@multica/core/auth";
import { api } from "@multica/core/api";
import type { Issue, IssueMetadata } from "@multica/core/types";
import { useCurrentWorkspace, useWorkspacePaths } from "@multica/core/paths";
import { memberListOptions } from "@multica/core/workspace/queries";
import { AppLink } from "@multica/views/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { Row } from "./portfolio";

// Projektkontekst («Kontekst»): the fixed facts agents look up before they act on a project. Stored
// as one JSON value, `ctx_spec`, on the project's intake issue next to `dia_spec`; the portfolio's
// own context sits on the one issue marked `portfolio_context`. The server validates it, sets
// updated_at/updated_by and only lets the diamond's owners and workspace admins write it (the
// portfolio: admins only). sync_diamonds.py copies it to roster.json and the hub's README and
// marks `ctx_synced` with the updated_at it applied. Agents propose changes as tasks in review.
// «Lav udkast» (MATA-266) fills the form's empty fields from what Multica can derive, with a source
// per value and the questions only the owners can answer; nothing is saved until they press Gem.

export const AREAS = [
  ["business", "Forretning"], ["product", "Produkt"], ["platform", "Platform"], ["technical", "Teknik"], ["steerco", "Styregruppe"],
] as const;
export type Area = (typeof AREAS)[number][0];
export const SOURCE_TYPES = [
  ["clickup", "ClickUp-liste"], ["sharepoint", "SharePoint-mappe"], ["teams", "Teams-kanal"], ["repo", "Repo"],
  ["figma", "Figma"], ["multica", "Multica-projekt"], ["other", "Andet"],
] as const;
export type SourceType = (typeof SOURCE_TYPES)[number][0];
export interface Source { type: SourceType; name: string; url?: string }
export interface ProjectContext {
  goals: string[];
  priorities: string[];
  milestones: { date: string; what: string }[];
  decision_rights: Partial<Record<Area, string>>;
  sources: Source[];
  not_sources: Source[];
  watch_for: string[];
  terms: { term: string; meaning: string }[];
  /** The portfolio context only: who owns the portfolio. */
  owner?: string;
  updated_at?: string;
  updated_by?: string;
}
export const STALE_DAYS = 30;
const empty = (): ProjectContext => ({
  goals: [], priorities: [], milestones: [], decision_rights: {}, sources: [], not_sources: [], watch_for: [], terms: [],
});

export function parseContext(raw: unknown): ProjectContext | null {
  if (typeof raw !== "string") return null;
  try {
    const c = JSON.parse(raw);
    return c && typeof c === "object" && Array.isArray(c.goals) ? { ...empty(), ...c } : null;
  } catch {
    return null;
  }
}

/** The checks the server makes, so the form can say what is wrong before saving. */
export function contextProblem(c: ProjectContext): string | null {
  const goals = c.goals.filter((g) => g.trim());
  if (goals.length < 1 || goals.length > 3) return "Skriv 1–3 mål, én sætning hver.";
  if (c.priorities.filter((p) => p.trim()).length > 3) return "Højst tre prioriteter.";
  for (const m of c.milestones) {
    if (!/^\d{4}-\d{2}-\d{2}$/.test(m.date) || !m.what.trim()) return "Hver milepæl skal have en dato og hvad der skal være klar.";
  }
  for (const s of c.sources) {
    if (!s.name.trim() || !/^https?:\/\/\S+$/.test(s.url?.trim() ?? "")) return "Hver kilde skal have et navn og et link (https://…).";
  }
  for (const s of c.not_sources) {
    if (!s.name.trim() || (s.url?.trim() && !/^https?:\/\/\S+$/.test(s.url.trim()))) return "En «ikke kilde» skal have et navn; et link skal starte med https://.";
  }
  for (const t of c.terms) if (!t.term.trim() || !t.meaning.trim()) return "Hver term skal have en forklaring.";
  return null;
}

/** Days since the context last changed, and whether that is stale (30 days or more). */
export function contextAge(c: ProjectContext | null, now = new Date()): { days: number | null; stale: boolean } {
  const at = c?.updated_at ? Date.parse(c.updated_at) : NaN;
  if (Number.isNaN(at)) return { days: null, stale: false };
  const days = Math.floor((now.getTime() - at) / 86_400_000);
  return { days, stale: days >= STALE_DAYS };
}

/** A draft from Multica (GET /api/projects/{id}/context-draft): what it could derive, each value
 * with its source, what stays empty and why, and the questions only the owners can answer. */
export interface ContextDraft {
  draft: ProjectContext;
  sources: { field: string; value: string; source: string }[];
  empty: { field: string; why: string }[];
  questions: { field: string; text: string; hints?: string[] }[];
  left_out: { field: string; value: string; source: string }[];
}

export function parseDraft(raw: unknown): ContextDraft | null {
  const d = raw as Partial<ContextDraft> | null;
  if (!d || typeof d !== "object" || !d.draft || !Array.isArray(d.draft.goals)) return null;
  return {
    draft: { ...empty(), ...d.draft }, sources: d.sources ?? [], empty: d.empty ?? [], questions: d.questions ?? [], left_out: d.left_out ?? [],
  };
}

/** The draft fills only what the form has empty; what the owners wrote is kept. */
export function mergeDraft(current: ProjectContext | null, draft: ProjectContext): ProjectContext {
  const c = { ...empty(), ...(current ?? {}) };
  const pick = <T,>(mine: T[], theirs: T[]) => (mine.length ? mine : theirs);
  const rights = { ...draft.decision_rights };
  for (const [k, v] of Object.entries(c.decision_rights)) if (v?.trim()) rights[k as Area] = v;
  return {
    ...c, goals: pick(c.goals, draft.goals), priorities: pick(c.priorities, draft.priorities), milestones: pick(c.milestones, draft.milestones),
    decision_rights: rights, sources: pick(c.sources, draft.sources), not_sources: pick(c.not_sources, draft.not_sources),
    watch_for: pick(c.watch_for, draft.watch_for), terms: pick(c.terms, draft.terms),
    ...(draft.owner !== undefined ? { owner: c.owner?.trim() ? c.owner : draft.owner } : {}),
  };
}

const FIELD_LABELS: Record<string, string> = {
  goals: "Mål", priorities: "Prioriteter", milestones: "Milepæle", decision_rights: "Beslutningsret", sources: "Kilder",
  not_sources: "Ikke kilder", watch_for: "Hold øje med", terms: "Termer", owner: "Ejer",
};

/** «decision_rights.business» → «Beslutningsret, Forretning»; «sources[2]» → «Kilder». */
export function fieldLabel(field: string): string {
  const [base, rest] = field.split(/[.[]/, 2);
  const area = field.startsWith("decision_rights.") ? AREAS.find(([k]) => k === rest)?.[1] : undefined;
  return (FIELD_LABELS[base ?? ""] ?? field) + (area ? `, ${area}` : "");
}

/** Who may edit a project's context; the server checks the same. */
export function canEditContext(row: Row, opts: { me: string | null; email: string | null; admin: boolean }): boolean {
  if (opts.admin) return true;
  if (!row.dia_spec || row.dia_spec.archived) return false;
  const email = opts.email?.toLowerCase();
  return Object.values(row.dia_spec.roles).some((r) => !!r.person && ((!!opts.me && r.person === opts.me) || (!!email && r.email?.toLowerCase() === email)));
}

/** Workspace owner or admin: may edit any context, the portfolio context and existing diamonds. */
export function useIsAdmin() {
  const workspace = useCurrentWorkspace();
  const myId = useAuthStore((s) => s.user?.id ?? null);
  const { data: members } = useQuery({ ...memberListOptions(workspace?.id ?? ""), enabled: !!workspace?.id });
  const role = members?.find((m) => m.user_id === myId)?.role;
  return role === "owner" || role === "admin";
}

const trimmed = (c: ProjectContext): ProjectContext => {
  const t = (s: string) => s.trim();
  const src = (s: Source): Source => ({ type: s.type, name: t(s.name), ...(s.url?.trim() ? { url: t(s.url) } : {}) });
  return {
    goals: c.goals.map(t).filter(Boolean), priorities: c.priorities.map(t).filter(Boolean),
    milestones: c.milestones.map((m) => ({ date: t(m.date), what: t(m.what) })),
    decision_rights: Object.fromEntries(Object.entries(c.decision_rights).map(([k, v]) => [k, t(v ?? "")]).filter(([, v]) => v)),
    sources: c.sources.map(src), not_sources: c.not_sources.map(src),
    watch_for: c.watch_for.map(t).filter(Boolean), terms: c.terms.map((x) => ({ term: t(x.term), meaning: t(x.meaning) })),
    ...(c.owner !== undefined ? { owner: t(c.owner) } : {}),
  };
};

const input = "rounded-md border bg-background px-2 py-1.5 text-sm";
const btn = "rounded-md border px-3 py-1.5 text-sm font-medium hover:bg-accent disabled:opacity-50";
const linkBtn = "text-xs text-muted-foreground underline underline-offset-4 hover:text-foreground";

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="flex flex-col gap-1.5">
      <h3 className="text-sm font-medium">{title}</h3>
      {children}
    </section>
  );
}

function None() {
  return <p className="text-sm text-muted-foreground">—</p>;
}

function SourceList({ items }: { items: Source[] }) {
  if (!items.length) return <None />;
  const label = (t: SourceType) => SOURCE_TYPES.find(([k]) => k === t)?.[1] ?? t;
  return (
    <ul className="flex flex-col gap-1 text-sm">
      {items.map((s, i) => (
        <li key={i}>
          <span className="text-muted-foreground">{label(s.type)}: </span>
          {s.url ? <a href={s.url} target="_blank" rel="noreferrer" className="underline underline-offset-4">{s.name}</a> : s.name}
        </li>
      ))}
    </ul>
  );
}

/** The context as people read it; the same fields agents get as JSON. */
export function ContextView({ ctx, portfolio = false }: { ctx: ProjectContext; portfolio?: boolean }) {
  const list = (xs: string[], ordered = false) => {
    if (!xs.length) return <None />;
    const L = ordered ? "ol" : "ul";
    return <L className={`${ordered ? "list-decimal" : "list-disc"} pl-5 text-sm leading-relaxed`}>{xs.map((x, i) => <li key={i}>{x}</li>)}</L>;
  };
  return (
    <div className="grid gap-5 md:grid-cols-2">
      {portfolio && <Section title="Ejer"><p className="text-sm">{ctx.owner || "—"}</p></Section>}
      <Section title="Mål">{list(ctx.goals)}</Section>
      <Section title="Prioriteter">{list(ctx.priorities, true)}</Section>
      <Section title="Milepæle">
        {ctx.milestones.length ? (
          <ul className="text-sm">{[...ctx.milestones].sort((a, b) => a.date.localeCompare(b.date)).map((m, i) => (
            <li key={i}><span className="tabular-nums text-muted-foreground">{m.date}</span> · {m.what}</li>
          ))}</ul>
        ) : <None />}
      </Section>
      <Section title="Beslutningsret">
        <dl className="grid grid-cols-[8rem_1fr] gap-x-3 gap-y-1 text-sm">
          {AREAS.map(([k, label]) => (
            <div key={k} className="contents"><dt className="text-muted-foreground">{label}</dt><dd>{ctx.decision_rights[k] || "—"}</dd></div>
          ))}
        </dl>
      </Section>
      <Section title="Kilder"><SourceList items={ctx.sources} /></Section>
      <Section title="Ikke kilder"><SourceList items={ctx.not_sources} /></Section>
      <Section title="Hold øje med">{list(ctx.watch_for)}</Section>
      <Section title="Termer">
        {ctx.terms.length ? (
          <dl className="grid grid-cols-[8rem_1fr] gap-x-3 gap-y-1 text-sm">
            {ctx.terms.map((t, i) => <div key={i} className="contents"><dt className="font-medium">{t.term}</dt><dd>{t.meaning}</dd></div>)}
          </dl>
        ) : <None />}
      </Section>
    </div>
  );
}

/** «krævet», «vigtigt» or «valgfrit» after a field's name. */
function Tag({ tag }: { tag?: string }) {
  if (!tag) return null;
  return <span className={`ml-1.5 text-xs font-normal ${tag === "valgfrit" ? "text-muted-foreground" : "text-[#CF022D]"}`}>{tag}</span>;
}

/** One text per line, at most `max` lines. */
function Lines({ id, label, tag, value, max, onChange, hint }: { id: string; label: string; tag?: string; value: string[]; max: number; onChange: (v: string[]) => void; hint?: string }) {
  const rows = [...value, ...Array(Math.max(0, max - value.length)).fill("")].slice(0, max);
  return (
    <fieldset className="flex flex-col gap-1.5">
      <legend className="mb-1 text-sm font-medium">{label}<Tag tag={tag} /></legend>
      {rows.map((v, i) => (
        <input key={i} id={`${id}-${i}`} value={v} aria-label={`${label} ${i + 1}`} className={input}
          onChange={(e) => onChange(rows.map((x, j) => (j === i ? e.target.value : x)))} />
      ))}
      {hint && <span className="text-xs text-muted-foreground">{hint}</span>}
    </fieldset>
  );
}

function SourceRows({ label, tag, value, onChange, linkRequired }: { label: string; tag?: string; value: Source[]; onChange: (v: Source[]) => void; linkRequired: boolean }) {
  const set = (i: number, patch: Partial<Source>) => onChange(value.map((s, j) => (j === i ? { ...s, ...patch } : s)));
  return (
    <fieldset className="flex flex-col gap-1.5">
      <legend className="mb-1 text-sm font-medium">{label}<Tag tag={tag} /></legend>
      {value.map((s, i) => (
        <div key={i} className="grid gap-2 sm:grid-cols-[10rem_1fr_1fr_auto]">
          <select value={s.type} onChange={(e) => set(i, { type: e.target.value as SourceType })} aria-label={`${label} ${i + 1}, type`} className={input}>
            {SOURCE_TYPES.map(([k, l]) => <option key={k} value={k}>{l}</option>)}
          </select>
          <input value={s.name} onChange={(e) => set(i, { name: e.target.value })} placeholder="Navn" aria-label={`${label} ${i + 1}, navn`} className={input} />
          <input value={s.url ?? ""} onChange={(e) => set(i, { url: e.target.value })} placeholder={linkRequired ? "Link (https://…)" : "Link (valgfrit)"}
            aria-label={`${label} ${i + 1}, link`} className={input} />
          <button type="button" className={linkBtn} onClick={() => onChange(value.filter((_, j) => j !== i))}>Fjern</button>
        </div>
      ))}
      <button type="button" className={`${linkBtn} self-start`} onClick={() => onChange([...value, { type: "teams", name: "", url: "" }])}>+ Tilføj</button>
    </fieldset>
  );
}

/** What the draft filled in and from where, and what only the owners can answer. */
function DraftNotes({ draft }: { draft: ContextDraft }) {
  return (
    <section className="flex flex-col gap-3 rounded-md border border-dashed px-3 py-2 text-sm" aria-label="Udkast fra Multica">
      <p className="text-muted-foreground">
        Udkast fra det, Multica ved om projektet. Intet er gemt. Ret det, der er forkert, svar på spørgsmålene, og tryk Gem.
        Felter, der ikke kunne udledes, står tomme.
      </p>
      {draft.sources.length > 0 && (
        <div>
          <h3 className="font-medium">Udfyldt, med kilde</h3>
          <ul className="list-disc pl-5">
            {draft.sources.map((s, i) => <li key={i}><span className="text-muted-foreground">{fieldLabel(s.field)}:</span> {s.value} <span className="text-muted-foreground">— {s.source}</span></li>)}
          </ul>
        </div>
      )}
      {draft.questions.length > 0 && (
        <div>
          <h3 className="font-medium">Det kan kun I svare på</h3>
          <ul className="list-disc pl-5">
            {draft.questions.map((q, i) => (
              <li key={i}>{q.text}{q.hints?.length ? <span className="block text-xs text-muted-foreground">{q.hints.join(" · ")}</span> : null}</li>
            ))}
          </ul>
        </div>
      )}
      {draft.left_out.length > 0 && (
        <p className="text-xs text-muted-foreground">Ikke taget med, fordi det står i porteføljekonteksten: {draft.left_out.map((x) => x.value).join("; ")}</p>
      )}
    </section>
  );
}

export function ContextForm({ initial, portfolio = false, onSave, onCancel, saving, error, draft }: {
  initial: ProjectContext | null; portfolio?: boolean; saving: boolean; error: Error | null;
  onSave: (c: ProjectContext) => void; onCancel: () => void; draft?: ContextDraft | null;
}) {
  const [c, setC] = useState<ProjectContext>(() => ({ ...empty(), ...(portfolio ? { owner: "" } : {}), ...(initial ?? {}) }));
  const put = <K extends keyof ProjectContext>(k: K, v: ProjectContext[K]) => setC((x) => ({ ...x, [k]: v }));
  const clean = trimmed(c);
  const problem = contextProblem(clean);
  return (
    <form className="flex flex-col gap-5 rounded-lg border p-4" onSubmit={(e) => { e.preventDefault(); if (!problem) onSave(clean); }}>
      {draft && <DraftNotes draft={draft} />}
      <p className="text-xs text-muted-foreground">Kun mål er krævet. Mål, beslutningsret og kilder er det vigtigste for agenterne; resten må gerne stå tomt.</p>
      {portfolio && (
        <label className="flex flex-col gap-1 text-sm">
          <span className="font-medium">Ejer af porteføljen</span>
          <input id="ctx-owner" value={c.owner ?? ""} onChange={(e) => put("owner", e.target.value)} className={input} />
        </label>
      )}
      <div className="grid gap-5 md:grid-cols-2">
        <Lines id="ctx-goal" label="Mål" tag="krævet" value={c.goals} max={3} onChange={(v) => put("goals", v)} hint="1–3 mål for perioden, én sætning hver." />
        <Lines id="ctx-priority" label="Prioriteter" tag="valgfrit" value={c.priorities} max={3} onChange={(v) => put("priorities", v)} hint="Top 3 lige nu, vigtigste først." />
      </div>
      <fieldset className="flex flex-col gap-1.5">
        <legend className="mb-1 text-sm font-medium">Milepæle<Tag tag="valgfrit" /></legend>
        {c.milestones.map((m, i) => (
          <div key={i} className="grid gap-2 sm:grid-cols-[11rem_1fr_auto]">
            <input type="date" value={m.date} aria-label={`Milepæl ${i + 1}, dato`} className={input}
              onChange={(e) => put("milestones", c.milestones.map((x, j) => (j === i ? { ...x, date: e.target.value } : x)))} />
            <input value={m.what} placeholder="Hvad skal være klar" aria-label={`Milepæl ${i + 1}, hvad`} className={input}
              onChange={(e) => put("milestones", c.milestones.map((x, j) => (j === i ? { ...x, what: e.target.value } : x)))} />
            <button type="button" className={linkBtn} onClick={() => put("milestones", c.milestones.filter((_, j) => j !== i))}>Fjern</button>
          </div>
        ))}
        <button type="button" className={`${linkBtn} self-start`} onClick={() => put("milestones", [...c.milestones, { date: "", what: "" }])}>+ Tilføj milepæl</button>
      </fieldset>
      <fieldset className="flex flex-col gap-1.5">
        <legend className="mb-1 text-sm font-medium">Beslutningsret: hvem afgør hvad<Tag tag="vigtigt" /></legend>
        {AREAS.map(([k, label]) => (
          <label key={k} className="grid gap-2 text-sm sm:grid-cols-[10rem_1fr] sm:items-center">
            <span className="text-muted-foreground">{label}</span>
            <input id={`ctx-right-${k}`} value={c.decision_rights[k] ?? ""} className={input} placeholder="Fx Business Owner (navn), eller styregruppen"
              onChange={(e) => put("decision_rights", { ...c.decision_rights, [k]: e.target.value })} />
          </label>
        ))}
      </fieldset>
      <SourceRows label="Kilder" tag="vigtigt" value={c.sources} linkRequired onChange={(v) => put("sources", v)} />
      <SourceRows label="Ikke kilder (lignende navne, der ikke hører til)" tag="valgfrit" value={c.not_sources} linkRequired={false} onChange={(v) => put("not_sources", v)} />
      <fieldset className="flex flex-col gap-1.5">
        <legend className="mb-1 text-sm font-medium">Hold øje med<Tag tag="valgfrit" /></legend>
        {c.watch_for.map((w, i) => (
          <div key={i} className="grid gap-2 sm:grid-cols-[1fr_auto]">
            <input value={w} aria-label={`Hold øje med ${i + 1}`} className={input}
              onChange={(e) => put("watch_for", c.watch_for.map((x, j) => (j === i ? e.target.value : x)))} />
            <button type="button" className={linkBtn} onClick={() => put("watch_for", c.watch_for.filter((_, j) => j !== i))}>Fjern</button>
          </div>
        ))}
        {c.watch_for.length < 8 && (
          <button type="button" className={`${linkBtn} self-start`} onClick={() => put("watch_for", [...c.watch_for, ""])}>+ Tilføj risikoområde</button>
        )}
        <span className="text-xs text-muted-foreground">Kendte risikoområder, fx «estimater er ofte for optimistiske».</span>
      </fieldset>
      <fieldset className="flex flex-col gap-1.5">
        <legend className="mb-1 text-sm font-medium">Termer<Tag tag="valgfrit" /></legend>
        {c.terms.map((t, i) => (
          <div key={i} className="grid gap-2 sm:grid-cols-[12rem_1fr_auto]">
            <input value={t.term} placeholder="Ord eller forkortelse" aria-label={`Term ${i + 1}`} className={input}
              onChange={(e) => put("terms", c.terms.map((x, j) => (j === i ? { ...x, term: e.target.value } : x)))} />
            <input value={t.meaning} placeholder="Betydning" aria-label={`Term ${i + 1}, betydning`} className={input}
              onChange={(e) => put("terms", c.terms.map((x, j) => (j === i ? { ...x, meaning: e.target.value } : x)))} />
            <button type="button" className={linkBtn} onClick={() => put("terms", c.terms.filter((_, j) => j !== i))}>Fjern</button>
          </div>
        ))}
        <button type="button" className={`${linkBtn} self-start`} onClick={() => put("terms", [...c.terms, { term: "", meaning: "" }])}>+ Tilføj term</button>
      </fieldset>
      {problem && <p className="text-sm text-[#CF022D]">{problem}</p>}
      {error && <p className="text-sm text-[#CF022D]">Kunne ikke gemme: {error.message}</p>}
      <div className="flex gap-2">
        <button type="submit" disabled={!!problem || saving} className="rounded-md bg-foreground px-3 py-1.5 text-sm font-medium text-background disabled:opacity-50">
          {saving ? "Gemmer …" : "Gem kontekst"}
        </button>
        <button type="button" onClick={onCancel} className="rounded-md border px-3 py-1.5 text-sm">Annullér</button>
      </div>
    </form>
  );
}

function Updated({ ctx, synced, error }: { ctx: ProjectContext; synced: unknown; error: unknown }) {
  const { days, stale } = contextAge(ctx);
  return (
    <div className="flex flex-col gap-1 text-xs text-muted-foreground">
      <p>
        Sidst ændret {ctx.updated_at ? new Date(ctx.updated_at).toLocaleDateString("da-DK", { day: "numeric", month: "short", year: "numeric" }) : "—"}
        {ctx.updated_by && ` af ${ctx.updated_by}`}
        {stale && <strong className="ml-2 font-medium text-[#CF022D]">Ikke ændret i {days} dage. Er den stadig rigtig?</strong>}
      </p>
      {typeof error === "string" && <p role="alert" className="text-[#CF022D]">Gemt i Multica, men ikke synkroniseret til hubben: {error}</p>}
      {typeof error !== "string" && synced !== ctx.updated_at && <p role="status">Gemt. Synkroniseres til hubben (op til et minut) …</p>}
    </div>
  );
}

/** Open proposals from agents: tasks in review for the product owner. */
function Proposals({ projectId }: { projectId: string }) {
  const p = useWorkspacePaths();
  const { data } = useQuery({
    queryKey: ["portfolio", "ctx-proposals", projectId],
    queryFn: async () => (await api.listIssues({ project_id: projectId, metadata: { ctx_proposal: true }, limit: 50 })).issues,
  });
  const open = (data ?? []).filter((i: Issue) => i.status !== "done" && i.status !== "cancelled");
  if (!open.length) return null;
  return (
    <section className="flex flex-col gap-1.5 rounded-md border px-3 py-2" aria-labelledby="ctx-proposals">
      <h3 id="ctx-proposals" className="text-sm font-medium">Forslag fra agenter ({open.length})</h3>
      <ul className="text-sm">
        {open.map((i) => <li key={i.id}><AppLink href={p.issueDetail(i.id)} className="underline underline-offset-4">{i.identifier}</AppLink> {i.title}</li>)}
      </ul>
      <p className="text-xs text-muted-foreground">Agenter retter ikke konteksten selv. Er du enig i et forslag, så ret konteksten her og luk opgaven.</p>
    </section>
  );
}

/** Kontekst-fanen på projektsiden. */
export function ContextTab({ row, me }: { row: Row; me: string | null }) {
  const qc = useQueryClient();
  const email = useAuthStore((s) => s.user?.email ?? null);
  const admin = useIsAdmin();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<ContextDraft | null>(null);
  const meta: IssueMetadata | undefined = row.intake_meta;
  const ctx = parseContext(meta?.ctx_spec);
  const makeDraft = useMutation({
    mutationFn: async () => parseDraft(await api.getProjectContextDraft(row.multica_project ?? "")),
    onSuccess: (d) => { setDraft(d); setEditing(true); },
  });
  const mayEdit = !!row.multica_project && canEditContext(row, { me, email, admin });
  const save = useMutation({
    mutationFn: async (c: ProjectContext) => {
      let intake = row.intake_issue ?? null;
      if (!intake) {
        intake = (await api.createIssue({ title: `Intake — ${row.name}`, project_id: row.multica_project ?? undefined, status: "done",
          description: "Portfolio-felter og diamant for projektet. Redigeres fra projektsiden i Multica." })).id;
        await api.setIssueMetadataKey(intake, "pf_intake", true);
      }
      await api.setIssueMetadataKey(intake, "ctx_spec", JSON.stringify(c));
    },
    onSuccess: () => { setEditing(false); setDraft(null); qc.invalidateQueries({ queryKey: ["portfolio", "live"] }); },
  });

  return (
    <section className="flex flex-col gap-4" aria-label="Projektkontekst">
      <p className="max-w-[75ch] text-sm text-muted-foreground">
        Faste oplysninger, som agenterne slår op, før de handler: mål, prioriteter, hvem der afgør hvad, og hvilke kilder der gælder.
        Diamantens ejere retter dem her; agenter kan kun foreslå ændringer.
      </p>
      {row.multica_project && <Proposals projectId={row.multica_project} />}
      {editing ? (
        <ContextForm initial={draft ? mergeDraft(ctx, draft.draft) : ctx} draft={draft} saving={save.isPending} error={save.error as Error | null}
          onSave={(c) => save.mutate(c)} onCancel={() => { setEditing(false); setDraft(null); }} />
      ) : ctx ? (
        <>
          <Updated ctx={ctx} synced={meta?.ctx_synced} error={meta?.ctx_sync_error} />
          <ContextView ctx={ctx} />
        </>
      ) : (
        <p className="rounded-md border border-dashed px-4 py-3 text-sm text-muted-foreground">Projektet har ingen kontekst endnu.</p>
      )}
      {!editing && (mayEdit ? (
        <div className="flex flex-col gap-1.5">
          <div className="flex gap-2">
            <button type="button" className={btn} onClick={() => setEditing(true)}>{ctx ? "Ret kontekst" : "Skriv kontekst"}</button>
            <button type="button" className={btn} disabled={makeDraft.isPending} onClick={() => makeDraft.mutate()}>
              {makeDraft.isPending ? "Laver udkast …" : "Lav udkast"}
            </button>
          </div>
          <p className="text-xs text-muted-foreground">«Lav udkast» udfylder de tomme felter ud fra diamanten, projektets ressourcer og hukommelse, med kilde på hvert felt. Du retter og gemmer selv.</p>
          {makeDraft.error && <p className="text-sm text-[#CF022D]">Kunne ikke lave udkast: {(makeDraft.error as Error).message}</p>}
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">Kun projektets diamant (Business, Product, Platform og Technical Owner) og workspace-admins kan rette konteksten.</p>
      ))}
    </section>
  );
}

/** Porteføljekontekst: the same fields once for the whole portfolio, edited by workspace admins. */
export function PortfolioContext() {
  const qc = useQueryClient();
  const workspace = useCurrentWorkspace();
  const admin = useIsAdmin();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<ContextDraft | null>(null);
  const makeDraft = useMutation({
    mutationFn: async () => parseDraft(await api.getProjectContextDraft("portfolio")),
    onSuccess: (d) => { setDraft(d); setEditing(true); },
  });
  const { data: issues, isLoading } = useQuery({
    queryKey: ["portfolio", "context", workspace?.id],
    enabled: !!workspace?.id,
    queryFn: async () => (await api.listIssues({ metadata: { portfolio_context: true }, limit: 2 })).issues,
  });
  const issue = issues?.[0] ?? null;
  const ctx = parseContext(issue?.metadata.ctx_spec);
  const save = useMutation({
    mutationFn: async (c: ProjectContext) => {
      let id = issue?.id ?? null;
      if (!id) {
        id = (await api.createIssue({ title: "Porteføljekontekst", status: "done",
          description: "Porteføljens kontekst: tværgående mål, prioriteter og ejer. Redigeres fra Projekter i Multica." })).id;
        await api.setIssueMetadataKey(id, "portfolio_context", true);
      }
      await api.setIssueMetadataKey(id, "ctx_spec", JSON.stringify(c));
    },
    onSuccess: () => { setEditing(false); setDraft(null); qc.invalidateQueries({ queryKey: ["portfolio", "context"] }); },
  });
  if (isLoading) return <div className="h-32 animate-pulse rounded-lg bg-muted" aria-label="Henter porteføljekontekst" />;
  if ((issues?.length ?? 0) > 1) return <p role="alert" className="text-sm text-[#CF022D]">Porteføljekonteksten ligger på mere end ét issue. En admin må rydde op.</p>;
  return (
    <section className="flex flex-col gap-4" aria-label="Porteføljekontekst">
      <p className="max-w-[75ch] text-sm text-muted-foreground">
        Tværgående mål og prioriteter for hele porteføljen, og hvem der ejer den. Porteføljevagten og agenterne læser den sammen med
        projekternes egen kontekst.
      </p>
      {editing ? (
        <ContextForm initial={draft ? mergeDraft(ctx, draft.draft) : ctx} draft={draft} portfolio saving={save.isPending} error={save.error as Error | null}
          onSave={(c) => save.mutate(c)} onCancel={() => { setEditing(false); setDraft(null); }} />
      ) : ctx ? (
        <>
          {issue && <Updated ctx={ctx} synced={issue.metadata.ctx_synced} error={issue.metadata.ctx_sync_error} />}
          <ContextView ctx={ctx} portfolio />
        </>
      ) : (
        <p className="rounded-md border border-dashed px-4 py-3 text-sm text-muted-foreground">Porteføljen har ingen kontekst endnu.</p>
      )}
      {!editing && (admin ? (
        <div className="flex flex-col gap-1.5">
          <div className="flex gap-2">
            <button type="button" className={btn} onClick={() => setEditing(true)}>{ctx ? "Ret porteføljekontekst" : "Skriv porteføljekontekst"}</button>
            <button type="button" className={btn} disabled={makeDraft.isPending} onClick={() => makeDraft.mutate()}>
              {makeDraft.isPending ? "Laver udkast …" : "Lav udkast"}
            </button>
          </div>
          <p className="text-xs text-muted-foreground">«Lav udkast» foreslår de termer og risici, der gælder alle projekter, med kilde. Det, der står her, gentages ikke i projekternes kontekst.</p>
          {makeDraft.error && <p className="text-sm text-[#CF022D]">Kunne ikke lave udkast: {(makeDraft.error as Error).message}</p>}
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">Kun workspace-admins kan rette porteføljekonteksten.</p>
      ))}
    </section>
  );
}
