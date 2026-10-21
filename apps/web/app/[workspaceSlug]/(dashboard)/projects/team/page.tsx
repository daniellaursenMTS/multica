"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { useWorkspacePaths } from "@multica/core/paths";

// Team moved to its own page in the sidebar; old links forward.
export default function Page() {
  const router = useRouter();
  const p = useWorkspacePaths();
  useEffect(() => { router.replace(p.team()); }, [router, p]);
  return null;
}
