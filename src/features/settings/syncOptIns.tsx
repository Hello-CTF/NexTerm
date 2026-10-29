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
  key: keyof SyncKindOptIn;
  group: SyncOptInGroup;
  summary: string;
  label: string;
  hint: string;
}

export const SYNC_OPT_IN_ITEMS: SyncOptInItem[] = [
  {
    key: "knownHost",
    group: "base",
    summary: "已知主机",
    label: "同步已知主机（主机信任）",
    hint: "默认开启。关闭后不再同步主机信任，也不会删除服务端已有副本。",
  },
  {
    key: "aiProfile",
    group: "ai",
    summary: "AI 模型档案",
    label: "同步 AI 模型档案",
    hint: "默认开启。关闭后不再同步模型档案；含 API 密钥的档案仅在凭据库解锁时同步。",
  },
  {
    key: "aiPermission",
    group: "ai",
    summary: "AI 权限",
    label: "同步 AI 权限与拦截规则",
    hint: "默认开启。关闭后不再同步全局权限模式和危险命令拦截规则。",
  },
  {
    key: "aiGrant",
    group: "ai",
    summary: "AI 授权",
    label: "同步 AI 授权与权限模式",
    hint: "默认开启。设备授权、路径授权规则与设备权限模式端到端加密同步；会话内临时授权不同步。",
  },
  {
    key: "preferences",
    group: "base",
    summary: "外观快捷键",
    label: "同步外观与快捷键",
    hint: "默认开启。关闭后不再同步界面外观和快捷键覆盖；定时任务由单独的开关控制。",
  },
  {
    key: "stateRules",
    group: "base",
    summary: "状态规则",
    label: "同步会话状态规则",
    hint: "默认开启。同步后的规则将应用到本机的会话助手；规则顺序取决于各设备收到的先后。",
  },
  {
    key: "runbookTemplates",
    group: "base",
    summary: "Runbook 模板",
    label: "同步 Runbook 模板",
    hint: "默认开启。模板中的命令按保存时的快照同步。",
  },
  {
    key: "skills",
    group: "ai",
    summary: "AI 技能",
    label: "同步自定义 AI 技能与内置技能的修改",
    hint: "默认开启。仅同步自定义技能，以及对内置技能的修改和禁用；内置技能本身随应用提供。",
  },
  {
    key: "memory",
    group: "ai",
    summary: "AI 记忆",
    label: "同步 AI 记忆",
    hint: "默认关闭。记忆内容端到端加密同步，离开本机前和写入前都会再次脱敏疑似密钥。",
  },
  {
    key: "cron",
    group: "automation",
    summary: "定时任务",
    label: "同步 AI 定时任务",
    hint: "默认关闭。每个任务只在它绑定的设备上运行；其他设备仅显示该任务，可在此改由本机运行或删除。",
  },
];

export function syncOptInSummary(optIn: SyncKindOptIn): string {
  const off = SYNC_OPT_IN_ITEMS.filter((item) => !optIn[item.key]);
  const on = SYNC_OPT_IN_ITEMS.length - off.length;
  const base = `已开启 ${on} / ${SYNC_OPT_IN_ITEMS.length} 项`;
  return off.length === 0 ? base : `${base}，未开启：${off.map((item) => item.summary).join("、")}`;
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
            {SYNC_OPT_IN_ITEMS.filter((item) => item.group === group.id).map((item) => (
              <label key={item.key} className="flex items-start gap-2">
                <input
                  type="checkbox"
                  className="mt-0.5 h-4 w-4 shrink-0"
                  checked={value[item.key]}
                  disabled={busy}
                  onChange={(e) => onChange({ [item.key]: e.target.checked })}
                />
                <span className="text-[12px] text-neutral-300">
                  {item.label}
                  <span className="nx-hint block">{item.hint}</span>
                </span>
              </label>
            ))}
          </fieldset>
        ))}
      </SummaryDialog>
    </div>
  );
}
