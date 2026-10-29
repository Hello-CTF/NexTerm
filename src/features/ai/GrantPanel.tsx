import { useCallback, useEffect, useMemo, useState } from "react";
import { assetApi, type Asset } from "../../ipc/commands";
import {
  grantApi,
  type AiDeviceGrant,
  type AiGrantKind,
  type AiGrantRule,
  type AiGrantRuleAction,
} from "../../ipc/grantApi";
import { fleetClient } from "../fleet/clients";
import type { FleetDevice } from "../../ipc/fleetApi";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { Select } from "../../ui/Select";
import { useAuth } from "../auth/store";
import { IconChevronRight, IconClose, IconLoader, IconPlus, IconSearch } from "../../ui/icons";

const KIND_LABEL: Record<AiGrantKind, string> = {
  terminal_write: "终端输入",
  session_exec: "执行命令",
  db_query: "数据库查询",
};

const KIND_HINT: Record<AiGrantKind, string> = {
  terminal_write: "AI 在终端中输入普通内容时不再询问",
  session_exec: "AI 执行普通命令时不再询问",
  db_query: "AI 查询此数据库时不再询问",
};

// Database assets support exactly one grant kind: db_query covers the AI
// database tools on connections resolved to that asset, never an SSH tunnel
// asset or an inline connection.
const TERMINAL_KINDS: AiGrantKind[] = ["session_exec", "terminal_write"];
const DB_KINDS: AiGrantKind[] = ["db_query"];

const RULE_ACTION_OPTIONS: { value: AiGrantRuleAction; label: string; needsPath: boolean }[] = [
  { value: "edit_file", label: "修改文件", needsPath: true },
  { value: "write_file", label: "写入文件", needsPath: true },
  { value: "exec_commands", label: "执行命令", needsPath: false },
  { value: "send_keys", label: "发送按键", needsPath: false },
];

const RULE_ACTION_LABEL: Record<AiGrantRuleAction, string> = {
  write_file: "写入文件",
  edit_file: "修改文件",
  exec_commands: "执行命令",
  send_keys: "发送按键",
};

const RULE_EXPIRY_OPTIONS: { value: string; label: string; millis: number }[] = [
  { value: "1d", label: "1 天", millis: 86_400_000 },
  { value: "1h", label: "1 小时", millis: 3600_000 },
  { value: "7d", label: "7 天", millis: 7 * 86_400_000 },
  { value: "30d", label: "30 天", millis: 30 * 86_400_000 },
  { value: "0", label: "永久", millis: 0 },
];

const ASSET_KIND_LABEL: Record<string, string> = {
  ssh: "SSH",
  winrm: "WinRM",
  local: "本地终端",
  docker: "Docker",
  mysql: "MySQL",
  postgres: "PostgreSQL",
  redis: "Redis",
  device: "设备",
};

const DB_ASSET_KINDS = new Set(["mysql", "postgres", "redis"]);
const SEARCH_LIMIT = 6;

// Grant targets are real assets plus fleet devices, the latter keyed by the
// device/<id> pseudo asset id used by the guard store and device sessions.
type GrantTarget = { id: string; name: string; kind: string };

function ruleScopeText(rule: AiGrantRule): string {
  const action = RULE_ACTION_LABEL[rule.action as AiGrantRuleAction] ?? rule.action;
  return rule.path ? `${action} ${rule.path}` : action;
}

function ruleExpiryText(rule: AiGrantRule, now: number): string {
  const expiresAt = rule.expiresAt ?? 0;
  if (expiresAt === 0) return "永久";
  if (expiresAt <= now) return "已过期";
  return `${new Date(expiresAt).toLocaleDateString()} 到期`;
}

function kindsOf(target: GrantTarget): AiGrantKind[] {
  return DB_ASSET_KINDS.has(target.kind) ? DB_KINDS : TERMINAL_KINDS;
}

interface GrantState {
  grants: Record<string, AiDeviceGrant>;
  rules: AiGrantRule[];
  setGrant: (target: GrantTarget, kind: AiGrantKind, on: boolean) => Promise<void>;
  addRule: (target: GrantTarget, action: AiGrantRuleAction, path: string, expiry: string) => Promise<boolean>;
  revokeRule: (rule: AiGrantRule) => Promise<void>;
  revokeAll: (target: GrantTarget) => Promise<void>;
  busy: string | null;
}

