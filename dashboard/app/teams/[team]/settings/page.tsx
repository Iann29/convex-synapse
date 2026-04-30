"use client";

import { useEffect, use } from "react";
import { useRouter } from "next/navigation";

type Params = { team: string };

// `/teams/<ref>/settings` has no content of its own — it's the umbrella
// route. Bounce to the General pane so users always land on something.
export default function TeamSettingsIndex({
  params,
}: {
  params: Promise<Params>;
}) {
  const { team: teamRef } = use(params);
  const router = useRouter();
  useEffect(() => {
    router.replace(`/teams/${encodeURIComponent(teamRef)}/settings/general`);
  }, [router, teamRef]);
  return null;
}
