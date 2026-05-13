"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ApiError, api } from "@/lib/api";

// safeReturnTo guards against open-redirect attacks: only accept
// absolute paths on this origin (start with `/`, not `//`, no
// scheme prefix). Anything else falls back to /teams. Matches the
// pattern used by lib/api.ts's 401 handler.
function safeReturnTo(raw: string | null): string {
  if (!raw) return "/teams";
  if (!raw.startsWith("/") || raw.startsWith("//")) return "/teams";
  if (raw.startsWith("/login")) return "/teams";
  return raw;
}

// useSearchParams() bails the static-prerender (Next.js 16 requires
// a Suspense boundary around any client component that calls it).
// Page entry wraps the form in <Suspense> so the build can statically
// generate /login while the searchParams resolve client-side. The
// fallback is invisible — we just need to satisfy the Suspense
// requirement; the form mounts a tick later under the same layout.
export default function LoginPage() {
  return (
    <Suspense fallback={null}>
      <LoginForm />
    </Suspense>
  );
}

function LoginForm() {
  const router = useRouter();
  const search = useSearchParams();
  const returnTo = safeReturnTo(search.get("return_to"));
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  // First-run redirect: when no admin exists yet, push the operator
  // through /setup instead of asking them to log in to nothing. The
  // probe is unauthenticated; failures (network, DB hiccup) are
  // ignored so the login form remains accessible as a fallback.
  useEffect(() => {
    let cancelled = false;
    api
      .installStatus()
      .then((s) => {
        if (!cancelled && s.firstRun) {
          router.replace("/setup");
        }
      })
      .catch(() => {
        /* fall through to the normal login form */
      });
    return () => {
      cancelled = true;
    };
  }, [router]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setPending(true);
    try {
      await api.login(email, password);
      // v1.6.12+: honour ?return_to so post-login routes to wherever
      // the operator was trying to go (e.g. /embed/<bound> after a
      // custom dashboard domain bounced them through here).
      router.push(returnTo);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Login failed");
    } finally {
      setPending(false);
    }
  };

  return (
    <main className="flex min-h-screen items-center justify-center px-4">
      <form
        onSubmit={submit}
        className="w-full max-w-sm space-y-4 rounded-lg border border-neutral-800 bg-neutral-900/40 p-6"
      >
        <div>
          <h1 className="text-lg font-semibold">Sign in to Synapse</h1>
          <p className="mt-1 text-xs text-neutral-400">
            Self-hosted control plane for Convex.
          </p>
        </div>
        <div className="space-y-2">
          <label htmlFor="login-email" className="block text-xs text-neutral-400">
            Email
          </label>
          <Input
            id="login-email"
            type="email"
            value={email}
            autoComplete="email"
            onChange={(e) => setEmail(e.target.value)}
            required
          />
        </div>
        <div className="space-y-2">
          <label htmlFor="login-password" className="block text-xs text-neutral-400">
            Password
          </label>
          <Input
            id="login-password"
            type="password"
            value={password}
            autoComplete="current-password"
            onChange={(e) => setPassword(e.target.value)}
            required
          />
        </div>
        {error && (
          <p className="text-xs text-red-400" role="alert">
            {error}
          </p>
        )}
        <Button type="submit" disabled={pending} className="w-full">
          {pending ? "Signing in..." : "Sign in"}
        </Button>
        <p className="text-center text-xs text-neutral-500">
          No account?{" "}
          <Link href="/register" className="text-neutral-200 hover:underline">
            Create one
          </Link>
        </p>
      </form>
    </main>
  );
}
