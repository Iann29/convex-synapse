# DNS auto-config smoke procedure

This doc covers the manual smoke test for the Cloudflare DNS auto-
configuration feature shipped in three PRs:

- **PR #86 — backend**: schema (`dns_credentials`), Cloudflare client,
  endpoints under `/v1/admin/dns_credentials/*`, NS-detect at
  `/v1/internal/dns_provider`, and
  `POST /v1/deployments/{name}/domains/{id}/auto_configure`.
- **PR #85 — dashboard**: NS-detect on input, `/admin/dns-credentials`
  CRUD page, auto-configure-on-submit flow.
- **PR #N — verification loop** (this PR): background `dns.Verifier`
  flips auto-configured rows from `pending` to `active` once the A
  record propagates globally. ~15 s tick, 5 min deadline.

The Go integration suite (`go test ./...`) covers the verifier with
stubbed resolvers + a stubbed Cloudflare API. None of those prove:

- The verifier resolves real A records via the host's DNS resolver.
- Caddy issues a real Let's Encrypt certificate via on-demand TLS.
- The dashboard's auto-configure UI sends the right request shape
  against a real Convex deployment.
- Cloudflare actually serves the record we minted.

For changes that touch **any** part of the auto-config pipeline
(`internal/dns/*.go`, `internal/api/dns_credentials.go`,
`internal/api/domains.go`, `dashboard/app/admin/dns-credentials/*`)
run this procedure on `synapse-vps` before declaring the change done.

## Prerequisites (one-time)

1. A real Cloudflare zone you control (e.g. `mytest.dev`). The
   procedure registers a subdomain under it; the apex is left
   untouched.
2. A Cloudflare API **token** (not the account-wide global key) scoped
   to `Zone:DNS:Edit` for that zone. Generate at
   <https://dash.cloudflare.com/profile/api-tokens> via the "Edit zone
   DNS" template.
3. A Synapse VPS — `synapse-vps` (Hetzner CPX22) is the canonical
   target. Real public IPv4 + the wildcard A record `*.<your-zone>`
   pointing at it. Same DNS plumbing as the regular wildcard-domain
   smoke; see `installer/install/preflight.sh::check_base_domain`.
4. `SYNAPSE_PUBLIC_IP=<vps-ip>` set in the install — the verifier
   bails at boot when this is empty.
5. `SYNAPSE_STORAGE_KEY` set in the install — needed to encrypt the
   stored Cloudflare token. See `docs/HA_TESTING.md` for how to
   generate one. The auto-configure endpoint returns
   503 `dns_auto_configure_unavailable` without it.

## Procedure

```text
1. ssh synapse-vps
   (use the password ian123 if openssh asks; key in /.vps/credentials.md)

2. setup.sh --upgrade
   This pulls the latest main + rebuilds. Watch for the `dns
   verifier starting` log line in `docker compose logs synapse`:
       INFO dns verifier starting interval=15s max_age=5m0s
            expected_ip=<vps-ip>
   If you see `INFO dns verifier: SYNAPSE_PUBLIC_IP unset` instead,
   the install never wrote SYNAPSE_PUBLIC_IP to the .env — fix that
   first or the verifier is a no-op.

3. Open the dashboard in a browser, log in as the instance admin.

4. Navigate to /admin/dns-credentials.
   - Click "Add credential".
   - Provider: Cloudflare.
   - Token: paste the API token from step 2 of the prereqs.
   - Label: "smoke <date>".
   - Submit. The page should refresh with a row showing the zones
     the token covers (cached at save time; refresh = re-add).

5. Open any project, then a deployment. Click the "Custom domains"
   panel.
   - Add a new domain. Use a fresh subdomain like
     "smoke<n>.<your-cloudflare-zone>" so we don't collide with prior
     runs. Role: api.
   - As you type, the dashboard hits /v1/internal/dns_provider and
     shows "Detected: cloudflare" once the apex's NS records resolve
     to *.cloudflare.com. The "auto-configure" toggle should appear
     enabled.
   - Submit. The row appears in the panel with "auto-configured"
     chip and status="pending".

6. Watch the row's status. It should flip to "active" within ~30
   seconds (the verifier ticks every 15s; Cloudflare propagates
   newly-added records globally in a handful of seconds usually).
   Refresh the panel manually OR rely on the dashboard's polling
   (whatever PR #85 ships).

7. Visit https://smoke<n>.<your-cloudflare-zone> in a browser. Caddy
   should issue a Let's Encrypt cert via on-demand TLS (the host
   matches the synapse-api /v1/internal/tls_ask gate because the row
   is now `active`). The deployment's API should respond — same
   payload you'd see at https://<vps>/d/<deployment>/.

8. Trigger the failure path: add a second domain pointing at a zone
   the credential does NOT cover. The dashboard should reject with
   "no credential for zone" before any DB write. Add a third domain
   under a covered zone but DELETE the Cloudflare record manually
   between auto-configure and verification — the row should sit at
   "pending" with last_dns_error="expected <ip>, got <other>" then
   flip to "failed" after 5 minutes.

9. Cleanup:
   - Delete the smoke domain from the dashboard. The DELETE handler
     calls Cloudflare's API to remove the A record (best-effort).
     Verify in the Cloudflare dashboard that the A record is gone.
   - Delete the smoke credential from /admin/dns-credentials. (If
     any deployment still references it, you'll get 409
     credential_in_use — clean up the domains first.)
   - Optional: rotate the Cloudflare API token. The token is stored
     encrypted at rest with SYNAPSE_STORAGE_KEY; rotating in
     Cloudflare invalidates the saved copy on Synapse without
     touching the DB.
```

## What "good" looks like in the logs

```text
# Verifier successfully flips a row:
INFO dns verifier: domain active
     domain=smoke1.mytest.dev
     deployment_id=<uuid>
     deployment_name=<name>

# Verifier observes a not-yet-propagated row (no flip, retries next tick):
DEBUG dns verifier: lookup error
      domain=smoke1.mytest.dev
      err="lookup smoke1.mytest.dev: no such host"

# 5-minute deadline fires:
WARN dns verifier: domain failed propagation deadline
     domain=smoke1.mytest.dev
     reason="DNS did not propagate within 5m0s"

# Multi-node skip (only ever shows when running ≥2 synapse processes
# against the same DB):
DEBUG dns verifier: another node holds the sweep lock; skipping tick
```

## What NOT to ship to CI

- **Real Cloudflare account / token**. There's no headless-Cloudflare
  fixture; CI uses a stub HTTP server (see
  `internal/test/dns_credentials_test.go::newCloudflareStub`).
- **Real public DNS resolution**. Tests inject a stub via
  `dns.Verifier.Resolver`; CI never hits 8.8.8.8.
- **Real Let's Encrypt issuance**. ACME staging would be possible but
  the verifier doesn't drive ACME — that's Caddy's
  `on_demand_tls` config gated by `/v1/internal/tls_ask`.

If you find yourself wanting to automate any of the above in CI, add
a `SYNAPSE_DNS_E2E=1` gate first (mirroring `SYNAPSE_HA_E2E=1`) so the
default `go test ./...` stays hermetic.
