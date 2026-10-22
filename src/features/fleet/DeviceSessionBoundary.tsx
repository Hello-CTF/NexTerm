// Account boundary for device sessions: lives permanently at the App root, regardless of whether the device management view is open. On resolved identity changes
// (WEB logout/401 session expiry/account A->B, desktop sync link disconnect/rebind), close all device workspaces
// and disconnect their sessions, so the old account's device shells are never left to the new account.
// previous prevents a mistaken cleanup at mount and remains unchanged while either desktop scope query is resolving.

import { useEffect, useRef } from "react";
import { useAuth } from "../auth/store";
import { WEB } from "../../ipc/env";
import { useDesktopAccountIdentity, useDesktopAccountScopeIdentity } from "../auth/desktopAccount";
import { cleanupDeviceSessions, reportDeviceSessionCleanup } from "./deviceSessionCleanup";

export function DeviceSessionBoundary() {
	const webUserId = useAuth((s) => s.user?.id ?? null);
	const desktopIdentity = useDesktopAccountIdentity();
	const desktopScope = useDesktopAccountScopeIdentity(desktopIdentity.user);
	const accountScope = WEB ? webUserId : desktopScope.scope;
	const identityStatus = WEB || (desktopIdentity.status === "resolved" && desktopScope.status === "resolved")
		? "resolved"
		: "resolving";
	const previous = useRef(accountScope);

	useEffect(() => {
		if (identityStatus === "resolving") return;
		if (previous.current === accountScope) return;
		const last = previous.current;
		previous.current = accountScope;
		// Initial identity resolution (null -> account) is not an account switch: on desktop the account queries
		// resolve after the layout boot has already restored device workspaces, and cleaning here would wipe them.
		if (last === null) return;
		void cleanupDeviceSessions().then(reportDeviceSessionCleanup);
	}, [accountScope, identityStatus]);

	return null;
}
