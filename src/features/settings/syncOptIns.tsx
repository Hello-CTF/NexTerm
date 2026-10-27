import { useEffect, useId, useState } from "react";
import type { SyncKindOptIn } from "../../ipc/types";
import { IconChevronDown, IconChevronRight } from "../../ui/icons";

export const SYNC_CONSOLE_TITLE = "账号同步";
export const SYNC_AUTO_BADGE = "自动同步";
export const SYNC_INTERVAL_LABEL = "每 60 秒";
export const SYNC_MERGE_GUIDE = "数据会自动在本机与服务端之间同步。";

interface SyncOptInItem {
  key: keyof SyncKindOptIn;
  summary: string;
  label: string;
  hint: string;
}

export const SYNC_OPT_IN_ITEMS: SyncOptInItem[] = [
  {
    key: "knownHost",
    summary: "已知主机",
    label: "同步已知主机（主机信任）",
    hint: "默认开启。关闭后不再同步主机信任，也不会删除服务端已有副本。",
  },
  {
    key: "aiProfile",
    summary: "AI 模型档案",
    label: "同步 AI 模型档案",
    hint: "默认开启。关闭后不再同步模型档案；含 API 密钥的档案仅在凭据库解锁时同步。",
  },
  {
    key: "aiPermission",
    summary: "AI 权限",
    label: "同步 AI 权限与拦截规则",
    hint: "默认开启。关闭后不再同步全局权限模式和危险命令拦截规则。",
  },
  {
    key: "aiGrant",
    summary: "AI 授权",
    label: "同步 AI 授权与权限模式",
    hint: "默认开启。设备授权、路径授权规则与设备权限模式端到端加密同步；会话内临时授权不同步。",
  },
  {
    key: "preferences",
    summary: "外观快捷键",
    label: "同步外观与快捷键",
    hint: "默认开启。关闭后不再同步界面外观和快捷键覆盖；定时任务由单独的开关控制。",
  },
  {
    key: "stateRules",
    summary: "状态规则",
    label: "同步会话状态规则",
    hint: "默认开启。同步后的规则会推送到本机的会话助手；规则顺序按各设备收到的先后排列。",
  },
  {
    key: "runbookTemplates",
    summary: "Runbook 模板",
    label: "同步 Runbook 模板",
    hint: "默认开启。模板中的命令按保存时的快照同步。",
  },
  {
    key: "skills",
    summary: "AI 技能",
    label: "同步自定义 AI 技能与内置技能的修改",
    hint: "默认开启。只同步你的自定义技能、对内置技能的修改和禁用；内置技能本身随应用提供。",
  },
  {
    key: "memory",
    summary: "AI 记忆",
    label: "同步 AI 记忆",
    hint: "默认关闭。记忆内容端到端加密同步，离开本机前和写入前都会再次脱敏疑似密钥。",
  },
  {
    key: "cron",
    summary: "定时任务",
    label: "同步 AI 定时任务",
    hint: "默认关闭。每个任务只在创建它的设备上运行；其他设备只显示，可在此删除不再需要的任务。",
  },
];

export function syncOptInSummary(optIn: SyncKindOptIn): string {
  return SYNC_OPT_IN_ITEMS.map((item) => `${item.summary} ${optIn[item.key] ? "已开启" : "已关闭"}`).join(" · ");
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
  const panelId = useId();
  const [open, setOpen] = useState(false);
  const anyOn = SYNC_OPT_IN_ITEMS.some((item) => value[item.key]);
  useEffect(() => {
    if (anyOn) setOpen(true);
  }, [anyOn]);

  return (
    <div className="mb-3">
      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          className="flex items-center gap-1.5 text-[12px] text-neutral-400 transition-colors hover:text-neutral-100"
          aria-expanded={open}
          aria-controls={panelId}
          onClick={() => setOpen((v) => !v)}
        >
          {open ? (
            <IconChevronDown size={12} className="shrink-0" />
          ) : (
            <IconChevronRight size={12} className="shrink-0" />
          )}
          同步内容开关
        </button>
        <span className="nx-hint text-[11px]">{syncOptInSummary(value)}</span>
      </div>
      {open && (
        <div id={panelId} className="mt-2 flex flex-col gap-2">
          {SYNC_OPT_IN_ITEMS.map((item) => (
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
        </div>
      )}
    </div>
  );
}
