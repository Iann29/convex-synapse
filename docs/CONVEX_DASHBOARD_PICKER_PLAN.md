# Convex Dashboard — In-Header Deployment Picker

> **Status:** ✅ Shipped (Strategy E — overlay) on 2026-05-02. See §12 for the
> as-built notes. The original investigation in §1–§9 is preserved as the
> design record. The §7 open questions were resolved while the operator
> was asleep — choices documented in §13.

## 1. Goal

Reproduce, inside the **per-deployment Convex Dashboard** (Health / Data /
Functions / Files / Schedules / Logs / Settings), the same in-header
deployment picker that Convex Cloud ships:

```
┌──────────────────────────────────────────────────────────────────────┐
│ ●●● │ Avatar │ /  ctdm-performance ▾  │ ⬢ Development (Cloud) · strong-frog-880 ▾ │
│─────┴────────┴────────────────────────┴─────────────────────────────────│
│ Health                                                                 │
│ Data                                                                   │
│ Functions                                                              │
│ ...                                                                    │
└──────────────────────────────────────────────────────────────────────┘
```

Click the green pill → dropdown lists Production, Development (Cloud),
Preview Deployments, Other Deployments, Project Settings. Picking a
sibling deployment swaps context **without leaving the dashboard** —
operator stays inside Health/Data/Logs but pointed at a different backend.

This is currently the single biggest UX gap between Synapse self-hosted
and Convex Cloud.

## 2. Why this is a priority

- **Drop-in feel.** Operators arriving from Cloud expect the picker.
  Without it they have to bounce out to Synapse's project page,
  click another deployment, wait for the iframe shell to reload.
- **Multi-deployment workflows are real** (dev + prod in the same project,
  preview deploys in CI). The current "open a new tab per deployment"
  pattern is annoying for anyone who uses Synapse for more than a demo.
- **Cheap install signal.** The picker is one of the first things people
  notice in screenshots / demos. Shipping it closes a visible gap in the
  first 30 seconds of using Synapse.

## 3. State today

Synapse hosts two distinct dashboards and the line between them is the
key to the design:

| Dashboard | Source | Repo location | Renders |
|---|---|---|---|
| **Synapse Dashboard** (admin) | Forked here | `dashboard/` in this repo | Teams, projects, deployments LIST, access tokens, audit log |
| **Convex Dashboard** (per-deployment) | Upstream | `ghcr.io/get-convex/convex-dashboard` (built from `npm-packages/dashboard-self-hosted/`) | Health, Data, Functions, Files, Schedules, Logs, Settings — **scoped to ONE deployment** |

The two are wired via PR #26's iframe shell: `dashboard/app/embed/[name]/page.tsx`
loads the upstream image in an `<iframe>` and answers its postMessage handshake
with `{ adminKey, deploymentUrl, deploymentName }`. The Caddy sidecar
`convex-dashboard-proxy` strips `X-Frame-Options` + `frame-ancestors` so
the iframe renders.

**Today's flow** when an operator wants to switch from `dev-foo` to `prod-bar`:

1. They're inside the iframe shell at `/embed/dev-foo`.
2. They click "back" or navigate to the project page (`/teams/<team>/<project>`).
3. They click "Open dashboard" on the `prod-bar` row.
4. New tab opens at `/embed/prod-bar` — fresh handshake, fresh iframe,
   fresh URL.

It works, but it's three clicks and a context switch where Cloud has zero
clicks.

## 4. Findings from the upstream source

I read the relevant chunks of `get-convex/convex-backend@main` end-to-end.
Three findings shape the strategy space:

### 4.1 The upstream **already has a list-deployments protocol** (built-in)

`npm-packages/dashboard-self-hosted/src/components/DeploymentList.tsx`
expects a `listDeploymentsApiUrl` prop that points at an HTTP endpoint
returning:

```json
{
  "deployments": [
    {"name": "dev-foo",  "url": "http://...",  "adminKey": "..."},
    {"name": "prod-bar", "url": "https://...", "adminKey": "..."}
  ]
}
```

