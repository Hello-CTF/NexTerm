// 桌面端账号身份: 以「账号同步」link 建立的服务端会话为身份源(SyncStatus 携带
// loggedIn/username/userId)。身份经 remote_account_me 直连获取完整用户视图;
// 该端点不可用时回退 adminUsers 探测(仅超管可调), 未命中(403)即普通用户最小视图。
// queryKey 挂在 ["sync-link"] 前缀下, SyncCard 登录/断开后 invalidate 会自动刷新身份。

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
		// me 不可用时走探测回退
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
		// 非超管(admin 接口被拒绝)或会话刚好失效: 返回普通用户最小视图
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
