"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Card, CardBody } from "@/components/ui/card";
import { ApiError, api, type CliCredentials } from "@/lib/api";
import { copyToClipboard } from "@/lib/clipboard";

type Props = {
  deploymentName: string;
};

// CliCredentialsPanel is a tiny, additive drop-in for the deployment row.
// It hides behind a "Show CLI credentials" button (the values are sensitive
// — we don't want them rendered on every page load), fetches them on click,
// and renders a copy-pastable `export …` snippet that the user can paste
// into a shell next to a `npx convex dev` invocation.
//
// The CLI looks for CONVEX_SELF_HOSTED_URL + CONVEX_SELF_HOSTED_ADMIN_KEY in
// process.env (after dotenv-loading .env.local then .env). Synapse builds
// the snippet server-side so the dashboard never has to know the env-var
// names — see `cli_credentials` handler in `internal/api/deployments.go`.
export function CliCredentialsPanel({ deploymentName }: Props) {
  const [creds, setCreds] = useState<CliCredentials | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const reveal = async () => {
    setError(null);
    setLoading(true);
    try {
      const c = await api.deployments.cliCredentials(deploymentName);
      setCreds(c);
    } catch (err) {
      setError(
        err instanceof ApiError
          ? err.message
          : "Could not load CLI credentials",
      );
    } finally {
      setLoading(false);
    }
  };

  const hide = () => {
    setCreds(null);
    setError(null);
    setCopied(false);
  };

  const copy = async () => {
    if (!creds) return;
    const ok = await copyToClipboard(creds.exportSnippet);
    if (ok) {
      setCopied(true);
      // 1.5s mirrors the URL-copy affordance on the deployment row.
      setTimeout(() => setCopied(false), 1500);
    } else {
      setError("Could not copy — select the snippet manually and Ctrl+C");
    }
  };

  if (!creds) {
    return (
      <div className="flex items-center gap-2 text-xs">
        <Button
          variant="ghost"
          size="sm"
          onClick={reveal}
          disabled={loading}
          aria-label={`Show CLI credentials for ${deploymentName}`}
        >
          {loading ? "Loading…" : "Show CLI credentials"}
        </Button>
        {error && <span className="text-red-400">{error}</span>}
      </div>
    );
  }

  return (
    <Card className="mt-2">
      <CardBody className="space-y-2">
        <div className="flex items-center justify-between">
          <div>
            <p className="text-xs font-semibold text-neutral-200">
              Use with Convex CLI
            </p>
            <p className="text-xs text-neutral-500">
              Paste into a shell, then run{" "}
              <code className="rounded bg-neutral-800 px-1 py-0.5 font-mono text-[11px] text-neutral-200">
                npx convex dev
              </code>
              .
            </p>
          </div>
          <div className="flex shrink-0 gap-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={copy}
              aria-label="Copy CLI credentials snippet"
            >
              {copied ? "Copied!" : "Copy"}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={hide}
              aria-label="Hide CLI credentials"
            >
              Hide
            </Button>
          </div>
        </div>
        <pre className="overflow-x-auto whitespace-pre rounded-md border border-neutral-800/80 bg-neutral-950 p-3 font-mono text-[11px] leading-snug text-neutral-200">
          {creds.exportSnippet}
        </pre>
        {error && <p className="text-xs text-red-400">{error}</p>}
      </CardBody>
    </Card>
  );
}