// GrantPanel manages long-term AI grants: the AI's current host first, then
// only the hosts that already hold a grant or rule, and a search to grant
// another host. Every other host stays out of the list.
export function GrantPanel({ currentAssetId }: { currentAssetId?: string }) {
  const pushToast = useUi((s) => s.pushToast);
  // Grants are owner-scoped server-side: a logged-in account manages only its
  // own grants, while desktop and explicit open deployments share one
  // instance-wide grant state.
  const account = useAuth((s) => s.user);
  const scopeNote = account
    ? "授权只对当前账号生效。"
    : "授权对当前 NexTerm 实例的所有会话生效，请仅用于可信主机。";
  const storeName = account ? "当前账号" : "当前 NexTerm 实例";
  const [assets, setAssets] = useState<Asset[] | null>(null);
  const [devices, setDevices] = useState<FleetDevice[]>([]);
  const [grants, setGrants] = useState<Record<string, AiDeviceGrant>>({});
  const [rules, setRules] = useState<AiGrantRule[]>([]);
  const [busy, setBusy] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [picked, setPicked] = useState<string | null>(null);

  const reload = useCallback(async () => {
    try {
      const [assetRows, grantRows, ruleRows, deviceRows] = await Promise.all([
        assetApi.list(),
        grantApi.list(),
        grantApi.ruleList(),
        // Fleet enumeration is additive: when the fleet link is unavailable
        // (e.g. desktop logged out) the panel degrades to asset-only targets.
        fleetClient().devices().then(
          (r) => r.devices ?? [],
          () => [] as FleetDevice[],
        ),
      ]);
      const map: Record<string, AiDeviceGrant> = {};
      for (const grant of grantRows ?? []) map[grant.deviceId] = grant;
      setAssets(assetRows ?? []);
      setDevices(deviceRows);
      setGrants(map);
      setRules(ruleRows ?? []);
      setLoadError(null);
    } catch (e) {
      setLoadError(describeError(e));
    }
  }, []);

  useEffect(() => {
    void reload();
  }, [reload]);

  // Revoked devices can no longer host sessions, so they are not offered as
  // targets; rules that still reference them render by their id.
  const targets: GrantTarget[] = useMemo(
    () => [
      ...(assets ?? []),
      ...devices.filter((d) => d.revoked_at === 0).map((d) => ({ id: `device/${d.id}`, name: d.name, kind: "device" })),
    ],
    [assets, devices],
  );
  const targetOf = (id: string): GrantTarget => targets.find((t) => t.id === id) ?? { id, name: id, kind: "" };

  const setGrant = async (target: GrantTarget, kind: AiGrantKind, on: boolean) => {
    const current = grants[target.id]?.kinds ?? [];
    const next = on ? [...new Set([...current, kind])] : current.filter((k) => k !== kind);
    if (on) {
      const confirmed = await ask(
        `确定要让 AI 在「${target.name}」上${KIND_LABEL[kind]}时不再询问吗？\n\n只读模式下同样生效；高危和无法判断的操作仍会询问，禁止的操作始终拒绝。授权保存在${storeName}，可随时关闭。`,
        { title: "开启免确认授权", kind: "warning" },
      );
      if (!confirmed) return;
    }
    setBusy(target.id);
    try {
      if (next.length === 0) {
        await grantApi.revoke(target.id);
        setGrants((prev) => {
          const copy = { ...prev };
          delete copy[target.id];
          return copy;
        });
      } else {
        const saved = await grantApi.set(target.id, next as AiGrantKind[]);
        setGrants((prev) => ({ ...prev, [target.id]: saved }));
      }
      pushToast("success", on ? `「${target.name}」${KIND_LABEL[kind]}已免确认` : `「${target.name}」${KIND_LABEL[kind]}恢复为逐次确认`);
    } catch (e) {
      pushToast("error", `未能${on ? "开启" : "关闭"}授权：${describeError(e)}`);
    } finally {
      setBusy(null);
    }
  };

  const addRule = async (target: GrantTarget, action: AiGrantRuleAction, path: string, expiry: string) => {
    const needsPath = RULE_ACTION_OPTIONS.find((o) => o.value === action)?.needsPath ?? true;
    const pattern = path.trim();
    if (needsPath && pattern === "") return false;
    const option = RULE_EXPIRY_OPTIONS.find((o) => o.value === expiry) ?? RULE_EXPIRY_OPTIONS[0];
    const scope = needsPath ? `${RULE_ACTION_LABEL[action]} ${pattern}` : RULE_ACTION_LABEL[action];
    const confirmed = await ask(
      `确定要为「${target.name}」添加授权规则吗？\n\n范围：${scope}\n有效期：${option.label}\n\n匹配的普通操作不再询问，只读模式下同样生效；高危和无法判断的操作仍会询问。`,
      { title: "添加授权规则", kind: "warning" },
    );
    if (!confirmed) return false;
    setBusy(target.id);
    try {
      const saved = await grantApi.ruleSet(target.id, action, needsPath ? pattern : "", option.millis === 0 ? 0 : Date.now() + option.millis);
      setRules((prev) => [...prev.filter((r) => r.id !== saved.id), saved]);
      pushToast("success", `已添加规则：${target.name} ${scope}`);
      return true;
    } catch (e) {
      pushToast("error", `未能添加规则：${describeError(e)}`);
      return false;
    } finally {
      setBusy(null);
    }
  };

  const revokeRule = async (rule: AiGrantRule) => {
    setBusy(rule.deviceId);
    try {
      await grantApi.ruleRevoke(rule.id);
      setRules((prev) => prev.filter((r) => r.id !== rule.id));
      pushToast("success", `已撤销规则：${ruleScopeText(rule)}`);
    } catch (e) {
      pushToast("error", `未能撤销规则：${describeError(e)}`);
    } finally {
      setBusy(null);
    }
  };

  const revokeAll = async (target: GrantTarget) => {
    const targetRules = rules.filter((r) => r.deviceId === target.id);
    setBusy(target.id);
    try {
      if (grants[target.id]) await grantApi.revoke(target.id);
      for (const rule of targetRules) await grantApi.ruleRevoke(rule.id);
      setGrants((prev) => {
        const copy = { ...prev };
        delete copy[target.id];
        return copy;
      });
      setRules((prev) => prev.filter((r) => r.deviceId !== target.id));
      pushToast("success", `已撤销「${target.name}」的全部授权`);
    } catch (e) {
      pushToast("error", `未能撤销授权：${describeError(e)}`);
      void reload();
    } finally {
      setBusy(null);
    }
  };

  const state: GrantState = { grants, rules, setGrant, addRule, revokeRule, revokeAll, busy };

  if (loadError) {
    return (
      <div className="flex items-center gap-1.5 text-[11px] text-red-400">
        <span className="min-w-0 flex-1">未能读取授权：{loadError}。请检查网络后重试</span>
        <button className="nx-btn nx-btn-outline nx-btn-xs" onClick={() => void reload()}>
          重试
        </button>
      </div>
    );
  }
  if (assets === null) {
    return (
      <div className="flex items-center gap-1.5 py-1 text-[11px] text-neutral-500">
        <IconLoader size={12} />
        正在读取授权…
      </div>
    );
  }

  const current = currentAssetId ? targets.find((t) => t.id === currentAssetId) : undefined;
  const authorizedIds = [...new Set([...Object.keys(grants), ...rules.map((r) => r.deviceId)])].filter((id) => id !== current?.id);
  const authorized = authorizedIds.map(targetOf).sort((a, b) => a.name.localeCompare(b.name));
  const needle = query.trim().toLowerCase();
  const matches = needle
    ? targets.filter((t) => t.id !== current?.id && !authorizedIds.includes(t.id) && t.name.toLowerCase().includes(needle)).slice(0, SEARCH_LIMIT)
    : [];
  const pickedTarget = picked && !authorizedIds.includes(picked) && picked !== current?.id ? targetOf(picked) : null;

  return (
    <div className="flex flex-col gap-2">
      {current && (
        <div className="rounded-lg border border-neutral-800/60 px-2 py-1.5">
          <div className="mb-1 flex items-center gap-1.5">
            <span className="text-[10.5px] text-neutral-500">当前主机</span>
            <span className="min-w-0 flex-1 truncate text-[11.5px] text-neutral-200">{current.name}</span>
            <span className="nx-count">{ASSET_KIND_LABEL[current.kind] ?? current.kind}</span>
          </div>
          <HostGrantEditor target={current} state={state} />
        </div>
      )}

      <div>
        <div className="mb-1 flex items-center gap-1.5 text-[11px] text-neutral-300">
          {current ? "其他已授权主机" : "已授权主机"}
          <span className="nx-count">{authorized.length}</span>
        </div>
        {authorized.length === 0 && <div className="nx-hint text-[10.5px]">暂无其他主机开启免确认授权。</div>}
        {authorized.map((target) => {
          const open = expanded === target.id;
          const grant = grants[target.id];
          const ruleCount = rules.filter((r) => r.deviceId === target.id).length;
          const summary = [
            ...(grant?.kinds ?? []).map((k) => KIND_LABEL[k as AiGrantKind] ?? k),
            ...(ruleCount > 0 ? [`${ruleCount} 条规则`] : []),
          ].join("、");
          return (
            <div key={target.id} className="mb-1 rounded-lg border border-neutral-800/60">
              <div className="flex items-center gap-1.5 px-2 py-1">
                <button
                  type="button"
                  className="flex min-w-0 flex-1 items-center gap-1.5 text-left"
                  aria-expanded={open}
                  onClick={() => setExpanded(open ? null : target.id)}
                >
                  <IconChevronRight size={10} className={`shrink-0 text-neutral-500 ${open ? "rotate-90" : ""}`} />
                  <span className="min-w-0 truncate text-[11.5px] text-neutral-200">{target.name}</span>
                  <span className="min-w-0 flex-1 truncate text-[10.5px] text-green-300/90">{summary}</span>
                </button>
                <button
                  className="nx-btn nx-btn-ghost nx-btn-xs shrink-0"
                  disabled={busy === target.id}
                  title="撤销当前主机的全部免确认授权和规则"
                  aria-label={`撤销「${target.name}」的全部免确认授权和规则`}
                  onClick={() => void revokeAll(target)}
                >
                  {busy === target.id ? "处理中…" : "撤销"}
                </button>
              </div>
              {open && (
                <div className="border-t border-neutral-800/60 px-2 py-1.5">
                  <HostGrantEditor target={target} state={state} />
                </div>
              )}
            </div>
          );
        })}
      </div>

      <div>
        <div className="relative">
          <IconSearch size={11} className="pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-neutral-500" />
          <input
            className="nx-input w-full py-0.5 pl-6 text-[11px]"
            placeholder="为其他主机授权：搜索主机名"
            aria-label="搜索要授权的主机"
            value={query}
            onChange={(event) => {
              setQuery(event.target.value);
              setPicked(null);
            }}
          />
        </div>
        {matches.length > 0 && !pickedTarget && (
          <div className="mt-1 flex flex-col" role="group" aria-label="匹配的主机">
            {matches.map((target) => (
              <button
                key={target.id}
                type="button"
                className="nx-menu-item w-full"
                onClick={() => setPicked(target.id)}
              >
                <span className="nx-menu-label">{target.name}</span>
                <span className="nx-menu-hint">{ASSET_KIND_LABEL[target.kind] ?? target.kind}</span>
              </button>
            ))}
          </div>
        )}
        {needle && matches.length === 0 && !pickedTarget && <div className="nx-hint mt-1 text-[10.5px]">无匹配的未授权主机。</div>}
        {pickedTarget && (
          <div className="mt-1 rounded-lg border border-neutral-800/60 px-2 py-1.5">
            <div className="mb-1 flex items-center gap-1.5">
              <span className="min-w-0 flex-1 truncate text-[11.5px] text-neutral-200">{pickedTarget.name}</span>
              <span className="nx-count">{ASSET_KIND_LABEL[pickedTarget.kind] ?? pickedTarget.kind}</span>
              <button className="nx-icon-btn nx-icon-btn-sm" title="取消" aria-label={`取消选择 ${pickedTarget.name}`} onClick={() => setPicked(null)}>
                <IconClose size={10} />
              </button>
            </div>
            <HostGrantEditor target={pickedTarget} state={state} />
          </div>
        )}
      </div>

      <div className="text-[10px] leading-relaxed text-neutral-500">{scopeNote}</div>
    </div>
  );
}

