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

function styleFor(d: Deployment) {
  const t = (d.deploymentType ?? d.type ?? "dev").toString();
  return TYPE_STYLES[t] ?? TYPE_STYLES.custom;
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
  const buttonRef = useRef<HTMLButtonElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);

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

  // Picker is purely informational when there's only one deployment —
  // render the pill as a static label, no dropdown, no shortcuts.
  const pickerEnabled = deployments.length > 1;

  // Keyboard shortcuts: Ctrl+Alt+1 → first prod, Ctrl+Alt+2 → first dev.
  // Matches the cloud picker's bindings. Only active when the picker
  // would actually do something (multi-deployment).
  useEffect(() => {
    if (!pickerEnabled) return;
    function onKey(e: KeyboardEvent) {
      if (!(e.ctrlKey && e.altKey)) return;
      if (e.key === "1" && groups.prod[0]) {
        e.preventDefault();
        switchTo(groups.prod[0]);
      } else if (e.key === "2" && groups.dev[0]) {
        e.preventDefault();
        switchTo(groups.dev[0]);
      }
    }
    function switchTo(d: Deployment) {
      if (d.name === current.name) return;
      router.push(`/embed/${encodeURIComponent(d.name)}`);
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [pickerEnabled, groups, current.name, router]);

  // Click-outside to close the menu. Refs let us spare the document
  // listener from running during the (vast majority of) time the menu
  // is closed.
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

  const onSelect = (d: Deployment) => {
    setOpen(false);
    if (d.name === current.name) return;
    router.push(`/embed/${encodeURIComponent(d.name)}`);
  };

  const currentStyle = styleFor(current);

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
        {pickerEnabled && <CaretIcon />}
      </button>

      {open && (
        <div
          ref={menuRef}
          role="menu"
          data-testid="deployment-picker-menu"
          className="absolute left-0 top-full z-50 mt-2 w-80 overflow-hidden rounded-lg border border-neutral-800 bg-neutral-950 shadow-xl"
        >
          <Section
            title="Production"
            items={groups.prod}
            currentName={current.name}
            shortcut={["Ctrl", "Alt", "1"]}
            onSelect={onSelect}
            emptyHint={
              <Link
                href={`/teams/${encodeURIComponent(teamRef)}/${encodeURIComponent(projectId)}`}
                className="block px-3 py-2 text-xs text-neutral-500 hover:bg-neutral-900"
              >
                Open the project page to create a Production deployment
              </Link>
            }
          />
          <Section
            title="Development"
            items={groups.dev}
            currentName={current.name}
            shortcut={["Ctrl", "Alt", "2"]}
            onSelect={onSelect}
          />
          {groups.preview.length > 0 && (
            <Section
              title="Preview Deployments"
              items={groups.preview}
              currentName={current.name}
              onSelect={onSelect}
            />
          )}
          {groups.custom.length > 0 && (
            <Section
              title="Custom"
              items={groups.custom}
              currentName={current.name}
              onSelect={onSelect}
            />
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
}: {
  title: string;
  items: Deployment[];
  currentName: string;
  onSelect: (d: Deployment) => void;
  shortcut?: string[];
  emptyHint?: React.ReactNode;
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
            const style = styleFor(d);
            const active = d.name === currentName;
            return (
              <li key={d.name}>
                <button
                  type="button"
                  onClick={() => onSelect(d)}
                  data-testid={`deployment-picker-item-${d.name}`}
                  className={[
                    "flex w-full items-center justify-between gap-2 px-3 py-2 text-left text-sm",
                    active
                      ? "bg-neutral-900 text-neutral-100"
                      : "text-neutral-300 hover:bg-neutral-900",
                  ].join(" ")}
                >
                  <span className="flex items-center gap-2 min-w-0">
                    <span className={`h-2 w-2 rounded-full ${style.dot}`} />
                    <span className="truncate font-mono text-xs">
                      {d.name}
                    </span>
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
