---
name: aster-smoke
description: Run Aster's end-to-end docker smoke tests, including the v0.6 module-query smoke that proves a real `npx convex deploy` bundle executes inside a v8cell against real Postgres. Use when the user asks to "smoke aster", "test aster end-to-end", "prove aster works", "run aster docker smoke", or wants to verify the Aster integration after pulling new aster-runner changes.
---

# Aster smoke harness

The Aster repo (`/home/ian/aster-runner`) ships three docker smokes. Each one is a single shell script that spins up containers, asserts an expected stdout, and tears everything down on EXIT. They are the operator-visible proof that the Aster ↔ Synapse integration actually executes.

| Script | Tag arg | What it proves |
|---|---|---|
| `docker/smoke.sh` | default `0.4` | brokerd + v8cell over UDS; legacy `Aster.read` + `Convex.asyncSyscall("1.0/get")` paths. Output: `{"output":42,"traps":1}`. |
| `docker/smoke-postgres.sh` | default `0.4` | adds postgres:16; brokerd runs `ASTER_STORE=postgres`, cell does `Convex.asyncSyscall("1.0/get")` against a real Postgres-seeded row. Output: `output:"ian"`. |
| `docker/smoke-bundle.sh` (v0.6) | default `0.4-modulequery` | **the real proof.** Stages a real `npx convex deploy` ZIP at `<modules_dir>/<storage_key>.blob`, runs the v8cell binary with `ASTER_MODULE_PATH=messages.js` + `ASTER_FUNCTION_NAME=getById` + `ASTER_ARGS_JSON=[{"id":"..."}]`. The cell compiles ESM, calls `getById.invokeQuery`, traps once via `Convex.asyncSyscall("1.0/get")`, returns the seeded document. Output: `output:"{\"_id\":\"messages|...\",\"name\":\"ian\"}"`. |

## Run all three

```bash
cd /home/ian/aster-runner

# Build the images first (they're cached after the first run; the V8 build is
# the slow path — ~5 min cold, seconds warm).
docker build --target=runtime-broker -t aster-brokerd:0.4 -f docker/Dockerfile .
docker build --target=runtime-v8cell -t aster-v8cell:0.4 -f docker/Dockerfile .

# Smoke #1 — UDS + V8 cell + memory store. Fast (~5s).
./docker/smoke.sh 0.4

# Smoke #2 — postgres-backed read path. Needs port 5432 free or it'll
# pick a transient one; pulls postgres:16 if absent (~30s first time).
./docker/smoke-postgres.sh 0.4

# Smoke #3 — the v0.6 module-query end-to-end. Same images.
./docker/smoke-bundle.sh 0.4-modulequery
```

If `smoke-bundle.sh` is the goal (most likely when the user says "smoke aster"), it's the one to run; the other two cover prerequisites and rarely fail in isolation.

## What "smoke passed" looks like for each

`smoke.sh` (legacy):

```
==> v8cell stdout: {"capsule_hash":..., "output":42, "traps":1}
OK: aster v0.4 brokerd + v8cell smoke passed
```

`smoke-postgres.sh`:

```
==> v8cell stdout: {"output":"ian","traps":1,...}
OK: aster brokerd(postgres) + v8cell smoke passed
```

`smoke-bundle.sh` (the proof of v0.6):

```
==> staged bundle at /tmp/aster-bundle-smoke-modules.<rand>/test-bundle.blob (14854 bytes, sha256 ef11...)
==> sourcePackageId = r4zexvjnaqqewnanxvq5anfexsana5t4
==> v8cell stdout: {"capsule_hash":...,"output":"{\"_id\":\"messages|...\",\"name\":\"ian\"}","traps":1}
OK: aster brokerd(postgres) + v8cell module-query smoke passed
```

## Diagnose failures

- **`Cannot connect to the Docker daemon`** — start docker first (`sudo systemctl start docker` on Linux; check Docker Desktop on macOS).
- **`port 5432 is already in use`** — another postgres is running. Kill it or change the port in the script.
- **V8 build takes forever** — first cold build downloads `libv8.a` (~80 MB). Subsequent builds reuse the cargo cache.
- **`broker did not log 'ready socket=' within 10s`** in `smoke-bundle.sh` — the brokerd is failing to read from Postgres. `docker logs <smoke-broker-name>` to see the actual error; usually a schema/seed mismatch.
- **`output:null` in `smoke-bundle.sh`** — the broker found NO row at the document id. Either the seed didn't insert the user document, or the cell is computing the wrong IDv6. Check the script's `==> sourcePackageId =` line and grep the `documents` table for `aaaaaaaa...` rows.
- **`EntryNotFound { tried, available }` in cell stderr** — the bundle ZIP is missing `modules/<path>.js`. Re-stage the ZIP; the script does this each run so a stale temp dir is the usual cause.
- **`sha256 mismatch`** — the staged ZIP's SHA-256 doesn't match what was inserted into `_source_packages.sha256`. Almost always caused by editing the script and forgetting that the SHA is computed from the bundle bytes; re-run cleanly.

## Where the Aster ↔ Synapse contract lives

When you're touching code that crosses the boundary (Synapse spawning v8cell containers, the `aster/invoke` endpoint, the `SYNAPSE_ASTER_*` envs, the `provisionAster` Docker layer), run smoke #3 — it's the only one that exercises the binary env-parse, the IPC LoadModuleBundle, the storage adapter, and the V8 ESM compile + invokeQuery in one go.

After landing changes in `/home/ian/aster-runner` that affect the brokerd or v8cell binaries, **rebuild the images first** (the smokes use whatever's tagged locally) and **bump the tag** if you're pinning Synapse to a new image (see `synapse/internal/docker/aster.go::AsterImageTag`).