function HostGrantEditor({ target, state }: { target: GrantTarget; state: GrantState }) {
  const [formOpen, setFormOpen] = useState(false);
  const [action, setAction] = useState<AiGrantRuleAction>("edit_file");
  const [path, setPath] = useState("");
  const [expiry, setExpiry] = useState(RULE_EXPIRY_OPTIONS[0].value);
  const grant = state.grants[target.id];
  const rules = state.rules.filter((r) => r.deviceId === target.id);
  const busy = state.busy === target.id;
  const now = Date.now();
  const database = DB_ASSET_KINDS.has(target.kind);
  const needsPath = RULE_ACTION_OPTIONS.find((o) => o.value === action)?.needsPath ?? true;

  return (
    <div className="flex flex-col gap-1">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="text-[10.5px] text-neutral-500">免确认</span>
        {kindsOf(target).map((kind) => {
          const on = grant?.kinds.includes(kind) ?? false;
          return (
            <label key={kind} className="flex cursor-pointer items-center gap-1 text-[11px] text-neutral-300" title={KIND_HINT[kind]}>
              <input
                type="checkbox"
                className="h-3.5 w-3.5"
                aria-label={`${target.name}：${KIND_LABEL[kind]}免确认`}
                checked={on}
                disabled={busy}
                onChange={(event) => void state.setGrant(target, kind, event.target.checked)}
              />
              {KIND_LABEL[kind]}
            </label>
          );
        })}
      </div>
      {!database && (
        <>
          {rules.map((rule) => (
            <div key={rule.id} className="flex items-center gap-1.5 text-[11px]">
              <span className="min-w-0 flex-1 truncate font-mono text-neutral-300" title={ruleScopeText(rule)}>
                {ruleScopeText(rule)}
              </span>
              <span className={`shrink-0 text-[10.5px] ${(rule.expiresAt ?? 0) !== 0 && (rule.expiresAt ?? 0) <= now ? "text-red-300" : "text-neutral-500"}`}>
                {ruleExpiryText(rule, now)}
              </span>
              <button
                className="nx-icon-btn nx-icon-btn-sm shrink-0"
                title={`撤销规则：${ruleScopeText(rule)}`}
                aria-label={`撤销规则：${ruleScopeText(rule)}`}
                disabled={busy}
                onClick={() => void state.revokeRule(rule)}
              >
                <IconClose size={10} />
              </button>
            </div>
          ))}
          {formOpen ? (
            <div className="flex flex-wrap items-center gap-1">
              <Select
                className="w-32 shrink-0"
                triggerClassName="nx-input nx-select-trigger py-0.5 text-[11px]"
                ariaLabel="规则操作"
                value={action}
                onChange={(value) => setAction(value as AiGrantRuleAction)}
                options={RULE_ACTION_OPTIONS.map((opt) => ({ value: opt.value, label: opt.label }))}
              />
              {needsPath && (
                <input
                  className="nx-input min-w-28 flex-1 py-0.5 font-mono text-[11px]"
                  placeholder="/etc/nginx/**"
                  aria-label="规则路径范围"
                  value={path}
                  onChange={(event) => setPath(event.target.value)}
                />
              )}
              <Select
                className="w-28 shrink-0"
                triggerClassName="nx-input nx-select-trigger py-0.5 text-[11px]"
                ariaLabel="规则有效期"
                value={expiry}
                onChange={setExpiry}
                options={RULE_EXPIRY_OPTIONS.map((opt) => ({ value: opt.value, label: opt.label }))}
              />
              <button
                className="nx-btn nx-btn-outline nx-btn-xs"
                disabled={busy || (needsPath && path.trim() === "")}
                onClick={() =>
                  void state.addRule(target, action, path, expiry).then((saved) => {
                    if (saved) {
                      setPath("");
                      setFormOpen(false);
                    }
                  })
                }
              >
                添加
              </button>
              <button className="nx-btn nx-btn-ghost nx-btn-xs" onClick={() => setFormOpen(false)}>
                取消
              </button>
            </div>
          ) : (
            <button
              type="button"
              className="flex w-fit items-center gap-1 text-[10.5px] text-neutral-500 hover:text-neutral-300"
              title="只对匹配的路径或操作免确认，例如只允许修改 /etc/nginx/** 下的文件"
              onClick={() => setFormOpen(true)}
            >
              <IconPlus size={10} />
              添加路径规则
            </button>
          )}
        </>
      )}
    </div>
  );
}
