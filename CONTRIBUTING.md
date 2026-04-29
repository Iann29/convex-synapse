# Contributing

Thanks for thinking about contributing! Synapse is in early development; the
fastest way to help right now is to try running it, file issues for what
breaks, and discuss design choices in the issue tracker before coding large
pieces.

## Quick dev loop

```bash
git clone git@github.com:Iann29/convex-synapse.git
cd convex-synapse
cp .env.example .env

# Start postgres
docker compose up -d postgres

# Run synapse
cd synapse
go run ./cmd/server
# → http://localhost:8080
```

## Making changes

1. Fork → branch → PR. Use a descriptive branch name (`feat/deploy-keys`,
   `fix/team-invite-email-validation`).
2. Match the existing commit-message style: `feat(synapse/<resource>): …`.
3. Build and vet must pass: `cd synapse && go build ./... && go vet ./...`.
4. Smoke-test the change end-to-end. Paste the curl flow in the PR description.
5. Update `docs/ROADMAP.md` if you advanced or reshuffled a phase.

## What we'd love help with

See `docs/ROADMAP.md`. Right now (v0.1) the high-impact items are:

- Dashboard fork: getting the Convex Cloud dashboard running against Synapse
- A real test suite (Go + Postgres testcontainers)
- Reverse-proxy mode so deployment URLs don't need exposed host ports
- `npx convex` CLI compatibility (token format, /authorize endpoint)

## Code of Conduct

Be kind. We follow the
[Contributor Covenant 2.1](https://www.contributor-covenant.org/version/2/1/code_of_conduct/).
