"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { TopBar } from "@/components/TopBar";
import { UpdateBanner } from "@/components/UpdateBanner";
import { getAccessToken } from "@/lib/auth";

// Auth gate + persistent TopBar for everything under /teams. The TopBar
// derives the active team from the URL itself, so per-team layouts don't
// need to wrap it again.
export default function TeamsLayout({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const [ready, setReady] = useState(false);

  useEffect(() => {
    if (!getAccessToken()) {
      router.replace("/login");
      return;
    }
    setReady(true);
  }, [router]);

  if (!ready) {
    return (
      <div className="flex min-h-screen items-center justify-center text-sm text-neutral-500">
        Loading...
      </div>
    );
  }

  return (
    <div className="min-h-screen">
      <TopBar />
      <UpdateBanner />
      <main className="mx-auto max-w-7xl px-4 py-8 sm:px-6">{children}</main>
    </div>
  );
}
