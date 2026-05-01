# Screenshots

Visual tour of the Synapse dashboard. The README points here so the
top-level page stays lean — every image lives in a single section so
contributors can scroll once and see the entire UI surface.

Captures are taken against the standard `./setup.sh` install. They
go stale every time the dashboard fork's design changes (chunks 4 +
9 of v0.6.0 most recently); refresh by running the install on a
fresh VPS and re-screenshotting whichever pages changed.

## Sign in

The dashboard runs on port 6790. Register a user with email + password.

![Login page](screenshots/01-login.png)

## Teams

Multi-tenant by team: each team owns projects, members, and deployments.

![Teams listing](screenshots/02-teams.png)

## Team home — projects + deployments

Top app bar with team picker, breadcrumb, Projects / Deployments tabs,
and inline invites at the bottom.

![Team home](screenshots/03-team-home.png)

## Project page — deployments

A real provisioned Convex backend per row. The card shows
type / status / default flags plus the URL, with one-click
**Open dashboard** (the standalone Convex Dashboard, embedded
under `/embed/<name>` and auto-logged via the postMessage
handshake) and a CLI-credentials panel below.

![Project deployments](screenshots/04-project-deployment.png)

## Create deployment — single-replica or HA

Tick "High availability (2 replicas + Postgres + S3)" to opt into the
HA path; the hint explains the cluster envs you need set. Without HA
enabled at the Synapse-process level, the request comes back with a
friendly inline error.

![Create deployment with HA toggle](screenshots/05-create-ha-dialog.png)

## CLI credentials — `npx convex` works out of the box

The panel emits the exact `export` lines the Convex CLI looks for
(`CONVEX_SELF_HOSTED_URL` + `CONVEX_SELF_HOSTED_ADMIN_KEY`). Paste into
a shell and `npx convex dev` talks straight to the Synapse-managed
backend.

![CLI credentials panel](screenshots/06-cli-credentials.png)

## Audit log

Every mutating action logs an event — admin-only read.

![Audit log](screenshots/07-audit-log.png)

## Team Settings

Three panes — General, Members, Access Tokens. Synapse is open-source
self-hosted; Cloud-only billing / usage / referral panes are
deliberately omitted (out of scope per
[`docs/ARCHITECTURE.md`](ARCHITECTURE.md)).

![Settings General](screenshots/08-settings-general.png)
![Settings Access Tokens](screenshots/09-settings-tokens.png)

## Account

User-scoped personal access tokens for CLI / CI.

![Account page](screenshots/10-me-account.png)
