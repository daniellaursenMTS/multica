import { readFile } from "node:fs/promises";
import path from "node:path";

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

async function signedIn(req: Request): Promise<boolean> {
  const api = process.env.REMOTE_API_URL;
  const cookie = req.headers.get("cookie");
  if (!api || !cookie) return false;
  try {
    const res = await fetch(`${api}/api/me`, { headers: { cookie }, cache: "no-store" });
    return res.ok;
  } catch {
    return false;
  }
}

export async function GET(req: Request, { params }: { params: Promise<{ file: string }> }) {
  const { file } = await params;
  if (!FILE.test(file)) return new Response("Not found", { status: 404 });
  if (!(await signedIn(req))) return new Response("Sign in to Multica to see the diamonds.", { status: 401 });

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
