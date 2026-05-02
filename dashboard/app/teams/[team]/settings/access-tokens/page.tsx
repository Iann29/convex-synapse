"use client";

import { use } from "react";
import { Card, CardBody, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { TokensPanel } from "@/components/TokensPanel";

type Params = { team: string };

// Team-scoped access tokens. Bearer can act on this team's projects and
// deployments — but NOT on other teams the issuer belongs to. The wider-
// scope alternative (user-scoped) lives at /me; the narrower-scope
// alternatives (project, app, deployment) live on the project / deployment
// pages respectively.
export default function TeamAccessTokensPage({
  params,
}: {
  params: Promise<Params>;
}) {
  const { team: teamRef } = use(params);
  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle>Access Tokens</CardTitle>
          <CardDescription>
            Team-scoped opaque tokens for CLI / CI access against this team.
            Less privileged than your personal account tokens; more
            privileged than per-project keys.
          </CardDescription>
        </CardHeader>
        <CardBody>
          <TokensPanel scope="team" target={teamRef} />
        </CardBody>
      </Card>
    </div>
  );
}
