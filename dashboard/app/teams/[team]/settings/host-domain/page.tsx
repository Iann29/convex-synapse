"use client";

import useSWR from "swr";
import { Card, CardBody } from "@/components/ui/card";
import { HostDomainPanel } from "@/components/HostDomainPanel";
import { api } from "@/lib/api";
import type { User } from "@/lib/auth";

// Host domain settings — instance-admin-only.
//
// The settings sidebar (settings/layout.tsx) hides the link entirely for
// non-admins, but a non-admin landing here directly via URL still needs
// a graceful fallback rather than a 403 leaked through SWR. We re-check
// /me here and render a "not authorised" card when the flag is missing.
//
// The actual panel lives in components/HostDomainPanel.tsx so it can be
// remounted under a hypothetical /admin route later without dragging
// settings layout chrome along.
export default function TeamHostDomainPage() {
  const { data: me, isLoading } = useSWR<User>("/me", () => api.me(), {
    revalidateOnFocus: false,
    shouldRetryOnError: false,
  });

  if (isLoading) {
    return (
      <Card>
        <CardBody>
          <p className="text-xs text-neutral-500">Loading…</p>
        </CardBody>
      </Card>
    );
  }

  if (!me?.isInstanceAdmin) {
    return (
      <Card data-testid="host-domain-not-admin">
        <CardBody className="space-y-2">
          <p className="text-sm font-medium text-neutral-200">
            Instance admin only
          </p>
          <p className="text-xs text-neutral-500">
            Changing the host&rsquo;s public domain is reserved for the
            operator who owns this Synapse install. Ask your instance
            admin if you need this changed.
          </p>
        </CardBody>
      </Card>
    );
  }

  return <HostDomainPanel />;
}
