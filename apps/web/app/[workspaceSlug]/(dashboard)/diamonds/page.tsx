"use client";

import { Suspense, useEffect } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { useWorkspacePaths } from "@multica/core/paths";

// Portfolio moved into Projects. Old links (?project=, ?tab=mine) are forwarded.
function Forward() {
  const router = useRouter();
  const params = useSearchParams();
  const p = useWorkspacePaths();
  useEffect(() => {
    const project = params.get("project");
    router.replace(project ? p.projectDetail(project) : params.get("tab") === "mine" ? `${p.projects()}?filter=mine` : p.projects());
  }, [router, params, p]);
  return null;
}

export default function Page() {
  return <Suspense fallback={null}><Forward /></Suspense>;
}
