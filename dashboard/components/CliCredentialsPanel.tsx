"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Card, CardBody } from "@/components/ui/card";
import { ApiError, api, type CliCredentials } from "@/lib/api";
import { copyToClipboard } from "@/lib/clipboard";

type Props = {
  deploymentName: string;
};

type Format = "env" | "shell";

// CliCredentialsPanel is a tiny, additive drop-in for the deployment row.
// Sensitive values, so it hides behind a "Show CLI credentials" button.
//
// The CLI reads CONVEX_SELF_HOSTED_URL + CONVEX_SELF_HOSTED_ADMIN_KEY from
// process.env (auto-loaded from .env.local via dotenv). The .env format
// is what most operators want — pasting straight into the project's
// .env.local is the path of least resistance for `npx convex dev`. We
// also expose the `export …` shell form for one-shot terminal sessions.
export function CliCredentialsPanel({ deploymentName }: Props) {
  const [creds, setCreds] = useState<CliCredentials | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [format, setFormat] = useState<Format>("env");

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

  const snippet = creds
    ? format === "env"
      ? creds.envSnippet
      : creds.exportSnippet
    : "";

  const copy = async () => {
    if (!creds) return;
    const ok = await copyToClipboard(snippet);
    if (ok) {
      setCopied(true);
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
              {format === "env" ? (
                <>
                  Paste into{" "}
                  <code className="rounded bg-neutral-800 px-1 py-0.5 font-mono text-[11px] text-neutral-200">
                    .env.local
                  </code>
                  , then run{" "}
                  <code className="rounded bg-neutral-800 px-1 py-0.5 font-mono text-[11px] text-neutral-200">
                    npx convex dev
                  </code>
                  .
                </>
              ) : (
                <>
                  Paste into a shell, then run{" "}
                  <code className="rounded bg-neutral-800 px-1 py-0.5 font-mono text-[11px] text-neutral-200">
                    npx convex dev
                  </code>
                  .
                </>
              )}
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
        <div
          className="inline-flex rounded-md border border-neutral-800/80 bg-neutral-950 p-0.5 text-[11px]"
          role="tablist"
          aria-label="Snippet format"
        >
          <button
            type="button"
            role="tab"
            aria-selected={format === "env"}
            className={`rounded px-2 py-1 font-mono transition ${
              format === "env"
                ? "bg-neutral-800 text-neutral-100"
                : "text-neutral-400 hover:text-neutral-200"
            }`}
            onClick={() => {
              setFormat("env");
              setCopied(false);
            }}
          >
            .env.local
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={format === "shell"}
            className={`rounded px-2 py-1 font-mono transition ${
              format === "shell"
                ? "bg-neutral-800 text-neutral-100"
                : "text-neutral-400 hover:text-neutral-200"
            }`}
            onClick={() => {
              setFormat("shell");
              setCopied(false);
            }}
          >
            shell (export)
          </button>
        </div>
        <pre className="overflow-x-auto whitespace-pre rounded-md border border-neutral-800/80 bg-neutral-950 p-3 font-mono text-[11px] leading-snug text-neutral-200">
          {snippet}
        </pre>
        {error && <p className="text-xs text-red-400">{error}</p>}
      </CardBody>
    </Card>
  );
}
