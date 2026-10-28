"use client";

import { useMemo, useState } from "react";
import { useAuthStore } from "@multica/core/auth";
import { api } from "@multica/core/api";
import { useCurrentWorkspace, useWorkspacePaths } from "@multica/core/paths";
import { memberListOptions } from "@multica/core/workspace/queries";
import type { DashboardUsageDaily } from "@multica/core/types";
import { AppLink } from "@multica/views/navigation";
import { estimateCost } from "@multica/views/runtimes";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { saveCapex, useCapex, usePortfolio, type CapexDay, type CapexItem, type CapexManual } from "../projects/_portfolio/portfolio";

// Capex (admins only): agent token use per Capex number, from Multica's own usage records. A
// project's usage is every agent run on its issues; a project points at a Capex number on its
// intake issue. Tokens = input + output (cache reads are priced, not counted). Kroner are an
// estimate from list prices: runs on a ChatGPT or Claude subscription have no real price per call.
// Manual entries add usage Multica never saw (the Claude Code sessions that built this setup).

// Categorical slots, validated for colour-blind separation on light and dark surfaces. A Capex
// keeps its colour by its place in the register, never by its rank in the chart.
const PALETTE_CSS = `
.capex-root { --cx-0:#2D6CB5; --cx-1:#CF022D; --cx-2:#2A9D8F; --cx-3:#D9932B; --cx-4:#7B5EA7; --cx-5:#5E8C3A;
  --cx-other:#8E959E; --cx-none:#B4BAC1; --cx-noproj:#D2D6DB; }
.dark .capex-root { --cx-0:#3F7BC4; --cx-1:#E0263F; --cx-2:#249C8E; --cx-3:#BC7E1C; --cx-4:#8A6FBF; --cx-5:#629A42;
  --cx-other:#7C838C; --cx-none:#5E646B; --cx-noproj:#464B51; }`;
const SLOTS = 6;

type Period = { key: string; label: string; from: string; to: string };
const iso = (d: Date) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
const MONTHS = ["jan.", "feb.", "mar.", "apr.", "maj", "jun.", "jul.", "aug.", "sep.", "okt.", "nov.", "dec."];

function periods(now = new Date()): Period[] {
  const y = now.getFullYear(), m = now.getMonth();
  const out: Period[] = [{ key: `y:${y}`, label: `${y} indtil nu`, from: `${y}-01-01`, to: iso(now) }];
  const q = Math.floor(m / 3);
  for (let i = 0; i < 4; i++) {
    const qq = ((q - i) % 4 + 4) % 4, yy = y - (q - i < 0 ? 1 : 0);
    out.push({ key: `q:${yy}-${qq + 1}`, label: `${qq + 1}. kvartal ${yy}`, from: iso(new Date(yy, qq * 3, 1)), to: iso(new Date(yy, qq * 3 + 3, 0)) });
  }
  for (let i = 0; i < 12; i++) {
    const d = new Date(y, m - i, 1);
    out.push({ key: `m:${iso(d).slice(0, 7)}`, label: `${MONTHS[d.getMonth()]} ${d.getFullYear()}`, from: iso(d), to: iso(new Date(d.getFullYear(), d.getMonth() + 1, 0)) });
  }
  return out;
}

const nfTokens = new Intl.NumberFormat("da-DK", { notation: "compact", maximumFractionDigits: 1 });
const nfKr = new Intl.NumberFormat("da-DK", { style: "currency", currency: "DKK", maximumFractionDigits: 0 });
const nfPct = new Intl.NumberFormat("da-DK", { style: "percent", maximumFractionDigits: 1 });
const tokensOf = (r: DashboardUsageDaily) => r.input_tokens + r.output_tokens;

interface Line { key: string; number: string; description: string; projects: string[]; tokens: number; usd: number; color: string; item?: CapexItem; special?: boolean }

