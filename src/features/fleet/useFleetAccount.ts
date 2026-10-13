// Unified account signal for device/share views: WEB takes the auth store session, desktop takes the server session
// established by the "account sync" link (same queryKey and semantics as features/auth/desktopAccount.ts,
// sharing the cache; SyncCard's invalidate of ["sync-link"] after login/disconnect auto-refreshes this signal).

import { useQuery } from "@tanstack/react-query";
import { syncApi } from "../../ipc/commands";
import { remoteAccountApi } from "../../ipc/remoteApi";
import { WEB } from "../../ipc/env";
import { useAuth } from "../auth/store";
import type { AccountUser } from "../../ipc/authApi";

export interface FleetAccount {
  // loading is true only during the desktop first fetch (WEB gets it synchronously); a non-empty userId means logged in.
  loading: boolean;
  userId: string | null;
  isAdmin: boolean;
}

// Same semantics as desktopAccount.ts fetchDesktopAccount: the role is probed via adminUsers
// (superadmin-only); a miss falls back to the minimal regular-user view. Semantic changes must be synced on both sides.
async function fetchDesktopIdentity(): Promise<AccountUser | null> {
  const status = await syncApi.status();
  if (!status.loggedIn || !status.username) return null;
  const minimal: AccountUser = {
    id: status.userId ?? "",
    username: status.username,
    display_name: "",
    role: "user",
    state: "active",
    must_change_password: false,
    mfa_enabled: false,
    created_at: 0,
    updated_at: 0,
    last_login_at: 0,
  };
  try {
    const r = await remoteAccountApi.adminUsers();
    const me = r.users.find((u) => u.username === status.username);
    return me ?? { ...minimal, role: "superadmin" };
  } catch {
    return minimal;
  }
}

export function useFleetAccount(): FleetAccount {
  const webUser = useAuth((s) => s.user);
  const query = useQuery({
    queryKey: ["sync-link", "identity"],
    queryFn: fetchDesktopIdentity,
    enabled: !WEB,
    staleTime: 30_000,
  });
  if (WEB) {
    return { loading: false, userId: webUser?.id ?? null, isAdmin: webUser?.role === "superadmin" };
  }
  // A query error (sync engine unavailable etc.) is treated as logged out: the degraded view guides to "account sync" login.
  if (query.data === undefined) return { loading: !query.isError, userId: null, isAdmin: false };
  if (query.data === null) return { loading: false, userId: null, isAdmin: false };
  return { loading: false, userId: query.data.id || null, isAdmin: query.data.role === "superadmin" };
}
