# Design notes

Reference target: the Convex Cloud dashboard at
`https://dashboard.convex.dev` (signed in). Screenshots captured by the
operator are kept locally — not committed since they include personal
info — but the patterns below are what we're matching.

## Visual language

- **Theme**: dark, near-black background (`#0a0a0a`-ish), card surfaces
  one or two shades lighter (`#161616` / `#1a1a1a`), subtle 1px borders
  in `neutral-800`.
- **Accent**: indigo-ish primary for CTAs (Convex uses purple/blue;
  we'll match-not-clone — slight hue shift to avoid trademark
  ambiguity).
- **Typography**: `Inter`, sizes from `text-xs` to `text-xl`. Headings
  `font-semibold`. Body neutral-300 on dark.
- **Avatars**: gradient discs with two-letter initials. Team avatars
  use a deterministic seed off the slug; user avatars off email.
- **Logo**: ours doesn't exist yet — the redesign should pick a small
  geometric mark (Synapse → connected nodes) and place it top-left.

## Layout

### Top app bar
- Logo · Team picker (avatar + name + dropdown) · spacer · primary tabs
  ("Home", "Team Settings") · "Ask AI" / "Support" links · profile
  avatar with menu.
- Persists across all pages once the user is in a team context.
- Click on team picker opens a dropdown listing all teams the user
  belongs to + "Create new team".

### Home page
- Optional banner card at top (referral / promo / nothing for v0).
- Tabs: "Projects" / "Deployments".
  - **Projects** view: search bar + grid/list toggle on the left,
    "Create Project" / "Start Tutorial" on the right. Cards in a
    responsive grid, each card shows project name, slug, last activity,
    deployment count.
  - **Deployments** view: flat list across all projects in the team.
- Empty state: large heading, supporting text, primary CTA centered.

### Team Settings
- Two-column layout: left sidebar nav (General, Members, Billing,
  Usage, Referrals, Access Tokens, Applications, Audit Log [PRO badge],
  Single Sign-On), right pane main content.
- Active item highlighted with a subtle pill.
- Each setting page is a stack of card-shaped sections ("Edit Team",
  "Delete Team", etc.).
- Delete-zone style: red border + red button + safety text.

### Form patterns
- Field label `text-xs text-neutral-400`, input full-width with small
  fill contrast.
- Region/option cards: clickable card with radio in the corner,
  selected state uses accent border + filled radio.
- Save button: disabled until form dirty; accent color when active.

## What we have today

The current dashboard (commits up to v0.2) is functional but visually
minimal. It uses the right primitives (Card, Button, Input, Dialog,
Skeleton, Badge) but not the right composition. The skeleton, copy
button, and refresh polling are already in place — those don't need to
change.

The redesign is mostly **layout + nav + new pages**, not new components.
We can probably keep ~80% of `components/ui/*`.

## Scope for the redesign PR

Tier 1 — core look
- Top app bar + persistent layout for authed routes
- Refreshed home (Projects / Deployments tabs, grid+list toggle)
- Team Settings shell with sidebar nav

Tier 2 — settings content
- General (rename / slug copy / delete-team danger zone)
- Members (move existing team-members listing + invite flow)
- Access Tokens (move /me tokens here, since they're team-scoped in
  Cloud — ours are user-scoped today; v0.3 may rescope or keep both)

Tier 3 — polish
- Avatar component with deterministic gradient
- Logo design + favicon
- Empty states with illustrations or geometric marks

Out of scope for the redesign PR (separate roadmap items):
- Billing / Usage / Referrals / Applications / Audit Log / SSO pages
- Tutorial flow
- Multi-region UI (we're single-host for v0)

## How to tackle it

1. New branch `feat/ui-redesign`.
2. Frontend-specialised agent — pass it the screenshots + this doc +
   `dashboard/components/ui/*` + the existing pages so it knows what
   primitives are already there.
3. Land Tier 1 first, run the existing Playwright suite (selectors must
   keep working — labels with `htmlFor`, dialog roles, button names).
4. Tier 2 in a follow-up commit on the same branch.
5. PR description: before/after screenshots, highlight the test count
   (must equal main).