function Donut({ lines, total, hover, setHover }: { lines: Line[]; total: number; hover: string | null; setHover: (k: string | null) => void }) {
  const R = 96, r = 62, C = 120;
  let a0 = -Math.PI / 2;
  const arcs = lines.filter((l) => l.tokens > 0).map((l) => {
    const a1 = a0 + (l.tokens / total) * Math.PI * 2;
    const big = a1 - a0 > Math.PI ? 1 : 0;
    const p = (rad: number, ang: number) => `${C + rad * Math.cos(ang)} ${C + rad * Math.sin(ang)}`;
    const d = a1 - a0 >= Math.PI * 2 - 1e-6
      ? `M${p(R, -Math.PI / 2)}A${R} ${R} 0 1 1 ${p(R, Math.PI * 1.5 - 1e-4)}L${p(r, Math.PI * 1.5 - 1e-4)}A${r} ${r} 0 1 0 ${p(r, -Math.PI / 2)}Z`
      : `M${p(R, a0)}A${R} ${R} 0 ${big} 1 ${p(R, a1)}L${p(r, a1)}A${r} ${r} 0 ${big} 0 ${p(r, a0)}Z`;
    a0 = a1;
    return { l, d };
  });
  const focus = lines.find((l) => l.key === hover);
  return (
    <svg viewBox="0 0 240 240" className="h-60 w-60 shrink-0" role="img" aria-label="Fordeling af tokens på Capex">
      {arcs.map(({ l, d }) => (
        <path key={l.key} d={d} fill={l.color} stroke="var(--background)" strokeWidth={2}
          opacity={hover && hover !== l.key ? 0.35 : 1} onMouseEnter={() => setHover(l.key)} onMouseLeave={() => setHover(null)}>
          <title>{`${l.number} · ${nfPct.format(l.tokens / total)} · ${nfTokens.format(l.tokens)} tokens`}</title>
        </path>
      ))}
      <text x={C} y={C - 6} textAnchor="middle" fontSize="13" fill="var(--muted-foreground)">{focus ? focus.number : "Tokens i alt"}</text>
      <text x={C} y={C + 16} textAnchor="middle" fontSize="20" fontWeight="600" fill="var(--foreground)">
        {focus ? nfPct.format(focus.tokens / total) : nfTokens.format(total)}
      </text>
    </svg>
  );
}

function ItemForm({ item, onSave, onCancel, busy }: { item?: CapexItem; onSave: (number: string, description: string) => void; onCancel: () => void; busy: boolean }) {
  const [number, setNumber] = useState(item?.number ?? "");
  const [description, setDescription] = useState(item?.description ?? "");
  const input = "rounded-md border bg-background px-2 py-1.5 text-sm";
  return (
    <form className="flex flex-wrap items-center gap-2" onSubmit={(e) => { e.preventDefault(); if (number.trim() && description.trim()) onSave(number.trim(), description.trim()); }}>
      <input id={`capex-number-${item?.id ?? "new"}`} value={number} onChange={(e) => setNumber(e.target.value)} placeholder="Capex-nr." aria-label="Capex-nummer" className={`${input} w-36`} autoFocus />
      <input id={`capex-desc-${item?.id ?? "new"}`} value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Hvad dækker det?" aria-label="Beskrivelse" className={`${input} min-w-64 flex-1`} />
      <button type="submit" disabled={!number.trim() || !description.trim() || busy} className="rounded-md bg-foreground px-3 py-1.5 text-sm font-medium text-background disabled:opacity-50">{busy ? "Gemmer …" : "Gem"}</button>
      <button type="button" onClick={onCancel} className="rounded-md border px-3 py-1.5 text-sm">Annullér</button>
    </form>
  );
}

// A hand-entered amount is spread evenly over the days it covers, so the period filter can cut it.
function spread(from: string, to: string, tokens: number, usd: number): CapexDay[] {
  const days: string[] = [];
  for (let d = new Date(`${from}T12:00:00`); iso(d) <= to; d.setDate(d.getDate() + 1)) days.push(iso(d));
  return days.map((date) => ({ date, tokens: tokens / days.length, usd: usd / days.length }));
}

