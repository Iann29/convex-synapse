# Synapse dashboard

Management UI for [Synapse](../README.md) — the open-source control plane for
self-hosted Convex deployments. Once you have a deployment, this dashboard
hands you off to the standard self-hosted Convex dashboard for data, functions,
and logs.

## Stack

- Next.js 16 + App Router + TypeScript
- Tailwind CSS 4
- SWR for data fetching (with `refreshInterval` polling while a deployment is
  mid-provisioning)
- Hand-rolled shadcn-style primitives (button, card, input, dialog, badge,
  skeleton, avatar)
- JWT in localStorage (no auth library)

## Getting started

```bash
# from this directory
npm install
npm run dev
```

Visit http://localhost:3000.

For the full stack (Synapse API + postgres + dashboard) use the repo-root
`docker compose up -d` instead.

### Environment

| Variable | Default | What it does |
|---|---|---|
| `NEXT_PUBLIC_SYNAPSE_URL` | `http://localhost:8080` | Synapse backend base URL |
| `NEXT_PUBLIC_CONVEX_DASHBOARD_URL` | `http://localhost:6791` | Self-hosted Convex dashboard URL (used for the "Open dashboard" button) |

Set them in `.env.local`. They must be `NEXT_PUBLIC_*` because the API client
runs in the browser.

## Layout

```
app/
  page.tsx                              # gate -> /teams or /login
  login/  register/                     # auth
  accept-invite/                        # invite-token landing page
  me/                                   # account + personal access tokens
  teams/
    layout.tsx                          # auth guard + header
    page.tsx                            # team list + create
    [team]/page.tsx                     # team home: projects + invites
    [team]/[project]/page.tsx           # deployments + env vars + CLI credentials
components/
  ui/                                   # button, card, input, dialog, badge,
                                        # skeleton, avatar, logo
  Header.tsx
  EnvVarsPanel.tsx
  InvitesPanel.tsx
  TokensPanel.tsx
  CliCredentialsPanel.tsx
lib/
  api.ts                                # typed Synapse REST wrapper
  auth.ts                               # localStorage JWT helpers
tests/                                  # Playwright e2e (12 tests, ~30s)
```

## Tests

```bash
# bring up the stack
( cd .. && docker compose up -d )

# run the suite
npx playwright install chromium     # first time only
npm run test:e2e
```

The suite covers:
- register / login / wrong password / anonymous redirect
- team create + project create + rename + delete
- env vars: add / list / delete batch
- invites: issue + accept across two browser contexts
- deployment: provision (real Convex container) + copy URL + delete
- personal access tokens: create / show once / delete

## What's there

- ✅ Email/password auth, JWT, refresh
- ✅ Team CRUD, member listing, invite + accept flow
- ✅ Project CRUD, rename, delete (cascades to deployments)
- ✅ Deployment provisioning (async, polls until running)
- ✅ Deployment delete (idempotent during provisioning)
- ✅ Default env vars per project (set / delete batch)
- ✅ Personal access tokens (`/me`)
- ✅ "Open dashboard" hand-off to standalone Convex dashboard
- ✅ "Copy URL" + "CLI credentials" inline on each deployment

## What's stubbed

- Token refresh isn't auto-triggered; on 401 the user is bounced to `/login`
- No optimistic UI — every mutation re-fetches via SWR
- Pagination is missing on team / project lists (PAT list is paginated server-side)
- Settings pages for billing / usage / SSO / audit log are out of scope (see
  `docs/ROADMAP.md`)

A full UI redesign matching the Convex Cloud aesthetic is in flight on the
`feat/ui-redesign` branch (PR #1) — see [docs/DESIGN.md](../docs/DESIGN.md).
