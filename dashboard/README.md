# Synapse dashboard

Minimal management UI for [Synapse](../README.md) — the open-source control
plane for self-hosted Convex deployments. Once you have a deployment, this
dashboard hands you off to the standard self-hosted Convex dashboard for
data/functions/logs.

## Stack

- Next.js 16 + App Router + TypeScript
- Tailwind CSS 4
- SWR for data fetching
- Hand-rolled shadcn-style primitives (button, card, input, dialog, badge)
- JWT in localStorage (no auth library)

## Getting started

```bash
# from this directory
npm install
npm run dev
```

Visit http://localhost:3000.

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
  page.tsx                            # gate -> /teams or /login
  login/  register/                   # auth
  teams/
    layout.tsx                        # auth guard + header
    page.tsx                          # team list + create
    [team]/page.tsx                   # project list + create
    [team]/[project]/page.tsx         # deployment list + create + open
components/ui/                        # button, card, input, dialog, badge
components/Header.tsx
lib/api.ts                            # typed Synapse REST wrapper
lib/auth.ts                           # localStorage JWT helpers
```

## What's stubbed

- No project settings page (rename, env vars, delete). Marked with a TODO in
  `app/teams/[team]/[project]/page.tsx`.
- No team members / invite flow.
- No token refresh; on 401 the user is bounced back to `/login`.
- No optimistic UI — every mutation re-fetches via SWR.
