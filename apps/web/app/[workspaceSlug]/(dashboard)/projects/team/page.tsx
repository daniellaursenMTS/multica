"use client";

import { useMemo, useState } from "react";
import { useAuthStore } from "@multica/core/auth";
import { api } from "@multica/core/api";
import { useCurrentWorkspace, useWorkspacePaths } from "@multica/core/paths";
import { memberListOptions } from "@multica/core/workspace/queries";
import { AppLink } from "@multica/views/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ROLE_NAME, usePortfolio, type DiaSpec, type RoleCode, type Row } from "../_portfolio/portfolio";

// Team (admins only): everyone who holds a role in a diamond, the projects and roles they hold,
// and the login they are linked to. Renaming writes the new name into every diamond the person is
// in; the diamond sync then updates roster.json, the hubs' role tables and ROSTER.md.

const ROLES: RoleCode[] = ["bo", "po", "plo", "to"];
interface Seat { row: Row; role: RoleCode; confirmed: boolean }
interface Person { name: string; seats: Seat[]; email: string | null; demoOnly: boolean }

function similar(name: string, others: string[]) {
  const x = name.toLowerCase();
  const first = x.split(/\s+/)[0];
  return others.find((k) => {
    const y = k.toLowerCase();
    return y !== x && (y === x || y.split(/\s+/)[0] === first || y.startsWith(`${x} `) || x.startsWith(`${y} `));
  }) ?? null;
}

