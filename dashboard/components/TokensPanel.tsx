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
  type CreateTokenResponse,
  type ListTokensResponse,
} from "@/lib/api";

// TokensPanel renders the create/list/delete UI for personal access tokens
// at any of the four cloud-spec scopes. Default (no props) is the legacy
// user-scoped behavior — same UI we shipped on /me before scoped tokens
// landed.
//
// scope="team"|"project"|"app"|"deployment" picks the resource-specific
// endpoint via the `target` prop, which carries the URL identifier
// (team slug, project id, deployment name). Issued tokens inherit the
// scope automatically server-side.
//
// Why one component for all five scopes:
//   - The shape of "create form, issued-token reveal, list, delete row"
//     is identical regardless of scope.
//   - Dropping in a fresh component per scope diverges quickly (typo'd
//     toast string in one, missing aria-label in another).
//   - Tests can stay on stable test ids — `token-row-<name>`, `issued-token` —
//     across every page the panel renders.
//
// Delete still goes through /v1/delete_personal_access_token because
// access tokens carry the same DELETE surface regardless of scope (the
// row's user_id is the gate). One endpoint, one handler.

export type TokenScope = "user" | "team" | "project" | "app" | "deployment";

type Props =
  | {
      scope?: "user";
      target?: undefined;
      heading?: string;
      description?: string;
    }
  | {
      scope: "team" | "project" | "app" | "deployment";
      target: string;
      heading?: string;
      description?: string;
    };

const HEADINGS: Record<TokenScope, string> = {
  user: "Personal access tokens",
  team: "Team access tokens",
  project: "Project access tokens",
  app: "App tokens (preview deploy keys)",
  deployment: "Deployment access tokens",
};

const DESCRIPTIONS: Record<TokenScope, string> = {
  user: "Use these for CLI / CI / programmatic access. Tokens carry your full account permissions.",
  team: "Tokens scoped to this team — bearer can act on this team's projects and deployments only.",
  project: "Tokens scoped to this project. Bearer can act on this project's deployments only.",
  app: "Short-lived tokens for CI/CD preview deploys. Same access surface as project tokens; categorised separately for clarity.",
  deployment: "Tokens scoped to this deployment only. The strictest scope.",
};

export function TokensPanel(props: Props = { scope: "user" }) {
  const scope = props.scope ?? "user";
  const target = "target" in props ? props.target : undefined;

  // Cache key is unique per (scope, target) so multiple panels on a page
  // don't share state.
  const swrKey =
    scope === "user" ? "/tokens" : `/tokens/${scope}/${target}`;

  const list = (): Promise<ListTokensResponse> => {
    if (scope === "user") return api.tokens.list();
    if (scope === "team") return api.teams.listTokens(target!);
    if (scope === "project") return api.projects.listTokens(target!);
    if (scope === "app") return api.projects.listAppTokens(target!);
    return api.deployments.listTokens(target!);
  };
  const create = (name: string): Promise<CreateTokenResponse> => {
    if (scope === "user") return api.tokens.create(name);
    if (scope === "team") return api.teams.createToken(target!, name);
    if (scope === "project") return api.projects.createToken(target!, name);
    if (scope === "app") return api.projects.createAppToken(target!, name);
    return api.deployments.createToken(target!, name);
  };

  const { data, error, isLoading, mutate } = useSWR<ListTokensResponse>(
    swrKey,
    list,
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

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);
    if (!name.trim()) return;
    setPending(true);
    try {
      const res = await create(name.trim());
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

  const heading = props.heading ?? HEADINGS[scope];
  const description = props.description ?? DESCRIPTIONS[scope];
  const headingId = `tokens-heading-${scope}-${target ?? "self"}`;

  return (
    <section className="space-y-3" aria-labelledby={headingId}>
      <div className="flex items-center justify-between">
        <div>
          <h2
            id={headingId}
            className="text-sm font-semibold text-neutral-200"
          >
            {heading}
          </h2>
          <p className="text-xs text-neutral-500">{description}</p>
        </div>
        <Button
          onClick={() => setOpen(true)}
          data-testid={`tokens-new-${scope}`}
        >
          New token
        </Button>
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
        title={issued ? "Token created" : `New ${scope} access token`}
      >
        {!issued && (
          <form onSubmit={submit} className="space-y-4">
            <div className="space-y-2">
              <label
                htmlFor={`token-name-${scope}`}
                className="block text-xs text-neutral-400"
              >
                Name
              </label>
              <Input
                id={`token-name-${scope}`}
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
              <Button
                type="submit"
                disabled={pending || !name.trim()}
                data-testid={`tokens-create-${scope}`}
              >
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
