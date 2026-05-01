"use client";

import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ApiError, api } from "@/lib/api";

// First-run wizard. Reachable only when /v1/install_status reports
// firstRun=true (the users table is empty). Once the admin is
// created, install_status flips and any second visit redirects to
// /login. The wizard composes three existing flows:
//
//   1. Create admin user      -> POST /v1/auth/register
//   2. Bootstrap a team       -> POST /v1/teams/create_team ("Default")
//   3. Bootstrap a project    -> POST /v1/projects/create   ("demo")
//   4. Provision a deployment -> POST /v1/deployments/create (dev tier)
//
// then drops the operator on the project page with the CLI snippet
// already visible. Skipping any step beyond #1 is allowed (advanced
// operators may want to wire things up by hand) — the "Skip demo"
// button on step 2 routes straight to /teams.
//
// The whole flow is idempotent at the API level: re-running after a
// partial wizard either picks up where it left off or simply creates
// "Default-2" / "demo-2" — never a 500.

type Phase =
  | { kind: "loading" }
  | { kind: "admin" }
  | { kind: "demo" }
  | { kind: "provisioning"; teamSlug: string; projectSlug: string }
  | { kind: "done"; teamSlug: string; projectSlug: string }
  | { kind: "redirect" };

export default function SetupPage() {
  const router = useRouter();
  const [phase, setPhase] = useState<Phase>({ kind: "loading" });
  const [version, setVersion] = useState<string>("");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  // Admin form state (step 1).
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [name, setName] = useState("");

  // Demo form state (step 2). Defaults are sane for the most common
  // path; operators who care can rename later.
  const [teamName, setTeamName] = useState("Default");
  const [projectName, setProjectName] = useState("demo");

  // Probe install_status on mount. firstRun=false means someone
  // already created an admin — bounce them to /login instead of
  // letting them duplicate. Probe failure also bounces (better than
  // showing a wizard that may not work).
  useEffect(() => {
    let cancelled = false;
    api
      .installStatus()
      .then((s) => {
        if (cancelled) return;
        setVersion(s.version);
        if (s.firstRun) {
          setPhase({ kind: "admin" });
        } else {
          setPhase({ kind: "redirect" });
          router.replace("/login");
        }
      })
      .catch(() => {
        if (!cancelled) {
          setPhase({ kind: "redirect" });
          router.replace("/login");
        }
      });
    return () => {
      cancelled = true;
    };
  }, [router]);

  const submitAdmin = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setPending(true);
    try {
      await api.register(email, password, name || undefined);
      setPhase({ kind: "demo" });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not create admin");
    } finally {
      setPending(false);
    }
  };

  const submitDemo = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setPending(true);
    try {
      const team = await api.teams.create(teamName);
      const created = await api.teams.createProject(team.slug, projectName);
      setPhase({
        kind: "provisioning",
        teamSlug: team.slug,
        projectSlug: created.projectSlug,
      });
      // Provision a dev deployment. The operator lands on the project
      // page with this row already showing — no "click to create" step.
      await api.projects.createDeployment(created.projectId, { type: "dev", ha: false });
      setPhase({
        kind: "done",
        teamSlug: team.slug,
        projectSlug: created.projectSlug,
      });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not bootstrap demo");
      setPhase({ kind: "demo" });
    } finally {
      setPending(false);
    }
  };

  const skipDemo = () => {
    router.push("/teams");
  };

  const finish = () => {
    if (phase.kind === "done") {
      router.push(`/teams/${phase.teamSlug}/${phase.projectSlug}`);
    }
  };

  if (phase.kind === "loading" || phase.kind === "redirect") {
    return (
      <main className="flex min-h-screen items-center justify-center px-4">
        <div className="text-sm text-neutral-400">Loading...</div>
      </main>
    );
  }

  return (
    <main className="flex min-h-screen items-center justify-center px-4">
      <div className="w-full max-w-md space-y-6 rounded-lg border border-neutral-800 bg-neutral-900/40 p-6">
        <div>
          <h1 className="text-lg font-semibold">Welcome to Synapse</h1>
          <p className="mt-1 text-xs text-neutral-400">
            First-run setup{version ? ` · v${version}` : ""}.
          </p>
          <ProgressDots phase={phase.kind} />
        </div>

        {phase.kind === "admin" && (
          <form id="setup-admin-form" onSubmit={submitAdmin} className="space-y-4">
            <div>
              <h2 className="text-sm font-medium">Step 1 — Create the admin user</h2>
              <p className="mt-1 text-xs text-neutral-400">
                This account becomes the team owner. You can invite more members later.
              </p>
            </div>
            <Field id="setup-name" label="Name (optional)" type="text" autoComplete="name"
              value={name} onChange={setName} />
            <Field id="setup-email" label="Email" type="email" autoComplete="email" required
              value={email} onChange={setEmail} />
            <Field id="setup-password" label="Password" type="password" autoComplete="new-password" required
              value={password} onChange={setPassword} />
            {error && (
              <p className="text-xs text-red-400" role="alert">{error}</p>
            )}
            <Button id="setup-admin-submit" type="submit" disabled={pending} className="w-full">
              {pending ? "Creating admin..." : "Create admin and continue"}
            </Button>
          </form>
        )}

        {phase.kind === "demo" && (
          <form id="setup-demo-form" onSubmit={submitDemo} className="space-y-4">
            <div>
              <h2 className="text-sm font-medium">Step 2 — Bootstrap a demo project</h2>
              <p className="mt-1 text-xs text-neutral-400">
                We&apos;ll create a team, a project, and a dev deployment so you can run
                <code className="mx-1 rounded bg-neutral-800 px-1 py-0.5 text-[11px]">npx convex deploy</code>
                immediately. Rename anything later from settings.
              </p>
            </div>
            <Field id="setup-team-name" label="Team name" type="text" required
              value={teamName} onChange={setTeamName} />
            <Field id="setup-project-name" label="Project name" type="text" required
              value={projectName} onChange={setProjectName} />
            {error && (
              <p className="text-xs text-red-400" role="alert">{error}</p>
            )}
            <div className="flex items-center gap-2">
              <Button id="setup-demo-submit" type="submit" disabled={pending} className="flex-1">
                {pending ? "Provisioning..." : "Create demo project"}
              </Button>
              <Button
                id="setup-demo-skip"
                type="button"
                onClick={skipDemo}
                disabled={pending}
                variant="ghost"
                className="flex-1"
              >
                Skip
              </Button>
            </div>
          </form>
        )}

        {phase.kind === "provisioning" && (
          <div className="space-y-3">
            <h2 className="text-sm font-medium">Provisioning a Convex backend...</h2>
            <p className="text-xs text-neutral-400">
              This usually takes about a second on a warm host (the Convex backend image
              is pre-pulled by the installer).
            </p>
          </div>
        )}

        {phase.kind === "done" && (
          <div className="space-y-3">
            <h2 className="text-sm font-medium">All set!</h2>
            <p className="text-xs text-neutral-400">
              Your demo deployment is ready. The next page shows the
              <code className="mx-1 rounded bg-neutral-800 px-1 py-0.5 text-[11px]">CONVEX_SELF_HOSTED_*</code>
              snippet you can paste into a shell to run
              <code className="mx-1 rounded bg-neutral-800 px-1 py-0.5 text-[11px]">npx convex deploy</code>.
            </p>
            <Button id="setup-finish" onClick={finish} className="w-full">
              Open the project
            </Button>
          </div>
        )}
      </div>
    </main>
  );
}

function ProgressDots({ phase }: { phase: Phase["kind"] }) {
  const order: Phase["kind"][] = ["admin", "demo", "provisioning", "done"];
  const current = order.indexOf(phase);
  return (
    <div className="mt-3 flex items-center gap-2">
      {order.map((p, i) => (
        <span
          key={p}
          className={`h-1.5 w-8 rounded-full transition-colors ${
            i <= current ? "bg-neutral-200" : "bg-neutral-800"
          }`}
        />
      ))}
    </div>
  );
}

function Field({
  id, label, type, autoComplete, required, value, onChange,
}: {
  id: string;
  label: string;
  type: string;
  autoComplete?: string;
  required?: boolean;
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <div className="space-y-2">
      <label htmlFor={id} className="block text-xs text-neutral-400">
        {label}
      </label>
      <Input
        id={id}
        type={type}
        autoComplete={autoComplete}
        required={required}
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
    </div>
  );
}
