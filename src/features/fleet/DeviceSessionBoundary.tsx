// 设备会话的账号边界: 常驻 App 根部, 与设备管理视图是否打开无关。身份变化
// (WEB logout/401 会话过期/账号 A->B, 桌面 sync link 断开/换绑) 时关掉全部
// 设备类工作区并断开其会话, 旧账号的设备 shell 不会留给新账号。
// previous 防止 mount 时误清。

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
		previous.current = userId;
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
