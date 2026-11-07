// Unified account signal for device/share views: WEB takes the auth store session, desktop takes the server session
// established by the "account sync" link (same queryKey and fetcher as features/auth/desktopAccount.ts,
// sharing the cache; SyncCard's invalidate of ["sync-link"] after login/disconnect auto-refreshes this signal).

import { useQuery } from "@tanstack/react-query";
import { WEB } from "../../ipc/env";
import { useAuth } from "../auth/store";
import { fetchDesktopAccount } from "../auth/desktopAccount";

export interface FleetAccount {
  // loading is true only during the desktop first fetch (WEB gets it synchronously); a non-empty userId means logged in.
  loading: boolean;
  userId: string | null;
  isAdmin: boolean;
}

export function useFleetAccount(): FleetAccount {
  const webUser = useAuth((s) => s.user);
  const query = useQuery({
    queryKey: ["sync-link", "identity"],
    queryFn: fetchDesktopAccount,
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
