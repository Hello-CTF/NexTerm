// Desktop account identity: the identity source is the server session established by the "account sync" link (SyncStatus carries
// loggedIn/username/userId). The identity is fetched as the full user view via remote_account_me;
// when that endpoint is unavailable, fall back to an adminUsers probe (superadmin only); a miss (403) yields the minimal regular-user view.
// The queryKey hangs under the ["sync-link"] prefix, so SyncCard invalidate after login/disconnect auto-refreshes the identity.

import { useQuery } from "@tanstack/react-query";
import { syncApi } from "../../ipc/commands";
import { remoteAccountApi } from "../../ipc/remoteApi";
import { DESKTOP } from "../../ipc/env";
import type { AccountUser } from "../../ipc/authApi";

async function fetchDesktopAccount(): Promise<AccountUser | null> {
	const status = await syncApi.status();
	if (!status.loggedIn || !status.username) return null;
	try {
		const me = await remoteAccountApi.me();
		if (me.user.username === status.username) return me.user;
	} catch {
		// Fall back to the probe when me is unavailable
	}
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
		// Not a superadmin (admin endpoint denied) or the session just expired: return the minimal regular-user view
		return minimal;
	}
}

export interface DesktopAccountIdentity {
	user: AccountUser | null;
	status: "resolving" | "resolved";
}

export function useDesktopAccountIdentity(): DesktopAccountIdentity {
	const query = useQuery({
		queryKey: ["sync-link", "identity"],
		queryFn: fetchDesktopAccount,
		enabled: DESKTOP,
		staleTime: 30_000,
	});
	const resolved = query.isSuccess && !query.isFetching;
	return {
		user: DESKTOP && resolved ? (query.data ?? null) : null,
		status: !DESKTOP || resolved ? "resolved" : "resolving",
	};
}

export function useDesktopAccount(): AccountUser | null {
	return useDesktopAccountIdentity().user;
}

export interface DesktopAccountScopeIdentity {
	scope: string | null;
	status: "resolving" | "resolved";
}

// Account data is scoped to both the remote server and user. An invalidated link is treated as
// unknown until its refetch completes, so data from the prior server is never shown during a switch.
export function useDesktopAccountScopeIdentity(user: AccountUser | null): DesktopAccountScopeIdentity {
	const link = useQuery({
		queryKey: ["sync-link", "account-scope-link"],
		queryFn: syncApi.linkGet,
		enabled: DESKTOP && Boolean(user),
		staleTime: 30_000,
	});
	if (!DESKTOP || !user) return { scope: null, status: "resolved" };
	if (!link.isSuccess || link.isFetching || link.data.username !== user.username) {
		return { scope: null, status: "resolving" };
	}
	return { scope: JSON.stringify([link.data.url, user.id, user.username]), status: "resolved" };
}

export function useDesktopAccountScope(user: AccountUser | null): string | null {
	return useDesktopAccountScopeIdentity(user).scope;
}
