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

export function useDesktopAccount(): AccountUser | null {
	const query = useQuery({
		queryKey: ["sync-link", "identity"],
		queryFn: fetchDesktopAccount,
		enabled: DESKTOP,
		staleTime: 30_000,
	});
	return DESKTOP ? (query.data ?? null) : null;
}
