"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useEffect, useRef, useState } from "react";
import useSWR from "swr";
import clsx from "clsx";
import { Avatar } from "@/components/ui/avatar";
import { SynapseLogo } from "@/components/ui/logo";
import { api, type Team } from "@/lib/api";
import { clearAuth, getCurrentUser, type User } from "@/lib/auth";

// Persistent top-of-page navigation. Mirrors the structure of the Convex
// Cloud dashboard: logo on the left, team picker next to it, primary tabs
// (Home, Team Settings) in the centre, and a profile-menu disc on the
// right. Profile + team menus are popovers built in-house — no external
// popover lib needed for two simple menus.
//
// `teamRef` is derived from the URL — outside a /teams/<ref>/… route the
// picker is still shown but the active item is unset.
export function TopBar() {
  const pathname = usePathname();
  const teamRef = extractTeamRef(pathname);
  const router = useRouter();
  const [user, setUser] = useState<User | null>(null);

  useEffect(() => {
    setUser(getCurrentUser());
  }, []);

  const logout = () => {
    clearAuth();
    router.push("/login");
  };

  return (
    <header className="sticky top-0 z-40 border-b border-neutral-900 bg-neutral-950/85 backdrop-blur supports-[backdrop-filter]:bg-neutral-950/65">
      <div className="mx-auto flex h-14 max-w-7xl items-center gap-3 px-4 sm:px-6">
        <Link
          href="/teams"
          aria-label="Synapse home"
          className="flex shrink-0 items-center gap-2 rounded-md p-1 text-neutral-100 transition-colors hover:bg-neutral-900"
        >
          <SynapseLogo />
          <span className="hidden text-sm font-semibold tracking-tight sm:block">
            Synapse
          </span>
        </Link>

        <ChevronSep />

        <TeamPicker teamRef={teamRef} />

        {teamRef && (
          <nav
            className="ml-2 hidden items-center gap-0.5 md:flex"
            aria-label="Team sections"
          >
            <TabLink
              href={`/teams/${encodeURIComponent(teamRef)}`}
              label="Home"
              active={isHomeActive(pathname, teamRef)}
            />
            <TabLink
              href={`/teams/${encodeURIComponent(teamRef)}/settings`}
              label="Team Settings"
              active={pathname?.startsWith(`/teams/${teamRef}/settings`) ?? false}
            />
          </nav>
        )}

        <div className="ml-auto flex items-center gap-1">
          <a
            href="https://github.com/get-convex/convex-backend"
            target="_blank"
            rel="noopener noreferrer"
            className="hidden rounded-md px-3 py-1.5 text-xs text-neutral-400 transition-colors hover:bg-neutral-900 hover:text-neutral-200 sm:inline-block"
          >
            Docs
          </a>
          {user && <ProfileMenu user={user} onLogout={logout} />}
        </div>
      </div>
    </header>
  );
}

function isHomeActive(pathname: string | null, teamRef: string): boolean {
  if (!pathname) return false;
  const base = `/teams/${teamRef}`;
  if (pathname === base) return true;
  if (pathname.startsWith(`${base}/settings`)) return false;
  return pathname.startsWith(`${base}/`);
}

// /teams/<ref>/… — strip the prefix. Returns undefined for /teams (root) or
// /me. Decoding lets us match against api.teams.get cache keys, which use
// the unencoded ref.
function extractTeamRef(pathname: string | null): string | undefined {
  if (!pathname) return undefined;
  const m = pathname.match(/^\/teams\/([^/]+)/);
  if (!m) return undefined;
  try {
    return decodeURIComponent(m[1]);
  } catch {
    return m[1];
  }
}

function TabLink({
  href,
  label,
  active,
}: {
  href: string;
  label: string;
  active: boolean;
}) {
  return (
    <Link
      href={href}
      className={clsx(
        "relative rounded-md px-3 py-1.5 text-sm transition-colors",
        active
          ? "text-neutral-100"
          : "text-neutral-400 hover:bg-neutral-900 hover:text-neutral-200",
      )}
    >
      {label}
      {active && (
        <span
          aria-hidden
          className="absolute inset-x-3 -bottom-[15px] h-0.5 rounded-full bg-violet-500"
        />
      )}
    </Link>
  );
}

function ChevronSep() {
  return (
    <span aria-hidden className="hidden text-neutral-700 sm:inline">
      /
    </span>
  );
}

