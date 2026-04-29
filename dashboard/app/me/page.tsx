"use client";

import useSWR from "swr";
import { Avatar } from "@/components/ui/avatar";
import { Card, CardBody, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { TokensPanel } from "@/components/TokensPanel";
import { api } from "@/lib/api";
import { type User } from "@/lib/auth";

// /me — the authenticated user's account page. Identity card up top,
// personal-access-token panel below. Future surfaces (password change, 2FA,
// notification prefs) slot in as additional cards.
export default function MePage() {
  const { data, error, isLoading } = useSWR<User>("/me", () => api.me());

  return (
    <div className="space-y-8">
      <header>
        <h1 className="text-2xl font-semibold tracking-tight text-neutral-100">
          Account
        </h1>
        <p className="mt-1 text-sm text-neutral-400">
          Your Synapse identity and API access tokens.
        </p>
      </header>

      <Card>
        <CardHeader>
          <CardTitle>Profile</CardTitle>
          <CardDescription>
            Visible to teammates inside any team you belong to.
          </CardDescription>
        </CardHeader>
        <CardBody>
          {isLoading && (
            <div className="flex items-center gap-4">
              <Skeleton className="h-12 w-12 rounded-full" />
              <div className="flex-1 space-y-2">
                <Skeleton className="h-4 w-1/3" />
                <Skeleton className="h-3 w-1/4" />
              </div>
            </div>
          )}
          {error && (
            <p className="text-xs text-red-400">
              Failed to load profile: {(error as Error).message}
            </p>
          )}
          {data && (
            <div className="flex flex-col gap-5 sm:flex-row sm:items-center">
              <Avatar
                seed={data.email}
                label={data.name || data.email}
                size="lg"
                className="h-14 w-14 text-lg"
              />
              <dl className="grid flex-1 grid-cols-[6rem_1fr] gap-y-2 text-sm">
                <dt className="text-neutral-500">Name</dt>
                <dd className="text-neutral-100">{data.name || "—"}</dd>
                <dt className="text-neutral-500">Email</dt>
                <dd className="text-neutral-100">{data.email}</dd>
                <dt className="text-neutral-500">User id</dt>
                <dd className="font-mono text-xs text-neutral-300 break-all">
                  {data.id}
                </dd>
              </dl>
            </div>
          )}
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
