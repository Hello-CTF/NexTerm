import type { AppTab } from "../../app/store";
import { tabAwareness } from "../../app/sessionAwarenessStore";
import type { MenuItem } from "../../ui/ContextMenu";
import { IconFlag } from "../../ui/icons";
import { requestStateMark, stateMarkIntents } from "./stateMarkRequests";

const STATE_MARK_LABELS = {
  not_this: { label: "状态有误…", hint: "起草一条规则，纠正这类屏幕的状态" },
  this_is_it: { label: "标记为需要处理…", hint: "起草一条规则，将这类屏幕标记为需要处理" },
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
    hint: helper ? STATE_MARK_LABELS[intent].hint : "状态规则只作用于持久终端",
    disabled: !helper,
    onSelect: () =>
      requestStateMark({ appTabId: tab.id, tabId, assetId: tab.assetId, title: tab.title, intent, current: awareness.state }),
  }));
}
