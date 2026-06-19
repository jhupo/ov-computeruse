import type { AuthSession, DashLoginResponse, DashPrincipal } from "./types";

const STORAGE_KEY = "ov.dash.session";

export interface DashSession extends AuthSession {
  token: string;
  expires_at: string;
  principal: DashPrincipal;
}

export function getStoredAuth(): DashSession | null {
  return readDashSession();
}

export function setStoredAuth(session: AuthSession): DashSession {
  if (typeof window !== "undefined") {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(session));
  }
  return session;
}

export function clearStoredAuth(): void {
  clearDashSession();
}

export function readDashSession(): DashSession | null {
  if (typeof window === "undefined") {
    return null;
  }

  const raw = window.localStorage.getItem(STORAGE_KEY);
  if (!raw) {
    return null;
  }

  try {
    const session = JSON.parse(raw) as DashSession;
    if (!session.token || isSessionExpired(session)) {
      clearDashSession();
      return null;
    }
    return session;
  } catch {
    clearDashSession();
    return null;
  }
}

export function saveDashSession(response: DashLoginResponse): DashSession {
  const session: DashSession = {
    token: response.token,
    expires_at: response.expires_at,
    principal: response.principal,
  };

  return setStoredAuth(session);
}

export function clearDashSession(): void {
  if (typeof window !== "undefined") {
    window.localStorage.removeItem(STORAGE_KEY);
  }
}

export function getDashToken(): string | null {
  return readDashSession()?.token ?? null;
}

export function isSessionExpired(session: Pick<DashSession, "expires_at">): boolean {
  const expiresAt = Date.parse(session.expires_at);
  return Number.isFinite(expiresAt) && expiresAt <= Date.now();
}