function Rename({ person, people, onDone }: { person: Person; people: string[]; onDone: () => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState(person.name);
  const next = name.trim();
  const merge = next !== person.name && people.includes(next);
  const save = useMutation({
    mutationFn: async () => {
      // One write per diamond the person is in: same spec, the name swapped in each of their roles.
      const byRow = new Map<string, Row>();
      person.seats.forEach((s) => byRow.set(s.row.id, s.row));
      for (const row of byRow.values()) {
        if (!row.intake_issue || !row.dia_spec) continue;
        const spec: DiaSpec = JSON.parse(JSON.stringify(row.dia_spec));
        ROLES.forEach((r) => {
          const v = spec.roles[r];
          if (v?.person === person.name) {
            v.person = next;
            if (person.email) { (v as Record<string, unknown>).email = person.email; (v as Record<string, unknown>).rename_from = person.name; }
          }
        });
        await api.setIssueMetadataKey(row.intake_issue, "dia_spec", JSON.stringify(spec));
      }
    },
    onSuccess: () => { qc.invalidateQueries({ queryKey: ["portfolio", "live"] }); onDone(); },
  });
  const input = "rounded-md border bg-background px-2 py-1.5 text-sm";
  return (
    <form className="flex flex-col gap-2" onSubmit={(e) => { e.preventDefault(); if (next && next !== person.name) save.mutate(); }}>
      <div className="flex flex-wrap items-center gap-2">
        <input id={`team-name-${person.name}`} value={name} onChange={(e) => setName(e.target.value)} className={input} autoFocus aria-label="Nyt navn" />
        <button type="submit" disabled={!next || next === person.name || save.isPending}
          className="rounded-md bg-foreground px-3 py-1.5 text-sm font-medium text-background disabled:opacity-50">
          {save.isPending ? "Gemmer …" : merge ? "Slå sammen" : "Gem navn"}
        </button>
        <button type="button" onClick={onDone} className="rounded-md border px-3 py-1.5 text-sm">Annullér</button>
      </div>
      {merge && <p className="text-xs text-[#9a6700]">«{next}» findes allerede. De to bliver én person i alle diamanter.</p>}
      {save.error && <p className="text-xs text-[#CF022D]">Kunne ikke gemme: {(save.error as Error).message}</p>}
    </form>
  );
}

export default function TeamPage() {
  const workspace = useCurrentWorkspace();
  const p = useWorkspacePaths();
  const myId = useAuthStore((s) => s.user?.id ?? null);
  const { data: members, isLoading: membersLoading } = useQuery({ ...memberListOptions(workspace?.id ?? ""), enabled: !!workspace?.id });
  const isAdmin = !!members?.some((m) => m.user_id === myId && (m.role === "owner" || m.role === "admin"));
  const { rows, index, loading } = usePortfolio();
  const [editing, setEditing] = useState<string | null>(null);

  const people = useMemo<Person[]>(() => {
    const emailOf = new Map(Object.entries(index?.accounts ?? {}).map(([e, n]) => [n, e]));
    const map = new Map<string, Person>();
    rows.filter((r) => r.dia_spec && !r.dia_archived).forEach((row) => {
      ROLES.forEach((role) => {
        const v = row.dia_spec?.roles[role];
        if (!v?.person) return;
        const p0 = map.get(v.person) ?? { name: v.person, seats: [], email: emailOf.get(v.person) ?? null, demoOnly: true };
        p0.seats.push({ row, role, confirmed: v.confirmed !== false });
        if (!row.demo) p0.demoOnly = false;
        map.set(v.person, p0);
      });
    });
    return Array.from(map.values()).sort((a, b) => Number(a.demoOnly) - Number(b.demoOnly) || a.name.localeCompare(b.name, "da"));
  }, [rows, index]);
  const names = people.map((x) => x.name);
  const pending = rows.some((r) => r.dia_pending);

  if (membersLoading) return <div className="p-6"><div className="h-64 animate-pulse rounded-lg bg-muted" /></div>;
  if (!isAdmin) {
    return <div className="p-6 text-sm text-muted-foreground">Team er kun for administratorer af workspacet.</div>;
  }
  return (
    <div className="h-full min-h-0 flex-1 overflow-y-auto">
      <div className="flex flex-col gap-6 p-6">
        <header className="flex flex-col gap-1">
          <AppLink href={p.projects()} className="self-start text-sm text-muted-foreground hover:text-foreground">← Projekter</AppLink>
          <h1 className="text-lg font-semibold">Team</h1>
          <p className="text-sm text-muted-foreground">
            Alle med en rolle i en diamant. Et nyt navn skrives i alle personens diamanter og følger med til hub, status og projekt-chat inden for et minut.
            {pending && " Synkroniserer ændringer …"}
          </p>
        </header>
        {loading && <div className="h-64 animate-pulse rounded-lg bg-muted" />}
        <div className="overflow-x-auto rounded-lg border">
          <table className="w-full min-w-[820px] text-sm">
            <thead className="text-left text-xs text-muted-foreground">
              <tr className="border-b"><th className="px-3 py-2 font-medium">Person</th><th className="px-3 py-2 font-medium">Projekter og roller</th><th className="px-3 py-2 font-medium">Login</th><th className="px-3 py-2" /></tr>
            </thead>
            <tbody>
              {people.map((person) => {
                const like = similar(person.name, names);
                return (
                  <tr key={person.name} className="border-b align-top last:border-0">
                    <td className="px-3 py-3">
                      {editing === person.name ? (
                        <Rename person={person} people={names} onDone={() => setEditing(null)} />
                      ) : (
                        <div className="flex flex-col gap-1">
                          <span className="font-medium">{person.name}{person.demoOnly && <span className="ml-2 rounded bg-muted px-1.5 py-0.5 text-[10px] uppercase">demo</span>}</span>
                          {like && <span className="text-xs text-[#9a6700]">Ligner «{like}». Samme person?</span>}
                        </div>
                      )}
                    </td>
                    <td className="px-3 py-3">
                      <ul className="flex flex-col gap-1">
                        {person.seats.map((s) => (
                          <li key={s.row.id + s.role}>
                            <AppLink href={`${p.projectDetail(s.row.id)}?view=diamond`} className="font-medium hover:underline">{s.row.name}</AppLink>
                            <span className="text-muted-foreground"> · {ROLE_NAME[s.role]}{s.confirmed ? "" : " (skal bekræftes)"}</span>
                          </li>
                        ))}
                      </ul>
                    </td>
                    <td className="px-3 py-3 text-muted-foreground">{person.email ?? "—"}</td>
                    <td className="px-3 py-3 text-right">
                      {editing !== person.name && (
                        <button type="button" onClick={() => setEditing(person.name)} className="rounded-md border px-3 py-1 text-sm hover:bg-accent">Ret navn</button>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        <p className="text-xs text-muted-foreground">
          Noter, personen allerede har sendt, står i hubben under det navn, de blev sendt med. Logins kobles i «Ret roller» på projektets Diamant-fane.
        </p>
      </div>
    </div>
  );
}
