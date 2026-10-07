// 设备终端的账号边界 (FLEET156): 常驻 App 根部 (WEB), 与 DevicesView 是否
// 打开无关。AuthGate 只是 overlay, 登出/切换账号不会卸载任何 pane, 所以旧
// 账号的设备终端标签必须由这里统一关闭: user.id 变化 (logout/401 会话过期/
// 账号 A->B) 时关闭全部 deviceTerminal 标签, 视图卸载即 detach 桥接, 旧账号
// 的 shell 不会留给新账号。用 previous user id 防止 mount 时误清。

import { useEffect, useRef } from "react";
import { useAuth } from "../auth/store";
import { useUi } from "../../app/store";

export function DeviceTerminalBoundary() {
  const userId = useAuth((s) => s.user?.id ?? null);
  const previous = useRef(userId);

  useEffect(() => {
    if (previous.current === userId) return;
    previous.current = userId;
    const st = useUi.getState();
    for (const w of st.workspaces) {
      for (const p of w.panes) {
        for (const t of p.tabs) {
          if (t.kind === "deviceTerminal") void st.closeTab(t.id);
        }
      }
    }
  }, [userId]);

  return null;
}
