import type { SyncKindOptIn } from "../../ipc/types";
import { SummaryDialog } from "./SummaryDialog";

export const SYNC_CONSOLE_TITLE = "账号同步";
export const SYNC_AUTO_BADGE = "自动同步";
export const SYNC_INTERVAL_LABEL = "每 60 秒";
export const SYNC_MERGE_GUIDE = "数据将在本机与服务端之间自动同步。";

type SyncOptInGroup = "base" | "ai" | "automation";

const SYNC_OPT_IN_GROUPS: { id: SyncOptInGroup; title: string }[] = [
  { id: "base", title: "基础" },
  { id: "ai", title: "AI" },
  { id: "automation", title: "自动化" },
];

interface SyncOptInItem {
  id: string;
  // Every key the item writes; an item with several keys reads as partly on when the stored values differ.
  keys: readonly (keyof SyncKindOptIn)[];
  group: SyncOptInGroup;
  summary: string;
  label: string;
  hint: string;
}

export const SYNC_OPT_IN_ITEMS: SyncOptInItem[] = [
  {
    id: "knownHost",
    keys: ["knownHost"],
    group: "base",
    summary: "已知主机",
    label: "同步已知主机（主机信任）",
    hint: "关闭后不再同步，服务端已有副本保留。",
  },
  {
    id: "aiProfile",
    keys: ["aiProfile"],
    group: "ai",
    summary: "AI 模型档案",
    label: "同步 AI 模型档案",
    hint: "含 API 密钥的档案仅在凭据库解锁时同步。",
  },
  {
    id: "aiPermission",
    keys: ["aiPermission", "aiGrant"],
    group: "ai",
    summary: "AI 权限",
    label: "AI 权限、授权与拦截规则",
    hint: "权限模式、设备与路径授权、危险命令规则；会话内临时授权不同步。",
  },
  {
    id: "preferences",
    keys: ["preferences"],
    group: "base",
    summary: "外观快捷键",
    label: "同步外观与快捷键",
    hint: "界面外观、终端偏好和快捷键覆盖。",
  },
  {
    id: "stateRules",
    keys: ["stateRules"],
    group: "base",
    summary: "状态规则",
    label: "同步会话状态规则",
    hint: "规则顺序取决于各设备收到的先后。",
  },
  {
    id: "skills",
    keys: ["skills"],
    group: "ai",
    summary: "AI 技能",
    label: "同步自定义 AI 技能与内置技能的修改",
    hint: "内置技能本身随应用提供。",
  },
  {
    id: "memory",
    keys: ["memory"],
    group: "ai",
    summary: "AI 记忆",
    label: "同步 AI 记忆",
    hint: "加密同步，离开本机前和写入前都会再次脱敏疑似密钥。",
  },
  {
    id: "cron",
    keys: ["cron"],
    group: "automation",
    summary: "定时任务",
    label: "同步 AI 定时任务",
    hint: "任务只在绑定的设备上运行，其他设备可改由本机运行或删除。",
  },
];

type SyncOptInState = "on" | "off" | "partial";

export function syncOptInState(item: SyncOptInItem, optIn: SyncKindOptIn): SyncOptInState {
  const on = item.keys.filter((key) => optIn[key]).length;
  if (on === item.keys.length) return "on";
  return on === 0 ? "off" : "partial";
}

export function syncOptInSummary(optIn: SyncKindOptIn): string {
  const notOn = SYNC_OPT_IN_ITEMS.filter((item) => syncOptInState(item, optIn) !== "on");
  const on = SYNC_OPT_IN_ITEMS.length - notOn.length;
  const base = `已开启 ${on} / ${SYNC_OPT_IN_ITEMS.length} 项`;
  if (notOn.length === 0) return base;
  const names = notOn.map((item) => (syncOptInState(item, optIn) === "partial" ? `${item.summary}（部分）` : item.summary));
  return `${base}，未开启：${names.join("、")}`;
}

export function SyncOptInSection({
  value,
  busy,
  onChange,
}: {
  value: SyncKindOptIn;
  busy: boolean;
  onChange: (patch: Partial<SyncKindOptIn>) => void;
}) {
  return (
    <div className="mb-3">
      <SummaryDialog
        summary={<>同步内容：{syncOptInSummary(value)}</>}
        buttonLabel="管理同步项"
        title="管理同步项"
        description="选择哪些内容参与账号同步。关闭某一项不会删除服务端已有的副本。"
      >
        {SYNC_OPT_IN_GROUPS.map((group) => (
          <fieldset key={group.id} className="m-0 flex min-w-0 flex-col gap-2 border-0 p-0">
            <legend className="mb-1 p-0 text-[11.5px] font-medium text-neutral-500">{group.title}</legend>
            {SYNC_OPT_IN_ITEMS.filter((item) => item.group === group.id).map((item) => {
              const state = syncOptInState(item, value);
              return (
              <label key={item.id} className="flex items-start gap-2">
                <input
                  type="checkbox"
                  className="mt-0.5 h-4 w-4 shrink-0"
                  checked={state === "on"}
                  ref={(input) => {
                    if (input) input.indeterminate = state === "partial";
                  }}
                  disabled={busy}
                  onChange={(e) => onChange(Object.fromEntries(item.keys.map((key) => [key, e.target.checked])) as Partial<SyncKindOptIn>)}
                />
                <span className="text-[12px] text-neutral-300">
                  {item.label}
                  <span className="nx-hint block">{item.hint}</span>
                </span>
              </label>
              );
            })}
          </fieldset>
        ))}
      </SummaryDialog>
    </div>
  );
}