function ManualForm({ items, rate, onSave, onCancel, busy }: { items: CapexItem[]; rate: number; onSave: (m: CapexManual) => void; onCancel: () => void; busy: boolean }) {
  const [label, setLabel] = useState("");
  const [capexId, setCapexId] = useState("");
  const [from, setFrom] = useState(iso(new Date()));
  const [to, setTo] = useState(iso(new Date()));
  const [tokens, setTokens] = useState("");
  const [kr, setKr] = useState("");
  const input = "rounded-md border bg-background px-2 py-1.5 text-sm";
  const ok = label.trim() && from && to && from <= to && (Number(tokens) > 0 || Number(kr) > 0);
  return (
    <form className="flex flex-wrap items-center gap-2" onSubmit={(e) => {
      e.preventDefault();
      if (ok) onSave({ id: crypto.randomUUID(), capex_id: capexId || null, label: label.trim(), days: spread(from, to, Number(tokens) || 0, (Number(kr) || 0) / rate) });
    }}>
      <input id="manual-label" value={label} onChange={(e) => setLabel(e.target.value)} placeholder="Hvad er det?" aria-label="Post" className={`${input} min-w-48 flex-1`} autoFocus />
      <select id="manual-capex" value={capexId} onChange={(e) => setCapexId(e.target.value)} aria-label="Capex" className={input}>
        <option value="">Intet Capex</option>
        {items.filter((c) => c.active).map((c) => <option key={c.id} value={c.id}>{c.number} · {c.description}</option>)}
      </select>
      <input id="manual-from" type="date" value={from} onChange={(e) => setFrom(e.target.value)} aria-label="Fra" className={input} />
      <input id="manual-to" type="date" value={to} onChange={(e) => setTo(e.target.value)} aria-label="Til" className={input} />
      <input id="manual-tokens" type="number" min="0" value={tokens} onChange={(e) => setTokens(e.target.value)} placeholder="Tokens" aria-label="Tokens" className={`${input} w-32`} />
      <input id="manual-kr" type="number" min="0" value={kr} onChange={(e) => setKr(e.target.value)} placeholder="Kr." aria-label="Kroner" className={`${input} w-28`} />
      <button type="submit" disabled={!ok || busy} className="rounded-md bg-foreground px-3 py-1.5 text-sm font-medium text-background disabled:opacity-50">{busy ? "Gemmer …" : "Gem"}</button>
      <button type="button" onClick={onCancel} className="rounded-md border px-3 py-1.5 text-sm">Annullér</button>
    </form>
  );
}