`_app.tsx` reads three knobs to figure out where this URL lives:
- `NEXT_PUBLIC_DEFAULT_LIST_DEPLOYMENTS_API_PORT` env var (build-time)
- Query params `?a=<api-url>&d=<deployment-name>` (runtime)
- Falls back to `DeploymentCredentialsForm` (manual paste of URL + admin
  key) if neither is set

Today's current `DeploymentList` UX **is interstitial**: it renders a
list of buttons as the login screen. Click one → that deployment loads
in the dashboard. There is no in-header picker. But the protocol is real
and stable.

### 4.2 The Cloud picker is a heavy component, not portable as-is

`npm-packages/dashboard/src/elements/DeploymentDisplay.tsx` (the green
pill) and `src/components/header/ProjectSelector/DeploymentMenuOptions.tsx`
(the dropdown content) implement the Cloud UX. They depend on:

- `useCurrentDeployment`, `useDeployments`, `useCurrentTeam`,
  `useTeamMembers`, `useProfile` — Cloud-only React hooks tied to Big
  Brain's GraphQL-ish API
- `useTeamEntitlements` (Pro/Enterprise plan checks)
- `useListVanityDomains` (custom domains feature)
- `udfs.convexCloudUrl.default` (a Convex query against Big Brain's
  internal deployment)
- `@convex-dev/platform/managementApi` (Cloud-only types)

You can't drop this component into `dashboard-self-hosted` without
rewriting half of it. But the **visual structure** (green pill, three
sections in the menu, Ctrl+Alt+1/2 shortcuts, deployment-type colour
classes) is a good blueprint to clone.

### 4.3 The upstream image is a single-deployment Next.js standalone

`self-hosted/docker-build/Dockerfile.dashboard` builds the image off
`npm-packages/dashboard-self-hosted` via `rush install` + `rush build`.
The output is a Next.js standalone server on port 6791. It's a 600MB
monorepo build (rushjs + multiple workspace packages: `dashboard-common`,
`design-system`, `system-udfs`, `convex`).

To **fork** the dashboard means forking the whole monorepo subset —
non-trivial but bounded.

## 5. Strategies

Four candidate approaches. Listed roughly in increasing implementation
cost.

### Strategy A — API-only, use built-in `DeploymentList` interstitial

**What:** Synapse exposes `GET /v1/internal/list_deployments?project=<id>`
returning `{deployments: [{name, url, adminKey}]}`. The `/embed/<name>`
shell sets the iframe `src` to `<dashboard>/?a=<api-url>&d=<name>` so the
upstream's built-in flow takes over.

**Pros:**
- **Zero fork.** We keep using `ghcr.io/get-convex/convex-dashboard:latest`,
  upstream upgrades are a Caddy reload.
- ~1 day of work. Backend endpoint + iframe URL change + minor security
  review.

**Cons:**
- **No header picker.** The `DeploymentList` upstream is interstitial
  only — picking a deployment loads the dashboard, but there's no way
  back without the operator hitting the browser back button.
- The UX gap from §1 stays open. Strategy A is "the API exists so we
  could go further later" — not the picker itself.

**Verdict:** Useful as a prerequisite for B/D, not a complete answer.

### Strategy B — Fork `dashboard-self-hosted`, add a header picker

**What:** Mirror `npm-packages/dashboard-self-hosted` + `dashboard-common`
+ `@convex-dev/design-system` into this repo (subtree or git submodule).
Add a single `<DeploymentPicker>` component to `DeploymentDashboardLayout`.
Rebuild as `ghcr.io/Iann29/synapse-convex-dashboard:<version>` and swap
the compose service to point at it.

**Pros:**
- **Full UX parity.** Picker in the header, dropdown with Production /
  Development / Preview / Other / Project Settings sections, Ctrl+Alt+1/2
  shortcuts, deployment-type colour pill — pixel-for-pixel reproducible.
