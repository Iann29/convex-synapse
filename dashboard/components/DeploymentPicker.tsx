"use client";

// DeploymentPicker — the green-pill in-header deployment switcher
// rendered above the iframed Convex Dashboard at /embed/<name>.
//
// Visual model lifted from Convex Cloud's `DeploymentDisplay`
// (github.com/get-convex/convex-backend, npm-packages/dashboard/
// src/elements/DeploymentDisplay.tsx) but rebuilt against Synapse's
// REST API — Cloud's component leans on Big Brain hooks that don't
// exist in self-hosted.
//
// Strategy E (overlay): the picker lives in the Synapse Dashboard
// fork, ABOVE the iframe. Switching a deployment routes the parent
// page to /embed/<new-name>, which re-mounts the iframe with fresh
// credentials. Trade-off vs a forked dashboard: a full iframe reload
// on every switch instead of in-place credential swap. Acceptable
// at v1 — operators don't switch deployments hundreds of times an
// hour, and avoiding the fork saves us 1-2 weeks + ongoing rebase.
//
// v1.1 polish (this revision):
//   - Keyboard navigation in the dropdown: ↑↓ moves through items,
//     Enter selects, Escape closes.
//   - "/" hotkey opens the dropdown and focuses the search input
//     (when one is shown — see below).
//   - Search filter at the top of the menu when there are 6+
//     deployments. Filters by name + type + reference, case-
//     insensitive. The threshold avoids cluttering the picker for
//     small projects.
//   - Status indicator next to the type dot on the pill (running /
//     provisioning / failed / stopped). Different shade so operators
//     can distinguish a provisioning prod from a running prod at a
//     glance.
//   - Last-viewed timestamp under each item ("visited 3m ago"),
//     pulled from localStorage. Recency hint only renders past 1m
//     so the picker isn't noisy in normal use.

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useMemo, useRef, useState } from "react";
import type { Deployment } from "@/lib/api";

// Deployment-type styling. Match the cloud picker pixel-for-pixel
// where it's stable: dev = blue, prod = green, preview = orange.
// The custom type ("self-hosted with no Convex-Cloud-style class")
// gets a neutral palette since there's no precedent in Cloud.
const TYPE_STYLES: Record<
  string,
  { dot: string; text: string; bg: string; border: string; label: string }
> = {
  prod: {
    dot: "bg-green-500",
    text: "text-green-300",
    bg: "bg-green-500/10",
    border: "border-green-500/30",
    label: "Production",
  },
  dev: {
    dot: "bg-blue-500",
    text: "text-blue-300",
    bg: "bg-blue-500/10",
    border: "border-blue-500/30",
    label: "Development",
  },
  preview: {
    dot: "bg-orange-500",
    text: "text-orange-300",
    bg: "bg-orange-500/10",
    border: "border-orange-500/30",
    label: "Preview",
  },
  custom: {
    dot: "bg-neutral-400",
    text: "text-neutral-200",
    bg: "bg-neutral-500/10",
    border: "border-neutral-500/30",
    label: "Custom",
  },
};

// Status colour ramp for the secondary dot on the pill. Mirrors
// the Badge tones used elsewhere in the dashboard so a "provisioning"
// deployment has the same visual signal as a provisioning row in
// the project page.
const STATUS_STYLES: Record<
  string,
  { dot: string; ring: string; label: string }
> = {
  running: { dot: "bg-emerald-400", ring: "ring-emerald-400/30", label: "running" },
  provisioning: {
    dot: "bg-amber-400",
    ring: "ring-amber-400/30",
    label: "provisioning",
  },
  failed: { dot: "bg-rose-500", ring: "ring-rose-500/30", label: "failed" },
  stopped: { dot: "bg-neutral-500", ring: "ring-neutral-500/30", label: "stopped" },
};

const DEFAULT_STATUS_STYLE = {
  dot: "bg-neutral-500",
  ring: "ring-neutral-500/30",
  label: "unknown",
};

const SEARCH_THRESHOLD = 6;
const RECENCY_VISIBLE_AFTER_MS = 60_000; // suppress "0m ago" / "just now"

function styleFor(d: Deployment) {
  const t = (d.deploymentType ?? d.type ?? "dev").toString();
  return TYPE_STYLES[t] ?? TYPE_STYLES.custom;
}

