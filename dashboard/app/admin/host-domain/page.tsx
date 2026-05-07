"use client";

import { HostDomainPanel } from "@/components/HostDomainPanel";

// Host domain — the URL where operators reach the Synapse host itself
// (dashboard + API). Distinct from per-deployment domains, which live
// under each deployment's settings. The layout (admin/layout.tsx)
// already enforces is_instance_admin gating + redirects non-admins, so
// this page is the panel and nothing else.
export default function AdminHostDomainPage() {
  return <HostDomainPanel />;
}