function TeamPicker({ teamRef }: { teamRef?: string }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  // List teams lazily — only when the menu opens, not on every render.
  // Once loaded SWR keeps the result so subsequent opens are instant.
  const { data: teams } = useSWR<Team[]>(
    open ? "/teams" : null,
    () => api.teams.list(),
  );

  // Resolve the current team's display name. Hits the cache populated by
  // the per-team page so on /teams/<slug> we don't double-fetch.
  const { data: current } = useSWR<Team>(
    teamRef ? ["/team", teamRef] : null,
    () => api.teams.get(teamRef!),
  );

  useEffect(() => {
    if (!open) return;
    const onClick = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onClick);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onClick);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const display = current?.name ?? teamRef ?? "Select a team";
  const seed = current?.slug ?? teamRef ?? "synapse";

  return (
    <div ref={ref} className="relative">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="menu"
        aria-expanded={open}
        className="flex h-9 items-center gap-2 rounded-md border border-transparent px-2 text-sm font-medium text-neutral-100 transition-colors hover:bg-neutral-900 focus:outline-none focus-visible:border-neutral-700 focus-visible:bg-neutral-900"
      >
        <Avatar seed={seed} label={display} size="sm" />
        <span className="max-w-[160px] truncate">{display}</span>
        <ChevronDown />
      </button>

      {open && (
        <div
          role="menu"
          className="synapse-fade-in absolute left-0 top-full z-50 mt-1.5 w-72 overflow-hidden rounded-lg border border-neutral-800 bg-[#141416] shadow-2xl"
        >
          <div className="border-b border-neutral-800 px-3 py-2 text-[11px] uppercase tracking-wider text-neutral-500">
            Teams
          </div>
          <div className="max-h-64 overflow-y-auto py-1">
            {!teams && (
              <div className="px-3 py-2 text-xs text-neutral-500">Loading…</div>
            )}
            {teams && teams.length === 0 && (
              <div className="px-3 py-2 text-xs text-neutral-500">
                You don't belong to any team yet.
              </div>
            )}
            {teams?.map((t) => {
              const active = teamRef && (teamRef === t.slug || teamRef === t.id);
              return (
                <Link
                  key={t.id}
                  href={`/teams/${encodeURIComponent(t.slug)}`}
                  onClick={() => setOpen(false)}
                  className={clsx(
                    "flex items-center gap-3 px-3 py-2 text-sm transition-colors",
                    active
                      ? "bg-violet-500/10 text-violet-200"
                      : "text-neutral-200 hover:bg-neutral-900",
                  )}
                >
                  <Avatar seed={t.slug} label={t.name} size="sm" />
                  <div className="min-w-0 flex-1">
                    <div className="truncate">{t.name}</div>
                    <div className="truncate text-[11px] text-neutral-500">
                      {t.slug}
                    </div>
                  </div>
                  {active && <Check />}
                </Link>
              );
            })}
          </div>
          <div className="border-t border-neutral-800 p-1">
            <Link
              href="/teams"
              onClick={() => setOpen(false)}
              className="flex w-full items-center gap-2 rounded-md px-3 py-2 text-sm text-neutral-200 transition-colors hover:bg-neutral-900"
            >
              <Plus />
              <span>All teams · Create new</span>
            </Link>
          </div>
        </div>
      )}
    </div>
  );
}

function ProfileMenu({ user, onLogout }: { user: User; onLogout: () => void }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onClick = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onClick);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onClick);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  return (
    <div ref={ref} className="relative flex items-center">
      {/* The avatar disc IS the Account link — aria-label "Account" is the
          contract the Playwright suite asserts on (the previous Header used
          the email text for the same role). Clicking it goes straight to
          /me; the chevron next to it opens the popover. */}
      <Link
        href="/me"
        aria-label="Account"
        className="flex items-center rounded-full focus:outline-none focus-visible:ring-2 focus-visible:ring-violet-400/70 focus-visible:ring-offset-2 focus-visible:ring-offset-neutral-950"
      >
        <Avatar seed={user.email} label={user.name || user.email} size="md" />
      </Link>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label="Open profile menu"
        className="ml-1 flex h-7 w-5 items-center justify-center rounded-md text-neutral-500 transition-colors hover:bg-neutral-900 hover:text-neutral-200 focus:outline-none focus-visible:ring-2 focus-visible:ring-violet-400/70"
      >
        <ChevronDown />
      </button>

      {open && (
        <div
          role="menu"
          className="synapse-fade-in absolute right-0 top-full z-50 mt-2 w-64 overflow-hidden rounded-lg border border-neutral-800 bg-[#141416] shadow-2xl"
        >
          <div className="flex items-center gap-3 border-b border-neutral-800 px-3 py-3">
            <Avatar seed={user.email} label={user.name || user.email} size="lg" />
            <div className="min-w-0">
              <div className="truncate text-sm text-neutral-100">
                {user.name || user.email.split("@")[0]}
              </div>
              <div className="truncate text-xs text-neutral-500">{user.email}</div>
            </div>
          </div>
          <div className="p-1">
            <Link
              href="/me"
              onClick={() => setOpen(false)}
              className="flex w-full items-center gap-2 rounded-md px-3 py-2 text-sm text-neutral-200 transition-colors hover:bg-neutral-900"
            >
              <UserIcon />
              Account & tokens
            </Link>
            <button
              type="button"
              onClick={() => {
                setOpen(false);
                onLogout();
              }}
              className="flex w-full items-center gap-2 rounded-md px-3 py-2 text-left text-sm text-neutral-200 transition-colors hover:bg-neutral-900"
            >
              <LogoutIcon />
              Logout
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

/* ----- inline icons (kept tiny so we don't add an icon dep) ----- */

function ChevronDown() {
  return (
    <svg width="12" height="12" viewBox="0 0 16 16" aria-hidden className="text-neutral-500">
      <path d="M4 6l4 4 4-4" stroke="currentColor" strokeWidth="1.6" fill="none" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

function Check() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden className="text-violet-300">
      <path d="M3.5 8.5l3 3 6-6" stroke="currentColor" strokeWidth="1.8" fill="none" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

function Plus() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden className="text-neutral-400">
      <path d="M8 3v10M3 8h10" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
    </svg>
  );
}

function UserIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden className="text-neutral-400">
      <circle cx="8" cy="6" r="2.5" stroke="currentColor" strokeWidth="1.4" fill="none" />
      <path d="M3 13c.5-2.2 2.5-3.5 5-3.5s4.5 1.3 5 3.5" stroke="currentColor" strokeWidth="1.4" fill="none" strokeLinecap="round" />
    </svg>
  );
}

function LogoutIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden className="text-neutral-400">
      <path d="M9 3h3v10H9" stroke="currentColor" strokeWidth="1.4" fill="none" strokeLinecap="round" strokeLinejoin="round" />
      <path d="M3 8h7m-2-2 2 2-2 2" stroke="currentColor" strokeWidth="1.4" fill="none" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}
