import { DESKTOP } from "../ipc/env";
import { askChoice } from "../ui/dialogs";
import { getUpdateState, useUpdateStore } from "./useUpdate";

// Never prompt when the current version is not trustworthy: for dev builds (Version="dev") and the CI placeholder (0.0.0),
// the backend isNewerVersion is always true, so without this guard every startup would prompt.
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
  // Record before prompting: re-entry during await will not prompt twice for the same version; only one prompt per launch.
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
  // "later" is equivalent to null (Esc/cancel): closes only this time, writes no preference.
  if (choice === "update") void getUpdateState().installUpdate();
  else if (choice === "skip") getUpdateState().skipVersion(status.version);
}
