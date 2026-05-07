"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { use } from "react";
import clsx from "clsx";
import useSWR from "swr";
import { Badge } from "@/components/ui/badge";
import { api } from "@/lib/api";
import type { User } from "@/lib/auth";

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

  // Pull /me here so the sidebar can show host-level admin items only
  // for users with is_instance_admin. Non-admins should not even see
  // the link — backend re-checks anyway, but exposing the route invites
  // confusion. Cached at the SWR layer so this is essentially free.
  const { data: me } = useSWR<User>("/me", () => api.me(), {
    revalidateOnFocus: false,
    shouldRetryOnError: false,
  });
  const isInstanceAdmin = me?.isInstanceAdmin === true;

  // Settings IA. Synapse is open-source self-hosted — Cloud-only concerns
  // (Billing, Usage, Referrals, Applications, paid-tier SSO) are not on
  // the roadmap and would just be dead UI. Audit Log lives at
  // /teams/{ref}/audit (top-level link in the team header) so it's not
  // duplicated here either.
  //
  // The "Instance" group is host-wide configuration that's only meaningful
  // when you own the box Synapse runs on. We hide the whole group for
  // non-admins — leaving an empty section header behind looks broken.
  const groups: { label?: string; items: NavItem[] }[] = [
    {
      items: [
        { href: `${base}/general`, label: "General" },
        { href: `${base}/members`, label: "Members" },
        { href: `${base}/access-tokens`, label: "Access Tokens" },
      ],
    },
  ];
  if (isInstanceAdmin) {
    groups.push({
      label: "Instance",
      items: [
        {
          href: `${base}/host-domain`,
          label: "Host domain",
          testid: "settings-nav-host-domain",
        },
      ],
    });
  }

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
              <div key={i} className="space-y-1">
                {g.label && (
                  <p className="px-3 text-[10px] font-semibold uppercase tracking-wide text-neutral-500">
                    {g.label}
                  </p>
                )}
                <ul className="space-y-0.5">
                  {g.items.map((it) => (
                    <li key={it.label}>
                      <SettingsNavItem
                        item={it}
                        active={!!it.href && pathname.startsWith(it.href)}
                      />
                    </li>
                  ))}
                </ul>
              </div>
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
  testid?: string;
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
      data-testid={item.testid}
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