- Switch-deployment in place: postMessage to the parent (our embed shell)
  → parent sends new credentials → React state updates → dashboard
  re-renders without a full page reload.

**Cons:**
- **Fork debt.** Upstream cuts a release every few weeks. Each one
  needs a rebase. The monorepo has multiple workspace packages, so
  even a "small" upstream change can touch 5+ files we mirror.
- **Build pipeline.** rushjs + monorepo + Next.js standalone + Docker
  multi-stage = ~10 min CI builds. Need a GitHub Actions workflow that
  publishes the image on every release.
- ~1-2 weeks of focused work to ship cleanly + 2-4 hours/month to
  maintain (upstream rebase + retest).

**Verdict:** The right answer if and only if the picker is worth ~1
person-month/year of recurring maintenance.

### Strategy C — Script-injection via the Caddy proxy

**What:** Keep the upstream image untouched. The `convex-dashboard-proxy`
Caddy sidecar already strips `X-Frame-Options`. Add a `replace_response`
directive that injects a `<script src="/synapse-picker.js">` into the
upstream's HTML. The injected script mounts a React island into the
header DOM, fetches the deployment list from Synapse, and listens to
clicks to switch.

**Pros:**
- No fork, no Docker rebuild.
- Deploys as a Caddy config change + a static JS bundle.

**Cons:**
- **Brittle.** Depends on the upstream HTML layout being stable
  (selectors, Tailwind class names). One upstream redesign and our
  injection breaks silently in production.
- CSP fights. The upstream may add a strict CSP that refuses inline
  scripts; we'd be back to a fork.
- Switch-in-place is harder — we'd need to convince the upstream's React
  app to re-render with new credentials. Probably forces a full reload.
- Hard to test (Playwright has to assert on injected DOM that didn't
  exist when the page first painted).

**Verdict:** Tactical hack. Not sustainable. Avoid unless we want a
14-day proof-of-concept while we wait for B to land.

### Strategy D — Hybrid: A as foundation, B as add-on

**What:**
1. Phase 1 — Implement Strategy A (1 day). Synapse serves the
   list-deployments API. Upstream image unchanged. Operators get the
   interstitial deployment-picker — acceptable but not great.
2. Phase 2 — Fork the dashboard (Strategy B), but ONLY the picker:
   one component file plus the `DeploymentDashboardLayout` integration.
   Use `git subtree` for the source so we can pull upstream updates.
   The fork's `npm run build` produces our own Docker image.

The picker UI built in Phase 2 reuses the API from Phase 1 — same
endpoint, same response shape. So if the fork ever falls behind upstream
and we have to drop it, we still have the interstitial flow as a working
fallback.

**Pros:**
- **Quick win in Phase 1** — operator sees progress fast.
- **Sustainable in Phase 2** — fork only what we change; upstream
  components stay shared.
- Fallback path if maintenance burden bites.

**Cons:**
- More moving pieces — two phases means two PRs, two test cycles.
- Phase 2 still pays the rushjs / monorepo / Docker tax.

**Verdict:** Recommended.

## 6. Recommendation

