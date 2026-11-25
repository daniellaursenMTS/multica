import { readFile } from "node:fs/promises";
import path from "node:path";
import type { NextRequest } from "next/server";

/**
 * Serves the diamond diagrams the Diamonds page shows.
 *
 * They are generated outside Multica (matas-ai-automation/diamond/render_views.py,
 * built with the same diagram-design rules as the project presentation) into a folder
 * mounted read-only at DIAMOND_VIEWS_DIR. Reading at request time — not from `public/` —
 * is what lets them refresh without rebuilding this image.
 *
 * The files name real people and their roles, so every request must belong to a
 * signed-in Multica user: the session cookie is checked against the API's `/api/me`.
 */
export const dynamic = "force-dynamic";

const FILE = /^[a-z0-9-]+\.(svg|json)$/;

// What the auth check saw, for the 401 response: header and cookie names only, never values.
function authSeen(req: NextRequest): string {
  const names = req.cookies.getAll().map((c) => c.name).sort().join(",") || "-";
  return `cookie-header=${req.headers.get("cookie") ? 1 : 0}; bearer=${req.headers.get("authorization")?.startsWith("Bearer ") ? 1 : 0}; cookies=${names}`;
}

async function signedIn(req: NextRequest): Promise<boolean> {
  const api = process.env.REMOTE_API_URL;
  // The web app signs in with the session cookie or, in token mode, a bearer token; accept either.
  // Cookies are read through Next's cookie store as well as the raw header: proxy.ts rewrites the
  // request headers, and the raw header has been seen missing here while /api requests had it.
  const fromStore = req.cookies.getAll().map((c) => `${c.name}=${c.value}`).join("; ");
  const cookie = req.headers.get("cookie") || fromStore;
  const authorization = req.headers.get("authorization");
  if (!api || (!cookie && !authorization?.startsWith("Bearer "))) return false;
  const headers: Record<string, string> = {};
  if (cookie) headers.cookie = cookie;
  if (authorization?.startsWith("Bearer ")) headers.authorization = authorization;
  try {
    const res = await fetch(`${api}/api/me`, { headers, cache: "no-store" });
    return res.ok;
  } catch {
    return false;
  }
}

export async function GET(req: NextRequest, { params }: { params: Promise<{ file: string }> }) {
  const { file } = await params;
  if (!FILE.test(file)) return new Response("Not found", { status: 404 });
  if (!(await signedIn(req))) return new Response("Sign in to Multica to see the diamonds.", { status: 401, headers: { "x-auth-seen": authSeen(req) } });

  const dir = process.env.DIAMOND_VIEWS_DIR ?? "/data/diamond-views";
  try {
    const body = await readFile(path.join(dir, file));
    return new Response(body, {
      headers: {
        "content-type": file.endsWith(".svg") ? "image/svg+xml; charset=utf-8" : "application/json; charset=utf-8",
        "cache-control": "no-store",
        "x-content-type-options": "nosniff",
      },
    });
  } catch {
    return new Response("Not generated yet. Run diamond_status.py --write.", { status: 404 });
  }
}
