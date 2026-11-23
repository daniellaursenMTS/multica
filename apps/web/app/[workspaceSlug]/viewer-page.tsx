"use client";

import { useAuthStore } from "@multica/core/auth";

// The project portal (MATA-267). Set at build time; until the portal is live the page says how to
// find knowledge through Claude instead.
const PORTAL_URL = process.env.NEXT_PUBLIC_MATAS_PORTAL_URL ?? "";

/** What a reader (workspace role viewer, MATA-286) sees instead of Multica: readers find project
 *  knowledge in the portal and through the MCP read tools. The server refuses them everything else. */
export function ViewerPage({ workspaceName }: { workspaceName: string }) {
  const user = useAuthStore((s) => s.user);
  const logout = useAuthStore((s) => s.logout);
  return (
    <main className="flex min-h-svh items-center justify-center p-6">
      <section className="flex max-w-lg flex-col gap-4 rounded-lg border p-6" aria-labelledby="viewer-title">
        <h1 id="viewer-title" className="text-lg font-semibold">Du er læser i {workspaceName}</h1>
        <p className="text-sm text-muted-foreground">
          Som læser finder du projekternes viden i projektportalen og gennem Claude, ikke i Multica selv. Du ser det, projekternes
          ejere har delt, og du kan stille Knowledge Hub spørgsmål.
        </p>
        {PORTAL_URL ? (
          <a href={PORTAL_URL} className="self-start rounded-md bg-foreground px-3 py-1.5 text-sm font-medium text-background">
            Gå til projektportalen
          </a>
        ) : (
          <p className="text-sm">
            Projektportalen er ikke åben endnu. Indtil da: brug Claude med forbindelsen <span className="font-medium">matas-multica</span>,
            og spørg fx «Hvad er status på 1004-2?».
          </p>
        )}
        <p className="text-xs text-muted-foreground">
          Logget ind som {user?.email}. Skal du arbejde i Multica, så bed en administrator om en anden rolle.
        </p>
        <button type="button" onClick={() => logout()} className="self-start rounded-md border px-3 py-1.5 text-sm hover:bg-accent">
          Log ud
        </button>
      </section>
    </main>
  );
}
