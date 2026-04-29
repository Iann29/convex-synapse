"use client";

import Link from "next/link";
import { Card, CardBody, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { TokensPanel } from "@/components/TokensPanel";

// Access Tokens pane.
//
// In Convex Cloud, access tokens are team-scoped. In Synapse v0 the only
// tokens we issue are user-scoped personal access tokens, listed at /me.
// We surface the same panel here so the IA matches Cloud and people who
// look for tokens under the team find them, with a clear note explaining
// the current scope.
export default function TeamAccessTokensPage() {
  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle>Access Tokens</CardTitle>
          <CardDescription>
            Tokens for CLI / CI access. In v0 these are scoped to your user,
            not to the team — see the note below for the roadmap.
          </CardDescription>
        </CardHeader>
        <CardBody>
          <p className="rounded-md border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-xs text-amber-200/80">
            Heads up: Synapse v0 issues <strong>user-scoped</strong> tokens
            only. Anything you create here also shows up under{" "}
            <Link href="/me" className="font-medium underline underline-offset-2">
              your account page
            </Link>
            . Team-scoped tokens are on the roadmap (v0.3+).
          </p>
        </CardBody>
      </Card>

      <Card>
        <CardBody>
          <TokensPanel />
        </CardBody>
      </Card>
    </div>
  );
}
