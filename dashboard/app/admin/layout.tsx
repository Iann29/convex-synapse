"use client";

import Link from "next/link";
import { useRouter, usePathname } from "next/navigation";
import { useEffect } from "react";
import clsx from "clsx";
import useSWR from "swr";
import { Card, CardBody } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { User } from "@/lib/auth";

// Admin shell — host-wide configuration that's only meaningful when you
// own the box Synapse runs on. Distinct from /teams/<ref>/settings,
// which is per-team. The intent is one home for every "instance admin"
// surface (host domain today; instance audit, host backup config,
// global metrics if/when those land).
//
// Auth: gated by users.is_instance_admin (server enforces, dashboard
// hides the route entirely for non-admins). Direct-URL access by a
// non-admin redirects to /teams.
export default function AdminLayout({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const pathname = usePathname() ?? "";

  const { data: me, isLoading } = useSWR<User>("/me", () => api.me(), {
    revalidateOnFocus: false,
    shouldRetryOnError: false,
  });

  useEffect(() => {
    if (!isLoading && me && me.isInstanceAdmin !== true) {
      router.replace("/teams");
    }
  }, [me, isLoading, router]);

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
    // Brief flash before the redirect lands. Render nothing instead of
    // surfacing the layout chrome to a non-admin.
    return null;
  }

  const navItems = [
    { href: "/admin/host-domain", label: "Host domain", testid: "admin-nav-host-domain" },
  ];

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight text-neutral-100">
          Admin
        </h1>
        <p className="mt-1 text-sm text-neutral-400">
          Host-wide configuration for the operator who owns this Synapse install.
        </p>
      </div>

      <div className="grid gap-8 md:grid-cols-[220px_minmax(0,1fr)]">
        <aside className="md:sticky md:top-20 md:self-start">
          <nav className="space-y-1" aria-label="Admin sections">
            <ul className="space-y-0.5">
              {navItems.map((it) => (
                <li key={it.label}>
                  <AdminNavItem
                    href={it.href}
                    label={it.label}
                    testid={it.testid}
                    active={pathname.startsWith(it.href)}
                  />
                </li>
              ))}
            </ul>
          </nav>
        </aside>
        <div className="min-w-0 space-y-6">{children}</div>
      </div>
    </div>
  );
}

function AdminNavItem({
  href,
  label,
  testid,
  active,
}: {
  href: string;
  label: string;
  testid?: string;
  active: boolean;
}) {
  return (
    <Link
      href={href}
      data-testid={testid}
      className={clsx(
        "flex items-center justify-between rounded-md px-3 py-1.5 text-sm transition-colors",
        active
          ? "bg-violet-500/10 text-violet-200"
          : "text-neutral-400 hover:bg-neutral-900 hover:text-neutral-100",
      )}
    >
      <span>{label}</span>
    </Link>
  );
}
