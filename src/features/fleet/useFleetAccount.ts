// 设备/分享视图的统一账号信号: WEB 取 auth store 会话, 桌面取「账号同步」link
// 建立的服务端会话 (与 features/auth/desktopAccount.ts 同 queryKey 同语义,
// 共享缓存; SyncCard 登录/断开后 invalidate ["sync-link"] 会自动刷新本信号)。

import { useQuery } from "@tanstack/react-query";
import { syncApi } from "../../ipc/commands";
import { remoteAccountApi } from "../../ipc/remoteApi";
import { WEB } from "../../ipc/env";
import { useAuth } from "../auth/store";
import type { AccountUser } from "../../ipc/authApi";

export interface FleetAccount {
  // loading 仅桌面首查期间为真 (WEB 同步可得); userId 非空即已登录。
  loading: boolean;
  userId: string | null;
  isAdmin: boolean;
}

// 与 desktopAccount.ts fetchDesktopAccount 同义: role 用 adminUsers 探测
// (仅超管可调), 未命中按普通用户最小视图。语义改动需两边同步。
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
  // 查询出错 (同步引擎不可用等) 按未登录处理: 降级视图引导去「账号同步」登录。
  if (query.data === undefined) return { loading: !query.isError, userId: null, isAdmin: false };
  if (query.data === null) return { loading: false, userId: null, isAdmin: false };
  return { loading: false, userId: query.data.id || null, isAdmin: query.data.role === "superadmin" };
}
