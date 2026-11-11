import type { AppTab } from "../../app/store";
import { tabAwareness } from "../../app/sessionAwarenessStore";
import type { MenuItem } from "../../ui/ContextMenu";
import { IconBell, IconBellOff, IconFlag } from "../../ui/icons";
import { isSessionMuted, setSessionMuted } from "../../app/mutedSessions";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import { requestStateMark, stateMarkIntents } from "./stateMarkRequests";
import { t } from "../../i18n";

const STATE_MARK_LABELS = {
  not_this: { label: t("状态有误…"), hint: t("起草一条规则，纠正这类屏幕的状态") },
  this_is_it: { label: t("标记为需要处理…"), hint: t("起草一条规则，将这类屏幕标记为需要处理") },
} as const;

// stateMarkItems offers to correct the attention state a terminal tab shows. State rules run in the durable helper,
// so a tab whose state this process derives cannot use them; the items say so instead of disappearing.
export function stateMarkItems(tab: AppTab, tabId: string): MenuItem[] {
  const awareness = tabAwareness(tabId);
  const intents = stateMarkIntents(awareness?.state);
  if (!awareness || intents.length === 0) return [];
  const helper = awareness.source === "helper";
  return intents.map((intent) => ({
    kind: "item",
    label: STATE_MARK_LABELS[intent].label,
    icon: <IconFlag size={12} />,
    hint: helper ? STATE_MARK_LABELS[intent].hint : t("状态规则只作用于持久终端"),
    disabled: !helper,
    onSelect: () =>
      requestStateMark({ appTabId: tab.id, tabId, assetId: tab.assetId, title: tab.title, intent, current: awareness.state }),
  }));
}

// muteItem toggles whether the session's needs_me and just_finished states notify and show; the choice follows the
// account to every device.
export function muteItem(tabId: string): MenuItem {
  const muted = isSessionMuted(tabId);
  return {
    kind: "item",
    label: muted ? t("取消静音") : t("静音此会话"),
    icon: muted ? <IconBell size={12} /> : <IconBellOff size={12} />,
    hint: muted ? t("恢复该会话的通知和提示") : t("不再为该会话弹通知，也不在收件箱和标签页上提示；在所有设备上生效"),
    onSelect: () => {
      setSessionMuted(tabId, !muted).catch((cause) =>
        useUi.getState().pushToast("error", t("未能保存静音设置：{value}", { value: describeError(cause) })),
      );
    },
  };
}
