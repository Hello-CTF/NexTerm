import { DESKTOP } from "../ipc/env";
import { askChoice } from "../ui/dialogs";
import { getUpdateState, useUpdateStore } from "./useUpdate";

// 当前版本不可信时一律不弹: dev 构建(Version="dev")与 CI 占位(0.0.0)下
// 后端 isNewerVersion 恒 true, 不挡就会每次启动都弹。
export function isReleaseVersion(version: string): boolean {
  const value = version.trim().replace(/^v/, "");
  if (!value || value === "dev" || value === "0.0.0") return false;
  return /^\d+(\.\d+){0,2}(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$/.test(
    value,
  );
}

export async function promptUpdateIfNeeded(): Promise<void> {
  if (!DESKTOP) return;
  const state = getUpdateState();
  const status = state.status;
  if (!status?.available || !status.version) return;
  if (!isReleaseVersion(status.currentVersion)) return;
  if (status.version === state.skippedVersion) return;
  if (status.version === state.remindedVersion) return;
  // 先记再弹: await 期间重入不会对同一版本弹第二次; 一次启动只弹一次。
  useUpdateStore.setState({ remindedVersion: status.version });
  const choice = await askChoice(
    `发现新版本 v${status.version}（当前 v${status.currentVersion}）`,
    {
      title: "软件更新",
      level: "info",
      choices: [
        { key: "update", label: "更新", hint: `下载并安装 v${status.version}`, primary: true },
        { key: "skip", label: "跳过当前版本", hint: "不再提醒此版本" },
        { key: "later", label: "稍后再说" },
      ],
    },
  );
  // "later" 与 null(Esc/取消) 同等: 仅本次关闭, 不写任何偏好。
  if (choice === "update") void getUpdateState().installUpdate();
  else if (choice === "skip") getUpdateState().skipVersion(status.version);
}