export default function CapexPage() {
  const workspace = useCurrentWorkspace();
  const p = useWorkspacePaths();
  const qc = useQueryClient();
  const myId = useAuthStore((s) => s.user?.id ?? null);
  const { data: members, isLoading: membersLoading } = useQuery({ ...memberListOptions(workspace?.id ?? ""), enabled: !!workspace?.id });
  const isAdmin = !!members?.some((m) => m.user_id === myId && (m.role === "owner" || m.role === "admin"));
  const { rows } = usePortfolio();
  const capex = useCapex();
  const options = useMemo(() => periods(), []);
  const [periodKey, setPeriodKey] = useState(options.at(0)?.key ?? "");
  const period: Period = useMemo(() => options.find((o) => o.key === periodKey) ?? options.at(0) ?? { key: "", label: "", from: "", to: "" }, [options, periodKey]);
  const [hover, setHover] = useState<string | null>(null);
  const [editing, setEditing] = useState<string | null>(null);
  const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;

  const projects = rows.filter((r) => r.source !== "clickup" && r.multica_project);
  const usage = useQueries({
    queries: [null, ...projects.map((r) => r.multica_project as string)].map((pid) => ({
      queryKey: ["capex-usage", workspace?.id, pid ?? "all", tz],
      enabled: !!workspace?.id && isAdmin,
      staleTime: 5 * 60_000,
      queryFn: () => api.getDashboardUsageDaily({ days: 365, project_id: pid, tz }),
    })),
  });
  const loading = usage.some((u) => u.isLoading) || capex.isLoading;

  const rate = capex.data?.usd_dkk ?? 6.9;
  const { lines, total } = useMemo(() => {
    const inPeriod = (rs: DashboardUsageDaily[] | undefined) => (rs ?? []).filter((r) => r.date >= period.from && r.date <= period.to);
    const sum = (rs: DashboardUsageDaily[]) => rs.reduce((a, r) => ({ tokens: a.tokens + tokensOf(r), usd: a.usd + estimateCost(r) }), { tokens: 0, usd: 0 });
    const items = capex.data?.items ?? [];
    const byCapex = new Map<string, Line>();
    items.forEach((it, i) => byCapex.set(it.id, { key: it.id, number: it.number, description: it.description, projects: [], tokens: 0, usd: 0,
      color: i < SLOTS ? `var(--cx-${i})` : "var(--cx-other)", item: it }));
    const none: Line = { key: "none", number: "Uden Capex", description: "Projekter, der ikke har valgt et Capex", projects: [], tokens: 0, usd: 0, color: "var(--cx-none)", special: true };
    let inProjects = { tokens: 0, usd: 0 };
    projects.forEach((r, i) => {
      const u = sum(inPeriod(usage[i + 1]?.data));
      inProjects = { tokens: inProjects.tokens + u.tokens, usd: inProjects.usd + u.usd };
      const target = byCapex.get(String(r.fields.capex_id ?? "")) ?? none;
      target.tokens += u.tokens; target.usd += u.usd;
      if (u.tokens > 0 || target !== none) target.projects.push(r.name);
    });
    const all = sum(inPeriod(usage[0]?.data));
    const noProject: Line = { key: "noproject", number: "Uden projekt", description: "Intake, chats og opgaver, der ikke ligger i et projekt", projects: [],
      tokens: Math.max(0, all.tokens - inProjects.tokens), usd: Math.max(0, all.usd - inProjects.usd), color: "var(--cx-noproj)", special: true };
    let manual = 0;
    (capex.data?.manual ?? []).forEach((m) => {
      const u = m.days.filter((d) => d.date >= period.from && d.date <= period.to)
        .reduce((a, d) => ({ tokens: a.tokens + d.tokens, usd: a.usd + d.usd }), { tokens: 0, usd: 0 });
      if (!u.tokens && !u.usd) return;
      const target = byCapex.get(m.capex_id ?? "") ?? none;
      target.tokens += u.tokens; target.usd += u.usd; target.projects.push(`${m.label} (manuel)`);
      manual += u.tokens;
    });
    const list = [...byCapex.values()].sort((a, b) => b.tokens - a.tokens);
    return { lines: [...list, none, noProject], total: Math.max(all.tokens, inProjects.tokens) + manual };
  }, [capex.data, projects, usage, period]);

  const save = useMutation({
    mutationFn: async (next: { items?: CapexItem[]; usd_dkk?: number; manual?: CapexManual[] }) =>
      saveCapex(capex.data?.issueId ?? null, { usd_dkk: next.usd_dkk ?? rate, items: next.items ?? items, manual: next.manual ?? manualEntries }),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ["capex"] }); setEditing(null); },
  });
  const items = capex.data?.items ?? [];
  const manualEntries = capex.data?.manual ?? [];
  const upsert = (id: string | null, number: string, description: string) =>
    save.mutate({ items: id ? items.map((x) => (x.id === id ? { ...x, number, description } : x)) : [...items, { id: crypto.randomUUID(), number, description, active: true }] });
  const toggle = (id: string) => save.mutate({ items: items.map((x) => (x.id === id ? { ...x, active: !x.active } : x)) });
  const setManual = (manual: CapexManual[]) => save.mutate({ manual });

  if (membersLoading) return <div className="p-6"><div className="h-64 animate-pulse rounded-lg bg-muted" /></div>;
  if (!isAdmin) return <div className="p-6 text-sm text-muted-foreground">Capex er kun for administratorer af workspacet.</div>;

  const totalUsd = lines.reduce((a, l) => a + l.usd, 0);
  const uncovered = lines.filter((l) => l.special).reduce((a, l) => a + l.tokens, 0);
  const pct = (n: number) => (total > 0 ? nfPct.format(n / total) : "—");

  return (
    <div className="capex-root h-full min-h-0 flex-1 overflow-y-auto">
      <style>{PALETTE_CSS}</style>
      <div className="flex flex-col gap-6 p-6">
        <header className="flex flex-wrap items-end justify-between gap-4">
          <div className="min-w-0">
            <h1 className="text-lg font-semibold">Capex</h1>
            <p className="text-sm text-muted-foreground">Agenternes tokenforbrug pr. Capex-nummer. Et projekt vælger sit Capex under «Opdater felter».</p>
          </div>
          <label className="flex items-center gap-2 text-sm">
            <span className="text-muted-foreground">Periode</span>
            <select id="capex-period" value={periodKey} onChange={(e) => setPeriodKey(e.target.value)} className="rounded-md border bg-background px-2 py-1.5">
              {options.map((o) => <option key={o.key} value={o.key}>{o.label}</option>)}
            </select>
          </label>
        </header>

        <section className="grid gap-3 sm:grid-cols-3" aria-label="Nøgletal">
          {[["Tokens", nfTokens.format(total), "input og output"], ["Anslået pris", nfKr.format(totalUsd * rate), `listepris, kurs ${rate.toLocaleString("da-DK")}`], ["Uden Capex", pct(uncovered), "uden Capex eller projekt"]].map(([k, v, s]) => (
            <div key={k} className="rounded-lg border px-4 py-3">
              <div className="text-xs text-muted-foreground">{k}</div>
              <div className="text-2xl font-semibold tabular-nums">{loading ? "…" : v}</div>
              <div className="text-xs text-muted-foreground">{s}</div>
            </div>
          ))}
        </section>

        <section className="flex flex-wrap items-center gap-8 rounded-lg border p-4" aria-label="Fordeling">
          {total > 0 ? <Donut lines={lines} total={total} hover={hover} setHover={setHover} /> : <p className="text-sm text-muted-foreground">{loading ? "Henter forbrug …" : "Intet forbrug i perioden."}</p>}
          <ul className="flex min-w-0 flex-1 flex-col gap-1.5 text-sm">
            {lines.filter((l) => l.tokens > 0).map((l) => (
              <li key={l.key} className={`flex items-center gap-2 rounded px-1 ${hover === l.key ? "bg-accent" : ""}`} onMouseEnter={() => setHover(l.key)} onMouseLeave={() => setHover(null)}>
                <span className="size-3 shrink-0 rounded-sm" style={{ background: l.color }} aria-hidden="true" />
                <span className="truncate">{l.number}{!l.special && <span className="text-muted-foreground"> · {l.description}</span>}</span>
                <span className="ml-auto tabular-nums text-muted-foreground">{pct(l.tokens)}</span>
              </li>
            ))}
          </ul>
        </section>

        <section className="flex flex-col gap-2" aria-labelledby="capex-list">
          <div className="flex items-center justify-between gap-3">
            <h2 id="capex-list" className="text-sm font-medium">Capex-numre</h2>
            {editing !== "new" && <button type="button" onClick={() => setEditing("new")} className="rounded-md border px-3 py-1.5 text-sm font-medium hover:bg-accent">+ Nyt Capex</button>}
          </div>
          {editing === "new" && <ItemForm onSave={(n, d) => upsert(null, n, d)} onCancel={() => setEditing(null)} busy={save.isPending} />}
          {save.error && <p className="text-sm text-[#CF022D]">Kunne ikke gemme: {(save.error as Error).message}</p>}
          <div className="overflow-x-auto rounded-lg border">
            <table className="w-full min-w-[820px] text-sm">
              <thead className="text-left text-xs text-muted-foreground">
                <tr className="border-b">
                  <th className="px-3 py-2 font-medium">Capex-nr.</th><th className="px-3 py-2 font-medium">Beskrivelse</th>
                  <th className="px-3 py-2 font-medium">Projekter</th><th className="px-3 py-2 text-right font-medium">Tokens</th>
                  <th className="px-3 py-2 text-right font-medium">Anslået pris</th><th className="px-3 py-2" /><th className="px-3 py-2 text-right font-medium">Andel</th>
                </tr>
              </thead>
              <tbody>
                {lines.map((l) => (
                  <tr key={l.key} className={`border-b align-top last:border-0 ${l.item && !l.item.active ? "text-muted-foreground" : ""}`}>
                    {editing === l.key && l.item ? (
                      <td colSpan={7} className="px-3 py-2"><ItemForm item={l.item} onSave={(n, d) => upsert(l.key, n, d)} onCancel={() => setEditing(null)} busy={save.isPending} /></td>
                    ) : (<>
                      <td className="px-3 py-2 font-medium">
                        <span className="mr-2 inline-block size-2.5 rounded-sm align-middle" style={{ background: l.color }} aria-hidden="true" />
                        {l.number}{l.item && !l.item.active && <span className="ml-2 rounded bg-muted px-1.5 py-0.5 text-[10px] uppercase">lukket</span>}
                      </td>
                      <td className="px-3 py-2">{l.description}</td>
                      <td className="px-3 py-2 text-muted-foreground">{l.projects.length ? l.projects.join(", ") : "—"}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{loading ? "…" : nfTokens.format(l.tokens)}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{loading ? "…" : nfKr.format(l.usd * rate)}</td>
                      <td className="px-3 py-2 text-right whitespace-nowrap">
                        {l.item && (<>
                          <button type="button" onClick={() => setEditing(l.key)} className="rounded-md border px-2 py-0.5 text-xs hover:bg-accent">Ret</button>{" "}
                          <button type="button" onClick={() => toggle(l.key)} disabled={save.isPending} className="rounded-md border px-2 py-0.5 text-xs hover:bg-accent">{l.item.active ? "Luk" : "Genåbn"}</button>
                        </>)}
                      </td>
                      <td className="px-3 py-2 text-right font-medium tabular-nums">{loading ? "…" : pct(l.tokens)}</td>
                    </>)}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {!items.length && <p className="text-sm text-muted-foreground">Ingen Capex-numre endnu. Opret det første med «+ Nyt Capex».</p>}
        </section>

        <section className="flex flex-col gap-2" aria-labelledby="capex-manual">
          <div className="flex items-center justify-between gap-3">
            <div>
              <h2 id="capex-manual" className="text-sm font-medium">Manuelle poster</h2>
              <p className="text-xs text-muted-foreground">Forbrug, Multica ikke selv har målt, fx Claude Code-sessionerne, der byggede opsætningen. Tæller med i Capex-linjen og i perioden.</p>
            </div>
            {editing !== "manual" && <button type="button" onClick={() => setEditing("manual")} className="shrink-0 rounded-md border px-3 py-1.5 text-sm font-medium hover:bg-accent">+ Manuel post</button>}
          </div>
          {editing === "manual" && <ManualForm items={items} rate={rate} onSave={(m) => setManual([...manualEntries, m])} onCancel={() => setEditing(null)} busy={save.isPending} />}
          {manualEntries.length > 0 && (
            <div className="overflow-x-auto rounded-lg border">
              <table className="w-full min-w-[720px] text-sm">
                <thead className="text-left text-xs text-muted-foreground">
                  <tr className="border-b">
                    <th className="px-3 py-2 font-medium">Post</th><th className="px-3 py-2 font-medium">Capex</th><th className="px-3 py-2 font-medium">Dage</th>
                    <th className="px-3 py-2 text-right font-medium">Tokens i alt</th><th className="px-3 py-2 text-right font-medium">Anslået pris i alt</th><th className="px-3 py-2" />
                  </tr>
                </thead>
                <tbody>
                  {manualEntries.map((m) => {
                    const dates = m.days.map((d) => d.date).sort();
                    const t = m.days.reduce((a, d) => ({ tokens: a.tokens + d.tokens, usd: a.usd + d.usd }), { tokens: 0, usd: 0 });
                    return (
                      <tr key={m.id} className="border-b align-top last:border-0">
                        <td className="px-3 py-2"><div className="font-medium">{m.label}</div>{m.note && <div className="text-xs text-muted-foreground">{m.note}</div>}</td>
                        <td className="px-3 py-2">
                          <select id={`manual-capex-${m.id}`} value={m.capex_id ?? ""} disabled={save.isPending} aria-label={`Capex for ${m.label}`}
                            onChange={(e) => setManual(manualEntries.map((x) => (x.id === m.id ? { ...x, capex_id: e.target.value || null } : x)))}
                            className="rounded-md border bg-background px-2 py-1 text-sm">
                            <option value="">Intet Capex</option>
                            {items.filter((c) => c.active || c.id === m.capex_id).map((c) => <option key={c.id} value={c.id}>{c.number} · {c.description}</option>)}
                          </select>
                        </td>
                        <td className="px-3 py-2 whitespace-nowrap text-muted-foreground">{dates.length ? `${dates.at(0)} – ${dates.at(-1)} (${dates.length})` : "—"}</td>
                        <td className="px-3 py-2 text-right tabular-nums">{nfTokens.format(t.tokens)}</td>
                        <td className="px-3 py-2 text-right tabular-nums">{nfKr.format(t.usd * rate)}</td>
                        <td className="px-3 py-2 text-right">
                          <button type="button" disabled={save.isPending} onClick={() => { if (confirm(`Fjern «${m.label}»?`)) setManual(manualEntries.filter((x) => x.id !== m.id)); }}
                            className="rounded-md border px-2 py-0.5 text-xs hover:bg-accent">Fjern</button>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </section>

        <footer className="flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
          <span>Tokens tæller input og output; cache-læsninger og -skrivninger indgår kun i prisen. Prisen er et skøn ud fra modellernes listepriser; kørsler på et ChatGPT- eller Claude-abonnement har ingen faktisk pris pr. kald. Et lukket Capex kan ikke vælges på nye projekter, men står stadig i statistikken.</span>
          <label className="flex items-center gap-2">Kurs USD→DKK
            <input id="capex-rate" type="number" step="0.01" min="1" defaultValue={rate} key={rate}
              onBlur={(e) => { const v = Number(e.target.value); if (v > 0 && v !== rate) save.mutate({ items, usd_dkk: v }); }}
              className="w-20 rounded-md border bg-background px-2 py-1 text-foreground" />
          </label>
          <AppLink href={p.projects()} className="underline underline-offset-4">Projekter</AppLink>
        </footer>
      </div>
    </div>
  );
}
