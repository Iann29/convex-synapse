# @iann29/synapse

Thin CLI wrapper for using the official Convex CLI with Synapse-managed
self-hosted deployments.

The package name is scoped because `synapse` is already taken on npm, but the
installed binary is still named `synapse`.

## Install

For one machine:

```bash
npm install -g @iann29/synapse
synapse --help
```

For one app/project:

```bash
npm install -D @iann29/synapse
npx synapse --help
```

Without installing into the project first:

```bash
npm exec --package @iann29/synapse -- synapse --help
```

Until the package is published to npm, install from a release tarball:

```bash
npm install -D https://github.com/Iann29/convex-synapse/releases/download/v1.6.2/iann29-synapse-1.6.2.tgz
npx synapse --help
```

For local development from this repository:

```bash
cd /path/to/convex-synapse/cli
npm link

cd /path/to/your-convex-app
synapse --help
```

Or install this checkout into a single app without a global link:

```bash
cd /path/to/your-convex-app
npm install -D /path/to/convex-synapse/cli
npx synapse --help
```

## Usage

```bash
synapse login https://synapse.example.com
synapse select
synapse convex dev --once
synapse convex deploy
```

`synapse select` stores non-secret project metadata in
`.synapse/project.json`. It also writes `.env.local` with the selected dev
deployment credentials for compatibility with direct Convex CLI use.

`synapse convex ...` is the safer project-aware path:

- `synapse convex dev` uses the linked dev deployment.
- `synapse convex deploy` uses the linked prod deployment.
- `synapse convex --target dev deploy` forces the dev target.
- Other Convex commands default to dev unless `--target prod` is passed.

At runtime the wrapper fetches fresh deployment credentials from Synapse,
sets `CONVEX_SELF_HOSTED_URL` and `CONVEX_SELF_HOSTED_ADMIN_KEY`, removes
`CONVEX_DEPLOYMENT`, and delegates to the official `npx convex ...` command.