**Strategy D — Hybrid.** Ship A first (~1 day, low risk), keep B as
follow-up (~1-2 weeks, scoped to "add the picker, not redesign the
dashboard"). Use the same API contract for both phases so the fork is
a UI delta, not a logic delta.

## 7. Open questions for the operator

These are decisions I shouldn't make without you:

1. **Scope of the picker dropdown.** Cloud's menu has Production,
   Development (Cloud), Preview Deployments, Other Deployments, Project
   Settings. For self-hosted, "Other Deployments" (= teammates' dev
   deployments) only makes sense if multiple operators share a Synapse
   instance. Drop the section, or keep it for parity?
2. **Custom Domains tab in Settings.** Cloud's Settings sidebar has a
   "Custom Domains" entry that opens a Pro-plan upsell. Synapse v1.0
   already supports custom domains via `SYNAPSE_BASE_DOMAIN`. Do we
   want to surface a working Custom Domains UI in the dashboard
   (Phase 3) or skip it?
3. **Phase 1 acceptable as v1?** Strategy A alone (the interstitial
   `DeploymentList`) ships in a day. Do we want to release that and
   come back for the header picker, or ship Phase 1 + Phase 2 as one
   block?
4. **Fork hosting.** Phase 2's image needs to live somewhere. Three
   options:
   - `ghcr.io/Iann29/synapse-convex-dashboard:<tag>` (your namespace)
   - `ghcr.io/convex-synapse/dashboard:<tag>` (org namespace if/when
     we move to one)
   - Vendored INSIDE `convex-synapse` and built from this repo's CI
     (no separate image registry)
5. **Upstream rebase cadence.** Monthly? Per-release? Driven by user-
   reported drift? Spelling out the cadence keeps the fork from rotting.

## 8. Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| Upstream renames `DeploymentDashboardLayout` props or postMessage protocol | Medium | Pin to a specific upstream tag; bump intentionally |
| Convex Dashboard image stops accepting query-param credentials | Low | Phase 2 fork already controls credential plumbing — we'd just keep using postMessage |
| rushjs / pnpm build ergonomics get worse | Medium | Vendor build tooling; lock Node version in Dockerfile |
| Picker breaks for operators with one deployment | Low | Render as static label (no dropdown) when `deployments.length === 1` |
| Switching deployment loses unsaved Function-runner state | Medium | Show a "you have unsaved input — switch anyway?" confirm before swapping |

## 9. Phased plan (if Strategy D approved)

### Phase 1 — list-deployments API + interstitial UX (~1 day)

- [ ] Backend endpoint: `GET /v1/internal/list_deployments?project=<id>`
      returning `{deployments: [{name, url, adminKey}]}`. Auth gate:
      bearer must be admin of the project. Internal-only path (mirrors
      `/v1/internal/tls_ask` from v1.0).
- [ ] `dashboard/app/embed/[name]/page.tsx` switches the iframe `src`
      to `<dashboard>/?a=<api-url>&d=<name>`. Keep the postMessage
      handshake as fallback when the API is unreachable.
- [ ] Synapse Go integration test: list_deployments endpoint
      shape + auth gates.
- [ ] Playwright smoke: open `/embed/<name>`, confirm
      `DeploymentList` interstitial renders the sibling deployment.

### Phase 2 — header picker fork (~1-2 weeks)

- [ ] Choose vendor strategy (subtree vs submodule). Mirror
      `npm-packages/{dashboard-self-hosted,dashboard-common,design-system}`
      into a new top-level dir (e.g. `convex-dashboard-fork/`).
- [ ] Build pipeline: GitHub Actions workflow that runs `rush build`
      and pushes `ghcr.io/<ns>/synapse-convex-dashboard:<tag>`.
- [ ] Docker compose service swaps to the new image behind a
      `SYNAPSE_DASHBOARD_IMAGE` env override.
- [ ] **The patch**: a `<DeploymentPicker>` React component in
      `dashboard-self-hosted/src/components/`, mounted by
      `DeploymentDashboardLayout` from `dashboard-common/src/layouts/`.
      Reads from the list-deployments API, renders the green pill, drops
      the menu, postMessages to the parent on switch.
- [ ] postMessage protocol bump: parent (our `/embed/<name>`) responds
      to `request-credentials-for: <name>` with new creds; dashboard
      updates `deploymentUrl` + `adminKey` in `DeploymentInfoContext`
      and React re-renders.
- [ ] Playwright spec covering: switch dev → prod, switch back, picker
      hidden when only one deployment, Ctrl+Alt+1 shortcut.
- [ ] Upstream-rebase runbook in `docs/CONVEX_DASHBOARD_FORK.md`.

### Phase 3 — optional polish (~1 week, post-Phase-2)

- [ ] Settings → Custom Domains hooked up to Synapse's custom-domain API
      (operator can manage `SYNAPSE_BASE_DOMAIN` subdomains from the
      dashboard).
- [ ] Settings → Backup & Restore wired to Synapse's `setup.sh --backup`
      / `--restore` (browser-side trigger via a new Synapse endpoint).
- [ ] Settings → Components/Integrations: assess what makes sense for
      self-hosted.

## 10. What this is not

- Not a replacement for the **Synapse Dashboard** (`dashboard/` in this
  repo). The two stay distinct: Synapse Dashboard owns "across all my
  projects/deployments"; the forked Convex Dashboard owns "inside one
  deployment".
- Not a redesign. The picker matches Cloud visually so users get the
  drop-in feel; we don't try to "improve on Cloud" in v1.
- Not Big-Brain hosting. We're not standing up a copy of Convex Cloud's
  Big Brain — Synapse's REST API plays that role for self-hosted, and
  the picker just calls into it.

## 11. Next step

Operator picks Strategy A / B / C / D from §5 and answers the open
questions in §7. Then I open `feat/convex-dashboard-picker` and start
Phase 1 in a follow-up PR.

## 12. As-built (Strategy E — overlay)

While reviewing the four candidates I realised a fifth that wasn't in
the original list: **the picker doesn't have to live INSIDE the iframed
upstream dashboard at all.** It can live in the parent page (our
Synapse Dashboard fork), as an overlay header rendered ABOVE the
iframe. That sidesteps the upstream-fork tax entirely while still
giving the operator a one-click switch.

### What shipped

| Piece | Where |
|---|---|
| Picker UI | `dashboard/components/DeploymentPicker.tsx` |
| Embed shell integration | `dashboard/app/embed/[name]/page.tsx` |
| Cross-origin list-deployments endpoint (reserved for future Strategy B) | `synapse/internal/api/dashboard_proxy.go` (`GET /v1/internal/list_deployments_for_dashboard?token=...`) |
| Go integration tests | `synapse/internal/test/dashboard_proxy_test.go` (7 cases) |
| Playwright e2e | `dashboard/tests/dashboard_picker.spec.ts` (4 cases) |
| RFC (this doc) | `docs/CONVEX_DASHBOARD_PICKER_PLAN.md` |

### How Strategy E works

```
┌─ Synapse Dashboard (our fork) ───────────────────────────────────┐
│ ┌─ Overlay header (h-10) ─────────────────────────────────────┐ │
│ │ Team / Project breadcrumb         <DeploymentPicker pill>   │ │
│ ├─────────────────────────────────────────────────────────────┤ │
│ │ ┌─ <iframe> the upstream Convex Dashboard ────────────────┐ │ │
│ │ │ [Logo] [Avatar/Project]                                 │ │ │
│ │ │ Health · Data · Functions · ...                         │ │ │
│ │ │ ...                                                     │ │ │
│ │ └─────────────────────────────────────────────────────────┘ │ │
│ └─────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────┘
```

The picker renders OUR data (fetched directly from the Synapse REST
API), not the iframe's. Switching a deployment routes the parent page
to `/embed/<new-name>`, which re-mounts the iframe with fresh
credentials via the existing postMessage handshake. No upstream
protocol changes; no fork; no rebase tax.

### Trade-offs vs Strategy B (full fork)

| Aspect | Strategy E (shipped) | Strategy B (forked) |
|---|---|---|
| Initial cost | ~6 h | ~1-2 weeks |
| Recurring cost | 0 | ~2-4 h/month rebase |
| Upstream upgrades | Automatic | Manual |
| Switch UX | Full iframe reload | In-place credential swap |
| Visual integration | Two stacked headers | Single header |
| Picker hotkeys | Work outside iframe; pass-through to iframe via the parent | Native to the iframe |

The "two stacked headers" cosmetic is the one real downside. v1 ships
with our header at `h-10` (40px) so it doesn't dominate the iframe.
If operators ask for tighter integration we promote to Strategy B as
documented in §5.

### Phase 2 of Strategy E (still open)

The overlay covers the most-painful case (switching a deployment).
Ideas that didn't make this round but stay viable on Strategy E:

- **Hide the iframe's own header** via CSP-permitted CSS in our shell.
  Today the upstream renders [Logo] + Avatar + ToggleTheme inside the
  iframe; trimming those gives single-header look without forking.
  *(Blocked by cross-origin CSS: a parent page can't style nodes
  inside an iframe whose origin differs. Stays open as a Strategy B
  follow-up — once we control the upstream image we can hide the
  upstream header at the source.)*
- **Sync route between picker and iframe** — when the operator
  navigates `/data` inside the iframe, reflect that in the parent's
  URL (`/embed/<name>/data`). Today the parent URL is just the
  deployment; the iframe's own router holds the page state.
  *(Blocked by cross-origin: parent can't read `iframe.contentWindow.
  location` and the upstream doesn't postMessage navigation events.
  Same Strategy B hook as above.)*
- **Remember last viewed deployment per project** in localStorage so
  reopening from the project page lands on the same deployment.
  Partially shipped in Phase 3 (we now stamp the timestamp; the
  recency badge in the dropdown reads it). The "auto-redirect on
  project page open" piece is still open — it's a UX call, not a
  technical one, so deferring until operators ask.

### Phase 3 polish (shipped 2026-05-02)

Same overlay, more ergonomics. Four UX wins, zero new dependencies:

- **Keyboard navigation in the dropdown** — arrow keys traverse
  items in order (prod → dev → preview → custom), Enter selects,
  Escape closes. Mirrors the muscle memory operators already have
  from `<select>`-style pickers without giving up the cloud-style
  visual layout.
- **"/" hotkey** opens the dropdown and focuses the search input.
  Matches GitHub / Linear conventions; ignores when an editable
  element is already focused so it doesn't fight with form inputs.
- **Search filter** appears at the top of the menu when there are
  6+ deployments. Filters by name, type, and reference (case-
  insensitive). Below the threshold the input is hidden — small
  projects don't pay for ergonomics they don't need.
- **Status indicator dot** on the pill (next to the deployment
  type dot) — running = emerald, provisioning = amber, failed =
  rose, stopped = neutral. Same colours appear on each dropdown
  item, so you can see "prod is provisioning" without expanding.
- **Last-viewed timestamp** ("visited 5m ago") under each dropdown
  item, pulled from a `localStorage[synapse.lastViewedAt.<projectId>.
  <deploymentName>]` key the embed shell stamps on mount. Hidden
  in the first minute so the picker isn't noisy in normal use.

Tests: 5 new Playwright cases on top of the 4 existing picker
specs (status indicator, keyboard nav arrow + Enter, Escape closes,
search filter narrows + empty state, "/" hotkey + focus). 41 → 46.

### Endpoint reservation: `list_deployments_for_dashboard`

The cross-origin endpoint shipped in 37ff428 is unused by the v1
overlay (the picker fetches via the same-origin Synapse API instead).
It stays in the codebase as the seam Strategy B would slot into:
when we eventually fork the dashboard image, the fork's
`?a=<api-url>&d=<name>` flow has a real endpoint to call.

The endpoint cost ~7 Go integration tests + ~150 lines of handler
code; cheap insurance.

## 13. Resolved decisions

The §7 questions, answered with operator-asleep defaults. Each is
revisable in a follow-up PR if the operator disagrees.

| # | Question | Decision | Reasoning |
|---|---|---|---|
| 1 | Scope of dropdown — keep "Other Deployments"? | Skip for v1 | Self-hosted is single-operator most of the time; a section that's empty 90% of the time is noise. Easy to add when multi-operator workflows appear. |
| 2 | Phase 1 (interstitial) acceptable as v1? | No — went straight to overlay | Operator wanted "lindo" — the interstitial doesn't qualify. |
| 3 | Fork hosting | N/A — Strategy E doesn't fork | |
| 4 | Upstream rebase cadence | N/A — Strategy E doesn't rebase | |
| 5 | Custom Domains tab in Settings | Skip for v1 | Not picker-related; tracked separately in ROADMAP under "Phase 3 polish". |

