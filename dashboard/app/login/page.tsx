"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ApiError, api } from "@/lib/api";

export default function LoginPage() {
  const router = useRouter();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setPending(true);
    try {
      await api.login(email, password);
      router.push("/teams");
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
          <label className="block text-xs text-neutral-400">Email</label>
          <Input
            type="email"
            value={email}
            autoComplete="email"
            onChange={(e) => setEmail(e.target.value)}
            required
          />
        </div>
        <div className="space-y-2">
          <label className="block text-xs text-neutral-400">Password</label>
          <Input
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
