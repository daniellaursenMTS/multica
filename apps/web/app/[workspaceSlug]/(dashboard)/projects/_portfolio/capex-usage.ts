import { z } from "zod";

const pair = z.tuple([z.number().finite().nonnegative(), z.number().finite().nonnegative()]);
const dayKey = z.string().regex(/^\d{4}-\d{2}-\d{2}$/);
export const usageSchema = z.object({
  v: z.literal(1), id: z.string().min(1), label: z.string().min(1), note: z.string().optional(),
  capex_id: z.string().nullable().optional(), project_id: z.string().nullable().optional(),
  months: z.record(z.string().regex(/^\d{4}-\d{2}$/), pair), days: z.record(dayKey, pair),
  daily_from: dayKey, through: dayKey,
}).refine((e) => !(e.capex_id && e.project_id), "Vælg enten projekt eller Capex");
export type CapexManual = z.infer<typeof usageSchema> & { issueId?: string };
export interface CapexDay { date: string; tokens: number; usd: number }
export interface UsageDraft { id: string; label: string; note?: string; capex_id: string | null; project_id?: string | null; days: CapexDay[] }

export function usageToday(now = new Date()): string {
  return new Intl.DateTimeFormat("sv-SE", { timeZone: "Europe/Copenhagen" }).format(now);
}
export function daysBefore(date: string, days: number): string {
  const d = new Date(`${date}T12:00:00Z`);
  d.setUTCDate(d.getUTCDate() - days);
  return d.toISOString().slice(0, 10);
}

/** Full snapshots only. Monthly totals include the retained daily values. */
export function compactUsage(draft: UsageDraft, today = usageToday()): CapexManual {
  if (draft.capex_id && draft.project_id) throw new Error("Vælg enten projekt eller Capex");
  const months: Record<string, [number, number]> = {};
  const days: Record<string, [number, number]> = {};
  const cutoff = daysBefore(today, 44);
  const seen = new Set<string>();
  for (const d of draft.days) {
    if (d.date > today || seen.has(d.date)) throw new Error("Fremtidig eller dubleret forbrugsdag");
    seen.add(d.date);
    pair.parse([d.tokens, d.usd]);
    const month = months[d.date.slice(0, 7)] ??= [0, 0];
    month[0] += d.tokens; month[1] += d.usd;
    if (d.date >= cutoff) days[d.date] = [d.tokens, d.usd];
  }
  for (const values of [months, days]) for (const p of Object.values(values)) {
    p[0] = Number(p[0].toFixed(8)); p[1] = Number(p[1].toFixed(8));
  }
  return usageSchema.parse({ v: 1, id: draft.id, label: draft.label, note: draft.note,
    ...(draft.project_id ? { project_id: draft.project_id } : { capex_id: draft.capex_id }),
    months, days, daily_from: cutoff, through: today });
}

export function usageMetadata(entry: CapexManual) {
  const data = usageSchema.parse(entry); // Strip UI-only fields, including issueId.
  const metadata = { capex_usage: true, capex_usage_id: data.id, capex_usage_data: JSON.stringify(data) };
  if (new TextEncoder().encode(JSON.stringify(metadata)).length > 6000) {
    throw new Error("Posten overstiger grænsen på 6.000 bytes. Intet er gemt.");
  }
  return metadata;
}

const dayCount = (from: string, to: string) => Math.round((Date.parse(to) - Date.parse(from)) / 86_400_000) + 1;

/**
 * Whole months from history; partial recent months from days. Never add both.
 * A partial month older than `daily_from` has no daily figures left: its share is then
 * estimated evenly from the month total and `approx` is set, so the UI can mark it.
 */
export function usageTotal(entry: CapexManual, from = "0000-01-01", to = "9999-12-31") {
  const total = { tokens: 0, usd: 0, approx: false };
  for (const [month, p] of Object.entries(entry.months)) {
    const start = `${month}-01`;
    const end = new Date(Date.UTC(Number(month.slice(0, 4)), Number(month.slice(5, 7)), 0)).toISOString().slice(0, 10);
    if (end < from || start > to) continue;
    if (from <= start && (to >= end || to >= entry.through)) {
      total.tokens += p[0]; total.usd += p[1];
      continue;
    }
    let keptTokens = 0, keptUsd = 0;
    for (const [date, day] of Object.entries(entry.days)) if (date.startsWith(month)) {
      keptTokens += day[0]; keptUsd += day[1];
      if (date >= from && date <= to) { total.tokens += day[0]; total.usd += day[1]; }
    }
    const lo = from > start ? from : start;
    if (lo < entry.daily_from) {
      const lastUndated = daysBefore(entry.daily_from, 1);
      const undated = dayCount(start, end < lastUndated ? end : lastUndated);
      const wanted = dayCount(lo, [to, end, lastUndated].sort()[0]!);
      const share = wanted / undated;
      total.tokens += Math.max(0, p[0] - keptTokens) * share;
      total.usd += Math.max(0, p[1] - keptUsd) * share;
      total.approx = true;
    }
  }
  return total;
}

/** Entries the nightly importers own (Claude Code, Codex). They rewrite name and numbers; only the allocation survives. */
export const importedUsage = (id: string) => id.startsWith("claude-code-") || id.startsWith("codex-");

/**
 * First and last day with usage. A month older than `daily_from` only knows its total, so it
 * counts from its first or to its last undated day.
 */
export function usageRange(entry: CapexManual): { from: string; to: string } | null {
  const used = Object.keys(entry.months).filter((m) => entry.months[m]!.some((v) => v > 0)).sort();
  const first = used.at(0), last = used.at(-1);
  if (!first || !last) return null;
  const kept = (month: string) => Object.keys(entry.days).filter((d) => d.startsWith(month) && entry.days[d]!.some((v) => v > 0)).sort();
  // Undated usage is whatever of the month total its kept days do not explain.
  const undated = (month: string) => [0, 1].some((i) =>
    entry.months[month]![i]! - kept(month).reduce((a, d) => a + entry.days[d]![i]!, 0) > 1e-6);
  const firstKept = kept(first).at(0), lastKept = kept(last).at(-1);
  const end = new Date(Date.UTC(Number(last.slice(0, 4)), Number(last.slice(5, 7)), 0)).toISOString().slice(0, 10);
  const lastUndated = daysBefore(entry.daily_from, 1);
  return { from: firstKept && !undated(first) ? firstKept : `${first}-01`, to: lastKept ?? (end < lastUndated ? end : lastUndated) };
}

/**
 * New totals on the stored distribution: every month and retained day is scaled by the same
 * factor, so months still include their days. A dimension that was zero borrows the other's
 * distribution.
 */
export function scaleUsage(entry: CapexManual, tokens: number, usd: number): CapexManual {
  pair.parse([tokens, usd]);
  const old = usageTotal(entry);
  if (old.tokens <= 0 && old.usd <= 0) throw new Error("Posten har intet forbrug at skalere");
  const totals = [old.tokens, old.usd];
  const share = (p: [number, number], i: 0 | 1) => (totals[i]! > 0 ? p[i] / totals[i]! : p[1 - i]! / totals[1 - i]!);
  const scale = (values: Record<string, [number, number]>) => Object.fromEntries(Object.entries(values).map(([k, p]) =>
    [k, [Number((tokens * share(p, 0)).toFixed(8)), Number((usd * share(p, 1)).toFixed(8))]]));
  const { issueId: _issueId, ...data } = entry;
  return usageSchema.parse({ ...data, months: scale(entry.months), days: scale(entry.days) });
}
