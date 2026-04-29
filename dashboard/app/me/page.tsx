"use client";

import useSWR from "swr";
import { Card, CardBody } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { TokensPanel } from "@/components/TokensPanel";
import { api } from "@/lib/api";
import { type User } from "@/lib/auth";

// /me — the authenticated user's account page. Shows identity + the
// personal-access-token panel. Designed as a single column to keep the
// surface area small; future settings (password change, 2FA, etc.) slot in
// as additional sections below.
export default function MePage() {
  const { data, error, isLoading } = useSWR<User>("/me", () => api.me());

  return (
    <div className="space-y-8">
      <header>
        <h1 className="text-xl font-semibold text-neutral-100">Account</h1>
        <p className="text-xs text-neutral-400">
          Your Synapse identity and API access tokens.
        </p>
      </header>

      <section className="space-y-3" aria-labelledby="profile-heading">
        <h2 id="profile-heading" className="text-sm font-semibold text-neutral-200">
          Profile
        </h2>
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
            Failed to load profile: {(error as Error).message}
          </p>
        )}
        {data && (
          <Card>
            <CardBody>
              <dl className="grid grid-cols-[6rem_1fr] gap-y-2 text-sm">
                <dt className="text-neutral-500">Name</dt>
                <dd className="text-neutral-100">{data.name || "—"}</dd>
                <dt className="text-neutral-500">Email</dt>
                <dd className="text-neutral-100">{data.email}</dd>
                <dt className="text-neutral-500">User id</dt>
                <dd className="font-mono text-xs text-neutral-300">{data.id}</dd>
              </dl>
            </CardBody>
          </Card>
        )}
      </section>

      <TokensPanel />
    </div>
  );
}
