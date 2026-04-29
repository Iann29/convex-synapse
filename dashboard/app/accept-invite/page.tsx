"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Card, CardBody } from "@/components/ui/card";
import { ApiError, api } from "@/lib/api";
import { loadAuth } from "@/lib/auth";

function AcceptInviteInner() {
  const params = useSearchParams();
  const router = useRouter();
  const token = params.get("token") ?? "";

  const [state, setState] = useState<
    | { kind: "idle" }
    | { kind: "running" }
    | { kind: "ok"; teamSlug: string; teamName: string }
    | { kind: "error"; message: string }
  >({ kind: "idle" });

  useEffect(() => {
    // If not signed in, bounce to login with a return URL pointing back here.
    const auth = loadAuth();
    if (!auth) {
      const returnTo = `/accept-invite?token=${encodeURIComponent(token)}`;
      router.replace(`/login?next=${encodeURIComponent(returnTo)}`);
    }
  }, [router, token]);

  const accept = async () => {
    if (!token) {
      setState({ kind: "error", message: "Missing invite token in URL" });
      return;
    }
    setState({ kind: "running" });
    try {
      const res = await api.invites.accept(token);
      setState({ kind: "ok", teamSlug: res.teamSlug, teamName: res.teamName });
    } catch (err) {
      setState({
        kind: "error",
        message: err instanceof ApiError ? err.message : "Could not accept invite",
      });
    }
  };

  return (
    <main className="flex min-h-screen items-center justify-center px-4">
      <Card className="w-full max-w-md">
        <CardBody className="space-y-4">
          <div>
            <h1 className="text-lg font-semibold">Accept team invite</h1>
            <p className="mt-1 text-xs text-neutral-400">
              Click below to join. The invite token is consumed on accept.
            </p>
          </div>

          {!token && (
            <p className="text-xs text-red-400">
              No <code>token</code> query parameter — the link is incomplete.
            </p>
          )}

          {state.kind === "idle" && token && (
            <Button onClick={accept}>Accept invite</Button>
          )}
          {state.kind === "running" && (
            <p className="text-sm text-neutral-400">Joining…</p>
          )}
          {state.kind === "ok" && (
            <div className="space-y-3">
              <p className="text-sm text-neutral-200">
                You're now a member of <strong>{state.teamName}</strong>.
              </p>
              <Button
                onClick={() =>
                  router.push(`/teams/${encodeURIComponent(state.teamSlug)}`)
                }
              >
                Go to team
              </Button>
            </div>
          )}
          {state.kind === "error" && (
            <p className="rounded border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-300">
              {state.message}
            </p>
          )}
        </CardBody>
      </Card>
    </main>
  );
}

export default function AcceptInvitePage() {
  return (
    <Suspense fallback={null}>
      <AcceptInviteInner />
    </Suspense>
  );
}
