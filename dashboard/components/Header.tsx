"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { clearAuth, getCurrentUser, type User } from "@/lib/auth";

export function Header() {
  const router = useRouter();
  const [user, setUser] = useState<User | null>(null);

  useEffect(() => {
    setUser(getCurrentUser());
  }, []);

  const logout = () => {
    clearAuth();
    router.push("/login");
  };

  return (
    <header className="sticky top-0 z-30 border-b border-neutral-800 bg-neutral-950/80 backdrop-blur">
      <div className="mx-auto flex h-12 max-w-6xl items-center justify-between px-4">
        <Link
          href="/teams"
          className="text-sm font-semibold tracking-tight text-neutral-100"
        >
          Synapse
        </Link>
        {user && (
          <div className="flex items-center gap-3 text-xs text-neutral-400">
            <span>{user.email}</span>
            <Button variant="ghost" size="sm" onClick={logout}>
              Logout
            </Button>
          </div>
        )}
      </div>
    </header>
  );
}
