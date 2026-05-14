"use client";

import { useCallback, useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";

// Fires-once tour shown on the first visit to /teams after a successful
// sign-in. Surfaces the eight Synapse superpowers that an operator
// landing on a single deployment card might otherwise never discover:
// multi-team, RBAC overrides, per-deployment custom domains with auto-
// TLS, HA upgrade, audit log, deploy keys, CLI wrapper, and the
// self-update flow.
//
// State machine:
//   - localStorage["synapse-tour-completed"] === "1" → never auto-open
//   - otherwise on first mount → auto-open the dialog
//   - manual button on the topbar can re-open at any time
//
// The dialog itself is a slide-based walkthrough. Step content is
// declarative so adding a new card is a one-line append.

const TOUR_KEY = "synapse-tour-completed";

type Slide = {
  title: string;
  body: string;
  // Optional path the operator can click into to "try it now" without
  // leaving the tour — handy for "see this in action" CTAs.
  cta?: { label: string; href: string };
};

const slides: Slide[] = [
  {
    title: "You're running your own Convex control plane.",
    body:
      "Synapse is everything between you and N self-hosted Convex deployments — " +
      "teams, projects, deployment lifecycle, custom domains, audit log. " +
      "The next 7 cards show you what's at your fingertips.",
  },
  {
    title: "Multi-team workspaces, real RBAC.",
    body:
      "Invite teammates by email; give them admin / member / viewer roles per team " +
      "and override that role per project when you need finer scopes. Convex Cloud " +
      "charges per seat for this; here it's free and yours.",
    cta: { label: "Manage your team", href: "/teams" },
  },
  {
    title: "Provision a deployment in ~1 second.",
    body:
      "Each deployment is a real Convex backend container managed by a worker queue. " +
      "Single-replica by default; flip the HA toggle and Synapse converts it to a " +
      "two-replica Postgres + S3 cluster with no downtime.",
  },
  {
    title: "Per-deployment custom domains with auto-TLS.",
    body:
      "Point api.client.com at your VPS, register it under a deployment, click verify. " +
      "Synapse + Caddy on-demand TLS issues a real certificate the first time anyone " +
      "hits the URL. Cloudflare auto-configure handles the A record for you.",
    cta: { label: "Wire custom domains", href: "/admin/host-domain" },
  },
  {
    title: "Deploy keys for CI — minted and revoked from the dashboard.",
    body:
      "Each deployment can mint named admin keys for GitHub Actions, Vercel, etc. " +
      "Revoke a leaked key without rotating the whole deployment — go to a " +
      "deployment card and click Deploy keys.",
  },
  {
    title: "Audit log of every privileged action.",
    body:
      "Who created which deployment, who minted which deploy key, when teams shifted " +
      "membership. Filterable by user and action; per-team scope. " +
      "Useful when you need to explain a config change weeks after the fact.",
    cta: { label: "Open audit log", href: "/teams" },
  },
  {
    title: "One-command CLI for every workflow you'd run on Convex Cloud.",
    body:
      "npx @iann29/synapse handles login, project selection, dev, deploy, env vars, " +
      "logs. Wraps the official npx convex CLI so muscle memory carries over.",
  },
  {
    title: "Auto-update from the dashboard.",
    body:
      "When a new Synapse release ships, a yellow banner appears here and the upgrade " +
      "is one click — the host-side updater daemon handles compose pull + recreate. " +
      "No SSH required for routine maintenance.",
  },
];

export function WelcomeTour() {
  // SSR-safe: render nothing on first hydrate pass, then decide from
  // localStorage on the client. Avoids the "setState inside useEffect"
  // anti-pattern by keeping the read in a single derived state value.
  const [phase, setPhase] = useState<"loading" | "open" | "closed">("loading");
  const [step, setStep] = useState(0);

  useEffect(() => {
    if (typeof window === "undefined") return;
    const completed = window.localStorage.getItem(TOUR_KEY) === "1";
    setPhase(completed ? "closed" : "open");
  }, []);

  const close = useCallback(() => {
    setPhase("closed");
    if (typeof window !== "undefined") {
      window.localStorage.setItem(TOUR_KEY, "1");
    }
  }, []);

  const next = useCallback(() => {
    setStep((s) => Math.min(s + 1, slides.length - 1));
  }, []);

  const prev = useCallback(() => {
    setStep((s) => Math.max(s - 1, 0));
  }, []);

  if (phase !== "open") return null;
  const slide = slides[step];
  const isLast = step === slides.length - 1;

  return (
    <Dialog
      open
      onClose={close}
      className="max-w-xl"
      title={slide.title}
    >
      <div className="space-y-4">
        <p className="text-sm leading-relaxed text-neutral-300">{slide.body}</p>

        {slide.cta && (
          <div className="rounded-md border border-emerald-900/60 bg-emerald-950/30 px-3 py-2 text-xs text-emerald-200">
            Try it next:{" "}
            <a
              href={slide.cta.href}
              className="font-semibold underline underline-offset-2 hover:text-emerald-100"
              onClick={close}
            >
              {slide.cta.label} →
            </a>
          </div>
        )}

        <div className="flex items-center justify-between pt-2">
          <div className="flex gap-1.5">
            {slides.map((_, i) => (
              <span
                key={i}
                aria-hidden
                className={
                  i === step
                    ? "h-1.5 w-6 rounded-full bg-neutral-300"
                    : "h-1.5 w-1.5 rounded-full bg-neutral-700"
                }
              />
            ))}
          </div>
          <div className="flex gap-2">
            {step > 0 && (
              <Button variant="ghost" size="sm" onClick={prev}>
                Back
              </Button>
            )}
            {!isLast ? (
              <Button size="sm" onClick={next}>
                Next
              </Button>
            ) : (
              <Button size="sm" onClick={close}>
                Got it
              </Button>
            )}
          </div>
        </div>

        <div className="border-t border-neutral-900 pt-3 text-center">
          <button
            type="button"
            onClick={close}
            className="text-xs text-neutral-500 hover:text-neutral-300"
          >
            Skip tour — don&apos;t show again
          </button>
        </div>
      </div>
    </Dialog>
  );
}

// HasCompletedTour exposes the localStorage flag so callers (e.g. a
// "Replay tour" button in the topbar) can decide whether to surface
// the entry point. Returns null on SSR — callers should fall back to
// "show it" so the entry stays accessible everywhere.
export function hasCompletedTour(): boolean | null {
  if (typeof window === "undefined") return null;
  return window.localStorage.getItem(TOUR_KEY) === "1";
}

// ReplayTourLink resets the flag and reloads — single source of truth
// so any topbar/settings entry can wire to it without duplicating the
// localStorage key.
export function replayTour() {
  if (typeof window !== "undefined") {
    window.localStorage.removeItem(TOUR_KEY);
    window.location.reload();
  }
}
