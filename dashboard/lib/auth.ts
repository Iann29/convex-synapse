// Tiny localStorage-backed JWT store. We deliberately don't use cookies
// (the dashboard is fully client-side and Synapse takes the bearer header).

export type User = {
  id: string;
  email: string;
  name?: string;
};

export type AuthBundle = {
  accessToken: string;
  refreshToken: string;
  user: User;
};

const KEY = "synapse.auth";

export function saveAuth(bundle: AuthBundle): void {
  if (typeof window === "undefined") return;
  window.localStorage.setItem(KEY, JSON.stringify(bundle));
}

export function loadAuth(): AuthBundle | null {
  if (typeof window === "undefined") return null;
  const raw = window.localStorage.getItem(KEY);
  if (!raw) return null;
  try {
    return JSON.parse(raw) as AuthBundle;
  } catch {
    return null;
  }
}

export function clearAuth(): void {
  if (typeof window === "undefined") return;
  window.localStorage.removeItem(KEY);
}

export function getAccessToken(): string | null {
  return loadAuth()?.accessToken ?? null;
}

export function getCurrentUser(): User | null {
  return loadAuth()?.user ?? null;
}
