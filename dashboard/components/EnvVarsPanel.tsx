"use client";

import { useState } from "react";
import useSWR from "swr";
import { Button } from "@/components/ui/button";
import { Card, CardBody } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { ApiError, api, type EnvVar } from "@/lib/api";

type Props = { projectId: string };

// Project-level env vars are seeded into newly-provisioned deployments.
// They are NOT propagated to already-running deployments — that's a v0.2
// concern. We surface a hint in the empty state.
export function EnvVarsPanel({ projectId }: Props) {
  const { data, error, isLoading, mutate } = useSWR<EnvVar[]>(
    ["/env-vars", projectId],
    () => api.projects.listEnvVars(projectId),
  );

  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [pending, setPending] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);
    if (!name.trim()) return;
    setPending(true);
    try {
      await api.projects.updateEnvVars(projectId, [
        { op: "set", name: name.trim(), value },
      ]);
      setName("");
      setValue("");
      await mutate();
    } catch (err) {
      setFormError(
        err instanceof ApiError ? err.message : "Could not save env var",
      );
    } finally {
      setPending(false);
    }
  };

  const remove = async (n: string) => {
    setFormError(null);
    try {
      await api.projects.updateEnvVars(projectId, [{ op: "delete", name: n }]);
      await mutate();
    } catch (err) {
      setFormError(
        err instanceof ApiError ? err.message : "Could not delete env var",
      );
    }
  };

  return (
    <section className="space-y-3">
      <div>
        <h2 className="text-sm font-semibold text-neutral-200">
          Default environment variables
        </h2>
        <p className="text-xs text-neutral-500">
          Applied to deployments at creation time.
        </p>
      </div>

      {isLoading && <p className="text-xs text-neutral-500">Loading…</p>}
      {error && (
        <p className="text-xs text-red-400">
          Failed to load env vars: {(error as Error).message}
        </p>
      )}

      {data && data.length === 0 && (
        <p className="text-xs text-neutral-500">No env vars yet.</p>
      )}

      {data && data.length > 0 && (
        <Card>
          <CardBody className="divide-y divide-neutral-800 p-0">
            {data.map((v) => (
              <div
                key={v.name}
                className="flex items-center justify-between gap-3 px-4 py-2 text-sm"
              >
                <div className="min-w-0 flex-1">
                  <p className="truncate font-mono text-neutral-100">{v.name}</p>
                  <p className="truncate font-mono text-xs text-neutral-500">
                    {v.value || <span className="italic">(empty)</span>}
                  </p>
                </div>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => remove(v.name)}
                  aria-label={`Delete env var ${v.name}`}
                >
                  Delete
                </Button>
              </div>
            ))}
          </CardBody>
        </Card>
      )}

      <form onSubmit={add} className="flex flex-wrap items-end gap-2">
        <div className="flex-1 min-w-[10rem] space-y-1">
          <label htmlFor="envvar-name" className="block text-xs text-neutral-400">
            Name
          </label>
          <Input
            id="envvar-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="API_KEY"
            required
          />
        </div>
        <div className="flex-1 min-w-[12rem] space-y-1">
          <label htmlFor="envvar-value" className="block text-xs text-neutral-400">
            Value
          </label>
          <Input
            id="envvar-value"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder="…"
          />
        </div>
        <Button type="submit" disabled={pending || !name.trim()}>
          {pending ? "Saving…" : "Add"}
        </Button>
      </form>

      {formError && <p className="text-xs text-red-400">{formError}</p>}
    </section>
  );
}
