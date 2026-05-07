"use client";

import { DnsCredentialsPanel } from "@/components/DnsCredentialsPanel";

// DNS credentials — instance-admin surface for managing DNS provider
// tokens (Cloudflare today). Stored credentials are referenced by the
// per-deployment custom-domain auto_configure flow so Synapse can push
// records into the operator's zone instead of asking them to copy/paste
// an A record by hand. Layout (admin/layout.tsx) handles the
// is_instance_admin gate; this page is the panel and nothing else.
export default function AdminDnsCredentialsPage() {
  return <DnsCredentialsPanel />;
}