function statusStyleFor(d: Deployment) {
  return STATUS_STYLES[(d.status ?? "").toLowerCase()] ?? DEFAULT_STATUS_STYLE;
}

// Local-storage key for "last time the operator landed on /embed/<name>".
// Kept under the same project id so a user with several projects sees
// independent recency per project.
const recencyKey = (projectId: string, name: string) =>
  `synapse.lastViewedAt.${projectId}.${name}`;

function recencyLabel(ts: number | null): string | null {
  if (!ts) return null;
  const ms = Date.now() - ts;
  if (ms < RECENCY_VISIBLE_AFTER_MS) return null;
  const min = Math.floor(ms / 60_000);
  if (min < 60) return `visited ${min}m ago`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `visited ${hr}h ago`;
  const d = Math.floor(hr / 24);
  return `visited ${d}d ago`;
}

export function DeploymentPicker({
  current,
  deployments,
  teamRef,
  projectId,
}: {
  current: Deployment;
  deployments: Deployment[];
  teamRef: string;
  projectId: string;
}) {
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [activeIdx, setActiveIdx] = useState(-1);
  const buttonRef = useRef<HTMLButtonElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);
  const searchRef = useRef<HTMLInputElement | null>(null);
  // Re-render once a minute so recency labels tick from "1m ago" to
  // "2m ago" without operator action. Cheap — the picker is at most
  // mounted on one tab.
  const [, setTick] = useState(0);
  useEffect(() => {
    if (!open) return;
    const t = setInterval(() => setTick((v) => v + 1), 60_000);
    return () => clearInterval(t);
  }, [open]);

  // Group by type, default-first inside each group, newest first
  // after that. Mirrors the cloud picker's sort order so operators
  // who switched from Cloud see familiar ordering.
  const groups = useMemo(() => {
    const sorter = (a: Deployment, b: Deployment) => {
      if (!!a.isDefault !== !!b.isDefault) return a.isDefault ? -1 : 1;
      const ta = new Date(a.createdAt ?? 0).getTime();
      const tb = new Date(b.createdAt ?? 0).getTime();
      return tb - ta;
    };
    const byType = (t: string) =>
      deployments
        .filter((d) => (d.deploymentType ?? d.type) === t)
        .sort(sorter);
    return {
      prod: byType("prod"),
      dev: byType("dev"),
      preview: byType("preview"),
      custom: byType("custom"),
    };
  }, [deployments]);

  // Search filter — applied per-section. Matches name OR
  // deploymentType OR reference (case-insensitive). Only renders
  // the input when there are enough deployments to justify it.
  const showSearch = deployments.length >= SEARCH_THRESHOLD;
  const matchesQuery = (d: Deployment): boolean => {
    if (!query.trim()) return true;
    const q = query.trim().toLowerCase();
    const haystack = [
      d.name,
      d.deploymentType ?? d.type,
      d.reference ?? "",
    ]
      .filter(Boolean)
      .join(" ")
      .toLowerCase();
    return haystack.includes(q);
  };

  // Filtered groups — same shape as `groups` but with non-matching
  // entries removed. Drives both the dropdown render and the
  // keyboard-nav flat list.
  const filteredGroups = useMemo(() => {
    return {
      prod: groups.prod.filter(matchesQuery),
      dev: groups.dev.filter(matchesQuery),
      preview: groups.preview.filter(matchesQuery),
      custom: groups.custom.filter(matchesQuery),
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [groups, query]);

  // Flat list of items in dropdown order — what arrow keys traverse.
  // Mirrors render order: prod → dev → preview → custom.
  const flatItems: Deployment[] = useMemo(
    () => [
      ...filteredGroups.prod,
      ...filteredGroups.dev,
      ...filteredGroups.preview,
      ...filteredGroups.custom,
    ],
    [filteredGroups],
  );

  // Picker is purely informational when there's only one deployment —
  // render the pill as a static label, no dropdown, no shortcuts.
  const pickerEnabled = deployments.length > 1;

  // Reset active index + query whenever the menu reopens (or the
  // filtered list changes underneath us).
  useEffect(() => {
    if (!open) {
      setQuery("");
      setActiveIdx(-1);
      return;
    }
    setActiveIdx(-1);
  }, [open]);

  // Clamp active index when the filter shrinks the list.
  useEffect(() => {
    if (activeIdx >= flatItems.length) setActiveIdx(-1);
  }, [flatItems.length, activeIdx]);

  // Recency lookup — read once per render. localStorage isn't
  // available during SSR; guard.
  const recencyFor = (d: Deployment): string | null => {
    if (typeof window === "undefined") return null;
    const raw = window.localStorage.getItem(recencyKey(projectId, d.name));
    if (!raw) return null;
    const ts = parseInt(raw, 10);
    if (Number.isNaN(ts)) return null;
    return recencyLabel(ts);
  };

  const switchTo = (d: Deployment) => {
    if (d.name === current.name) {
      setOpen(false);
      return;
    }
    setOpen(false);
    router.push(`/embed/${encodeURIComponent(d.name)}`);
  };

  // Keyboard shortcuts. Two regimes:
  //   1. Always-on (when the picker is enabled): Ctrl+Alt+1 → first
  //      prod, Ctrl+Alt+2 → first dev, "/" → focus the picker /
  //      search.
  //   2. Menu-scoped (only when the dropdown is open): ↑↓ traverses
  //      the flat list, Enter selects, Escape closes.
  useEffect(() => {
    if (!pickerEnabled) return;
    function onKey(e: KeyboardEvent) {
      // Ctrl+Alt shortcuts (Cloud-style).
      if (e.ctrlKey && e.altKey) {
        if (e.key === "1" && groups.prod[0]) {
          e.preventDefault();
          switchTo(groups.prod[0]);
          return;
        }
        if (e.key === "2" && groups.dev[0]) {
          e.preventDefault();
          switchTo(groups.dev[0]);
          return;
        }
      }
      // "/" opens the picker + focuses search (if the dropdown isn't
      // already inside an editable element). Match GitHub / Linear's
      // muscle memory.
      if (e.key === "/" && !open) {
        const target = e.target as HTMLElement | null;
        const tag = target?.tagName ?? "";
        if (tag === "INPUT" || tag === "TEXTAREA" || target?.isContentEditable) {
          return;
        }
        e.preventDefault();
        setOpen(true);
        // Focus the search input on the next paint, after the menu
        // has actually mounted.
        requestAnimationFrame(() => searchRef.current?.focus());
        return;
      }
      // Menu-scoped keys.
      if (!open) return;
      if (e.key === "Escape") {
        e.preventDefault();
        setOpen(false);
        buttonRef.current?.focus();
        return;
      }
      if (e.key === "ArrowDown") {
        e.preventDefault();
        if (flatItems.length === 0) return;
        setActiveIdx((idx) => (idx + 1) % flatItems.length);
        return;
      }
      if (e.key === "ArrowUp") {
        e.preventDefault();
        if (flatItems.length === 0) return;
        setActiveIdx((idx) => (idx <= 0 ? flatItems.length - 1 : idx - 1));
        return;
      }
      if (e.key === "Enter") {
        if (activeIdx >= 0 && activeIdx < flatItems.length) {
          e.preventDefault();
          switchTo(flatItems[activeIdx]);
        }
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pickerEnabled, groups, current.name, open, flatItems, activeIdx]);

  // Click-outside to close the menu.
  useEffect(() => {
    if (!open) return;
    function onClick(e: MouseEvent) {
      const target = e.target as Node;
      if (
        buttonRef.current?.contains(target) ||
        menuRef.current?.contains(target)
      ) {
        return;
      }
      setOpen(false);
    }
    document.addEventListener("mousedown", onClick);
    return () => document.removeEventListener("mousedown", onClick);
  }, [open]);

  const currentStyle = styleFor(current);
  const currentStatusStyle = statusStyleFor(current);

  // Track which flat-index belongs to which deployment name so each
  // item knows its keyboard cursor position. Built once per render.
  const indexByName = useMemo(() => {
    const m = new Map<string, number>();
    flatItems.forEach((d, i) => m.set(d.name, i));
    return m;
  }, [flatItems]);

  return (
    <div className="relative">
      <button
        ref={buttonRef}
        type="button"
        onClick={() => pickerEnabled && setOpen((v) => !v)}
        disabled={!pickerEnabled}
        aria-haspopup="menu"
        aria-expanded={open}
        data-testid="deployment-picker-pill"
        title={current.status ? `Status: ${current.status}` : undefined}
        className={[
          "flex items-center gap-2 rounded-full border px-3 py-1 text-sm transition-colors",
          currentStyle.bg,
          currentStyle.border,
          currentStyle.text,
          pickerEnabled
            ? "hover:bg-opacity-20 cursor-pointer"
            : "cursor-default",
        ].join(" ")}
      >
        <span className={`h-2 w-2 rounded-full ${currentStyle.dot}`} />
        <span className="font-medium">
          {currentStyle.label}
          {current.adopted ? " (adopted)" : ""}
        </span>
        <span className="text-neutral-400">·</span>
        <span className="font-mono text-xs text-neutral-200">
          {current.name}
        </span>
        <span
          data-testid="deployment-picker-status"
          aria-label={`Status: ${currentStatusStyle.label}`}
          className={[
            "ml-1 h-1.5 w-1.5 rounded-full ring-2",
            currentStatusStyle.dot,
            currentStatusStyle.ring,
          ].join(" ")}
        />
        {pickerEnabled && <CaretIcon />}
      </button>

      {open && (
        <div
          ref={menuRef}
          role="menu"
          data-testid="deployment-picker-menu"
          className="absolute left-0 top-full z-50 mt-2 w-80 overflow-hidden rounded-lg border border-neutral-800 bg-neutral-950 shadow-xl"
        >
          {showSearch && (
            <div className="border-b border-neutral-800 p-2">
              <input
                ref={searchRef}
                type="text"
                value={query}
                onChange={(e) => {
                  setQuery(e.target.value);
                  setActiveIdx(0);
                }}
                placeholder="Filter by name, type, reference…"
                aria-label="Filter deployments"
                data-testid="deployment-picker-search"
                className="h-8 w-full rounded-md border border-neutral-700 bg-neutral-900 px-2 text-xs text-neutral-100 placeholder:text-neutral-500 focus:border-neutral-500 focus:outline-none"
              />
            </div>
          )}
          <Section
            title="Production"
            items={filteredGroups.prod}
            currentName={current.name}
            shortcut={["Ctrl", "Alt", "1"]}
            onSelect={switchTo}
            activeName={
              activeIdx >= 0 && activeIdx < flatItems.length
                ? flatItems[activeIdx].name
                : null
            }
            indexByName={indexByName}
            recencyFor={recencyFor}
            emptyHint={
              query.trim() ? null : (
                <Link
                  href={`/teams/${encodeURIComponent(teamRef)}/${encodeURIComponent(projectId)}`}
                  className="block px-3 py-2 text-xs text-neutral-500 hover:bg-neutral-900"
                >
                  Open the project page to create a Production deployment
                </Link>
              )
            }
          />
          <Section
            title="Development"
            items={filteredGroups.dev}
            currentName={current.name}
            shortcut={["Ctrl", "Alt", "2"]}
            onSelect={switchTo}
            activeName={
              activeIdx >= 0 && activeIdx < flatItems.length
                ? flatItems[activeIdx].name
                : null
            }
            indexByName={indexByName}
            recencyFor={recencyFor}
          />
          {filteredGroups.preview.length > 0 && (
            <Section
              title="Preview Deployments"
              items={filteredGroups.preview}
              currentName={current.name}
              onSelect={switchTo}
              activeName={
                activeIdx >= 0 && activeIdx < flatItems.length
                  ? flatItems[activeIdx].name
                  : null
              }
              indexByName={indexByName}
              recencyFor={recencyFor}
            />
          )}
          {filteredGroups.custom.length > 0 && (
            <Section
              title="Custom"
              items={filteredGroups.custom}
              currentName={current.name}
              onSelect={switchTo}
              activeName={
                activeIdx >= 0 && activeIdx < flatItems.length
                  ? flatItems[activeIdx].name
                  : null
              }
              indexByName={indexByName}
              recencyFor={recencyFor}
            />
          )}
          {query.trim() && flatItems.length === 0 && (
            <p className="px-3 py-4 text-center text-xs text-neutral-500">
              No deployments match{" "}
              <code className="font-mono text-neutral-300">
                {query.trim()}
              </code>
            </p>
          )}
          <div className="border-t border-neutral-800">
            <Link
              href={`/teams/${encodeURIComponent(teamRef)}/${encodeURIComponent(projectId)}`}
              className="block px-3 py-2 text-sm text-neutral-300 hover:bg-neutral-900"
              data-testid="deployment-picker-project-link"
            >
              <span className="inline-flex items-center gap-2">
                <GearIcon />
                Project page
              </span>
            </Link>
          </div>
        </div>
      )}
    </div>
  );
}

function Section({
  title,
  items,
  currentName,
  onSelect,
  shortcut,
  emptyHint,
  activeName,
  indexByName,
  recencyFor,
}: {
  title: string;
  items: Deployment[];
  currentName: string;
  onSelect: (d: Deployment) => void;
  shortcut?: string[];
  emptyHint?: React.ReactNode;
  activeName: string | null;
  indexByName: Map<string, number>;
  recencyFor: (d: Deployment) => string | null;
}) {
  if (items.length === 0 && !emptyHint) return null;
  return (
    <div className="border-b border-neutral-800 last:border-b-0">
      <p className="px-3 pt-2 text-[10px] font-semibold uppercase tracking-wider text-neutral-500">
        {title}
      </p>
      {items.length === 0 ? (
        emptyHint
      ) : (
        <ul className="py-1">
          {items.map((d, idx) => {
            const style = statusStyleFor(d);
            const typeStyle = (() => {
              const t = (d.deploymentType ?? d.type ?? "dev").toString();
              return TYPE_STYLES[t] ?? TYPE_STYLES.custom;
            })();
            const active = d.name === currentName;
            const keyboardActive = activeName === d.name;
            const recency = recencyFor(d);
            return (
              <li key={d.name}>
                <button
                  type="button"
                  onClick={() => onSelect(d)}
                  data-testid={`deployment-picker-item-${d.name}`}
                  data-keyboard-active={keyboardActive ? "true" : undefined}
                  data-flat-index={indexByName.get(d.name)}
                  className={[
                    "flex w-full flex-col gap-0.5 px-3 py-2 text-left text-sm",
                    keyboardActive
                      ? "bg-neutral-800 text-neutral-100"
                      : active
                        ? "bg-neutral-900 text-neutral-100"
                        : "text-neutral-300 hover:bg-neutral-900",
                  ].join(" ")}
                >
                  <span className="flex items-center justify-between gap-2">
                    <span className="flex items-center gap-2 min-w-0">
                      <span
                        className={`h-2 w-2 rounded-full ${typeStyle.dot}`}
                      />
                      <span className="truncate font-mono text-xs">
                        {d.name}
                      </span>
                      <span
                        title={`Status: ${style.label}`}
                        className={`h-1.5 w-1.5 rounded-full ${style.dot}`}
                      />
                      {d.isDefault && (
                        <span className="text-[10px] uppercase text-neutral-500">
                          default
                        </span>
                      )}
                    </span>
                    {idx === 0 && shortcut && (
                      <span className="flex shrink-0 items-center gap-0.5 text-[10px] text-neutral-500">
                        {shortcut.map((k, i) => (
                          <kbd
                            key={i}
                            className="rounded border border-neutral-700 bg-neutral-900 px-1 py-0.5 font-mono"
                          >
                            {k}
                          </kbd>
                        ))}
                      </span>
                    )}
                  </span>
                  {recency && (
                    <span
                      className="pl-4 text-[10px] text-neutral-500"
                      data-testid={`deployment-picker-recency-${d.name}`}
                    >
                      {recency}
                    </span>
                  )}
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

function CaretIcon() {
  return (
    <svg
      width="10"
      height="10"
      viewBox="0 0 12 12"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M3 4.5l3 3 3-3" />
    </svg>
  );
}

function GearIcon() {
  return (
    <svg
      width="14"
      height="14"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1-2.83 0l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-2 2 2 2 0 0 1-2-2v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83 0 2 2 0 0 1 0-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1-2-2 2 2 0 0 1 2-2h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 2-2 2 2 0 0 1 2 2v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 2 2 2 2 0 0 1-2 2h-.09a1.65 1.65 0 0 0-1.51 1z" />
    </svg>
  );
}
