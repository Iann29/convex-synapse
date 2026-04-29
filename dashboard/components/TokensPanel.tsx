"use client";

import { useState } from "react";
import useSWR from "swr";
import { Button } from "@/components/ui/button";
import { Card, CardBody } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import {
  ApiError,
  api,
  type AccessToken,
  type ListTokensResponse,
} from "@/lib/api";

// Personal access tokens panel for /me. Tokens authenticate the CLI / CI
// against Synapse without going through /v1/auth/login. The plaintext is
// shown ONCE on creation — server stores only a SHA-256 hash and can't
// surface it again.
export function TokensPanel() {
  const { data, error, isLoading, mutate } = useSWR<ListTokensResponse>(
    "/tokens",
    () => api.tokens.list(),
  );

  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [pending, setPending] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  // The freshly-created plaintext token; surfaced inside the dialog only.
  const [issued, setIssued] = useState<{ name: string; token: string } | null>(
    null,
  );

  const closeDialog = () => {
    setOpen(false);
    setName("");
    setFormError(null);
    setIssued(null);
  };

  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);
    if (!name.trim()) return;
    setPending(true);
    try {
      const res = await api.tokens.create(name.trim());
      setIssued({ name: res.accessToken.name, token: res.token });
      setName("");
      await mutate();
    } catch (err) {
      setFormError(err instanceof ApiError ? err.message : "Could not create token");
    } finally {
      setPending(false);
    }
  };

  const remove = async (token: AccessToken) => {
    // Native confirm is fine for a destructive op on a single row;
    // matches the pattern used elsewhere (project delete).
    if (
      typeof window !== "undefined" &&
      !window.confirm(`Delete token "${token.name}"? This can't be undone.`)
    ) {
      return;
    }
    try {
      await api.tokens.delete(token.id);
      await mutate();
    } catch (err) {
      setFormError(
        err instanceof ApiError ? err.message : "Could not delete token",
      );
    }
  };

  return (
    <section className="space-y-3" aria-labelledby="tokens-heading">
      <div className="flex items-center justify-between">
        <div>
          <h2 id="tokens-heading" className="text-sm font-semibold text-neutral-200">
            Personal access tokens
          </h2>
          <p className="text-xs text-neutral-500">
            Use these for CLI / CI / programmatic access. Tokens carry your
            full account permissions.
          </p>
        </div>
        <Button onClick={() => setOpen(true)}>New token</Button>
      </div>

      {isLoading && (
        <Card>
          <CardBody>
            <Skeleton className="h-4 w-1/3" />
            <Skeleton className="mt-2 h-3 w-1/4" />
          </CardBody>
        </Card>
      )}

      {error && (
        <p className="text-xs text-red-400">
          Failed to load tokens: {(error as Error).message}
        </p>
      )}

      {data && data.items.length === 0 && (
        <Card>
          <CardBody className="text-center">
            <p className="text-sm text-neutral-300">No tokens yet.</p>
            <p className="mt-1 text-xs text-neutral-500">
              Create one to use the Synapse API from outside the dashboard.
            </p>
          </CardBody>
        </Card>
      )}

      {data && data.items.length > 0 && (
        <Card>
          <CardBody className="divide-y divide-neutral-800 p-0">
            {data.items.map((tok) => (
              <div
                key={tok.id}
                className="flex items-center justify-between gap-3 px-4 py-3 text-sm"
                data-testid={`token-row-${tok.name}`}
              >
                <div className="min-w-0 flex-1">
                  <p className="truncate text-neutral-100">{tok.name}</p>
                  <p className="font-mono text-xs text-neutral-500">
                    scope: {tok.scope} · created{" "}
                    {new Date(tok.createTime).toLocaleString()}
                    {tok.lastUsedAt
                      ? ` · last used ${new Date(tok.lastUsedAt).toLocaleString()}`
                      : " · never used"}
                  </p>
                </div>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => remove(tok)}
                  aria-label={`Delete token ${tok.name}`}
                >
                  Delete
                </Button>
              </div>
            ))}
          </CardBody>
        </Card>
      )}

      <Dialog
        open={open}
        onClose={closeDialog}
        title={issued ? "Token created" : "New personal access token"}
      >
        {!issued && (
          <form onSubmit={create} className="space-y-4">
            <div className="space-y-2">
              <label htmlFor="token-name" className="block text-xs text-neutral-400">
                Name
              </label>
              <Input
                id="token-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="ci-runner"
                required
                autoFocus
                maxLength={100}
              />
              <p className="text-xs text-neutral-500">
                A short label so you can recognise this token later.
              </p>
            </div>
            {formError && <p className="text-xs text-red-400">{formError}</p>}
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="ghost"
                onClick={closeDialog}
                disabled={pending}
              >
                Cancel
              </Button>
              <Button type="submit" disabled={pending || !name.trim()}>
                {pending ? "Creating..." : "Create"}
              </Button>
            </div>
          </form>
        )}

        {issued && (
          <div className="space-y-3">
            <p className="rounded bg-yellow-900/40 px-3 py-2 text-xs text-yellow-200">
              Save this token now — you won&apos;t see it again. If you lose it,
              create a new one.
            </p>
            <p className="text-xs text-neutral-400">
              Token for <span className="font-medium">{issued.name}</span>:
            </p>
            <code
              data-testid="issued-token"
              className="block break-all rounded bg-neutral-900 px-3 py-2 font-mono text-xs text-neutral-100"
            >
              {issued.token}
            </code>
            <div className="flex justify-end">
              <Button onClick={closeDialog}>Done</Button>
            </div>
          </div>
        )}
      </Dialog>
    </section>
  );
}
