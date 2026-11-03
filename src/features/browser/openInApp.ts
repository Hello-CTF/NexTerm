import { nextTabId, useUi } from "../../app/store";
import { isMobileShell } from "../../app/shell/shellMode";
import { WEB } from "../../ipc/env";
import { openExternalURL } from "../../ipc/wails";
import { describeError } from "../../ui/errorText";
import { currentEmbedEnv, planEmbed } from "./embedPolicy";

function hostOf(url: string): string {
  try {
    return new URL(url).host || url;
  } catch {
    return url;
  }
}

export function openUrlInApp(url: string): void {
  const state = useUi.getState();
  const plan = planEmbed(url, currentEmbedEnv(WEB, isMobileShell()));
  if (plan.kind === "blocked") {
    state.pushToast("error", `无法在应用内打开：${plan.reason}`);
    return;
  }
  if (plan.kind === "external") {
    state.pushToast("info", `${plan.reason}，已改用系统浏览器`);
    if (WEB) {
      window.open(plan.url, "_blank", "noopener,noreferrer");
      return;
    }
    void openExternalURL(plan.url).catch((e: unknown) => state.pushToast("error", `无法打开链接：${describeError(e)}`));
    return;
  }
  state.addTab({ id: nextTabId("browser"), kind: "browser", title: hostOf(plan.url), url: plan.url, closable: true });
}
