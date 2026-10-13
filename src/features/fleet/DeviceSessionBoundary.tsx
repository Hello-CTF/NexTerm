// Account boundary for device sessions: lives permanently at the App root, regardless of whether the device management view is open. On identity changes
// (WEB logout/401 session expiry/account A->B, desktop sync link disconnect/rebind), close all device workspaces
// and disconnect their sessions, so the old account's device shells are never left to the new account.
// previous prevents a mistaken cleanup at mount.

import { useEffect, useRef } from "react";
import { useAuth } from "../auth/store";
import { useUi } from "../../app/store";
import { WEB } from "../../ipc/env";
import { useDesktopAccount } from "../auth/desktopAccount";
import { sessionApi } from "../../ipc/commands";

export function DeviceSessionBoundary() {
	const webUserId = useAuth((s) => s.user?.id ?? null);
	const desktopUserId = useDesktopAccount()?.id ?? null;
	const userId = WEB ? webUserId : desktopUserId;
	const previous = useRef(userId);

	useEffect(() => {
		if (previous.current === userId) return;
		const last = previous.current;
		previous.current = userId;
		// Initial identity resolution (null -> user) is not an account switch: on desktop the account query
		// resolves after the layout boot has already restored device workspaces, and cleaning here would wipe them.
		if (last === null) return;
		const st = useUi.getState();
		const deviceWorkspaces = st.workspaces.filter((w) => w.assetKind === "device");
		if (deviceWorkspaces.length === 0) return;
		for (const w of deviceWorkspaces) {
			for (const p of w.panes) {
				for (const t of p.tabs) {
					void st.closeTab(t.id);
				}
			}
			if (w.sessionId) void sessionApi.disconnect(w.sessionId);
		}
		useUi.setState({
			workspaces: st.workspaces.filter((w) => w.assetKind !== "device"),
			activeWorkspaceId: deviceWorkspaces.some((w) => w.id === st.activeWorkspaceId)
				? (st.workspaces.filter((w) => w.assetKind !== "device").at(-1)?.id ?? null)
				: st.activeWorkspaceId,
		});
	}, [userId]);

	return null;
}
