"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { use } from "react";
import clsx from "clsx";
import { Badge } from "@/components/ui/badge";

type Params = { team: string };

// Team Settings shell. Two-column: a sticky sidebar nav on the left, the
// active pane on the right. Each route under /teams/<ref>/settings/* mounts
// its own page; this layout just provides chrome.
export default function TeamSettingsLayout({
  params,
  children,
}: {
  params: Promise<Params>;
  children: React.ReactNode;
}) {
  const { team: teamRef } = use(params);
  const pathname = usePathname() ?? "";
  const base = `/teams/${encodeURIComponent(teamRef)}/settings`;

  // Items follow the Convex Cloud taxonomy. Items without dedicated pages in
  // v0 are rendered disabled — the brief calls those out as "out of scope
  // for the redesign PR" but we keep them in the IA so the structure
  // matches Cloud and we can land them incrementally.
  const groups: { items: NavItem[] }[] = [
    {
      items: [
        { href: `${base}/general`, label: "General" },
        { href: `${base}/members`, label: "Members" },
        { href: `${base}/access-tokens`, label: "Access Tokens" },
      ],
    },
    {
      items: [
        { label: "Billing", disabled: true },
        { label: "Usage", disabled: true },
        { label: "Referrals", disabled: true },
        { label: "Applications", disabled: true },
        { label: "Audit Log", disabled: true, badge: "PRO" },
        { label: "Single Sign-On", disabled: true },
      ],
    },
  ];

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight text-neutral-100">
          Team Settings
        </h1>
        <p className="mt-1 text-sm text-neutral-400">
          Manage members, access, and team-wide configuration.
        </p>
      </div>

      <div className="grid gap-8 md:grid-cols-[220px_minmax(0,1fr)]">
        <aside className="md:sticky md:top-20 md:self-start">
          <nav className="space-y-6" aria-label="Team settings sections">
            {groups.map((g, i) => (
              <ul key={i} className="space-y-0.5">
                {g.items.map((it) => (
                  <li key={it.label}>
                    <SettingsNavItem
                      item={it}
                      active={!!it.href && pathname.startsWith(it.href)}
                    />
                  </li>
                ))}
              </ul>
            ))}
          </nav>
        </aside>
        <div className="min-w-0 space-y-6">{children}</div>
      </div>
    </div>
  );
}

type NavItem = {
  label: string;
  href?: string;
  disabled?: boolean;
  badge?: string;
};

function SettingsNavItem({ item, active }: { item: NavItem; active: boolean }) {
  const inner = (
    <span className="flex items-center justify-between gap-2">
      <span>{item.label}</span>
      {item.badge && (
        <Badge tone="neutral" className="text-[10px]">
          {item.badge}
        </Badge>
      )}
    </span>
  );
  if (item.disabled || !item.href) {
    return (
      <span
        className="flex cursor-not-allowed items-center justify-between rounded-md px-3 py-1.5 text-sm text-neutral-600"
        aria-disabled="true"
      >
        {inner}
      </span>
    );
  }
  return (
    <Link
      href={item.href}
      className={clsx(
        "flex items-center justify-between rounded-md px-3 py-1.5 text-sm transition-colors",
        active
          ? "bg-violet-500/10 text-violet-200"
          : "text-neutral-400 hover:bg-neutral-900 hover:text-neutral-100",
      )}
    >
      {inner}
    </Link>
  );
}
