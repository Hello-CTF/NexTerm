import { useCallback, useEffect, useState } from "react";
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
import { useAuth } from "../auth/store";
import { IconClose, IconKey, IconLoader, IconPlus } from "../../ui/icons";

const GRANT_KIND_OPTIONS: { value: AiGrantKind; label: string; hint: string }[] = [
  { value: "terminal_write", label: "终端写入", hint: "AI 替你输入：按键直接发进终端" },
  { value: "session_exec", label: "命令执行", hint: "仅放行需确认的普通 AI 命令；高危或无法判断的操作仍按当前权限模式处理，禁止操作始终拒绝" },
];

const KIND_LABEL: Record<AiGrantKind, string> = {
  terminal_write: "终端写入",
  session_exec: "命令执行",
};

const RULE_ACTION_OPTIONS: { value: AiGrantRuleAction; label: string; needsPath: boolean; hint: string }[] = [
  { value: "write_file", label: "写入文件", needsPath: true, hint: "只允许写匹配路径下的文件，如 /var/log/**" },
  { value: "edit_file", label: "修改文件", needsPath: true, hint: "只允许修改匹配路径下的文件，如 /etc/nginx/**" },
  { value: "exec_commands", label: "执行命令", needsPath: false, hint: "仅放行需确认的普通 AI 命令；高危或无法判断的操作仍按当前权限模式处理，禁止操作始终拒绝" },
  { value: "send_keys", label: "发送按键", needsPath: false, hint: "仅放行需确认的普通终端输入；高危或无法判断的操作仍按当前权限模式处理，禁止操作始终拒绝" },
];

const RULE_ACTION_LABEL: Record<AiGrantRuleAction, string> = {
  write_file: "写入文件",
  edit_file: "修改文件",
  exec_commands: "执行命令",
  send_keys: "发送按键",
};

const RULE_EXPIRY_OPTIONS: { value: string; label: string; millis: number }[] = [
  { value: "0", label: "永久", millis: 0 },
  { value: "1h", label: "1 小时", millis: 3600_000 },
  { value: "1d", label: "1 天", millis: 86_400_000 },
  { value: "7d", label: "7 天", millis: 7 * 86_400_000 },
  { value: "30d", label: "30 天", millis: 30 * 86_400_000 },
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

// Grant targets are real assets plus fleet devices, the latter keyed by the device/<id> pseudo asset id
// (same form the guard store and session_connect_device use); only id/name/kind are ever rendered.
type GrantTarget = { id: string; name: string; kind: string };

function ruleScopeText(rule: AiGrantRule): string {
  const action = RULE_ACTION_LABEL[rule.action] ?? rule.action;
  return rule.path ? `${action} ${rule.path}` : action;
}

function ruleExpiryText(rule: AiGrantRule, now: number): string {
  if (rule.expiresAt === 0) return "永久";
  if (rule.expiresAt <= now) return "已过期";
  return `${new Date(rule.expiresAt).toLocaleString()} 过期`;
}

export function GrantPanel({ onClose }: { onClose: () => void }) {
  const pushToast = useUi((s) => s.pushToast);
  // Grants are owner-scoped server-side (mutations are rejected for assets the caller does not own):
  // a logged-in account manages only its own grants, while desktop and explicit open deployments
  // share one instance-wide grant state.
  const account = useAuth((s) => s.user);
  const grantScopeNote = account
    ? "授权按账号隔离，仅对当前账号生效，其他账号不可见"
    : "桌面和明确开放部署使用共享授权，对当前 NexTerm 实例的所有会话生效";
  const grantStoreName = account ? "当前账号" : "当前 NexTerm 实例";
  const [assets, setAssets] = useState<Asset[] | null>(null);
  const [devices, setDevices] = useState<FleetDevice[]>([]);
  const [grants, setGrants] = useState<Record<string, AiDeviceGrant>>({});
  const [drafts, setDrafts] = useState<Record<string, AiGrantKind[]>>({});
  const [busy, setBusy] = useState<Record<string, boolean>>({});
  const [rules, setRules] = useState<AiGrantRule[]>([]);
  const [ruleDevice, setRuleDevice] = useState("");
  const [ruleAction, setRuleAction] = useState<AiGrantRuleAction>("write_file");
  const [rulePath, setRulePath] = useState("");
  const [ruleExpiry, setRuleExpiry] = useState("0");
  const [ruleBusy, setRuleBusy] = useState(false);
  const [ruleFormOpen, setRuleFormOpen] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);

  const reload = useCallback(async () => {
    try {
      const [assetRows, grantRows, ruleRows, deviceRows] = await Promise.all([
        assetApi.list(),
        grantApi.list(),
        grantApi.ruleList(),
        // Fleet enumeration is additive: when the fleet link is unavailable (e.g. desktop logged out)
        // the panel degrades to asset-only targets instead of failing the whole load.
        fleetClient().devices().then(
          (r) => r.devices,
          () => [] as FleetDevice[],
        ),
      ]);
      const map: Record<string, AiDeviceGrant> = {};
      for (const grant of grantRows) map[grant.deviceId] = grant;
      setAssets(assetRows);
      setDevices(deviceRows);
      setGrants(map);
      setRules(ruleRows);
      setLoadError(null);
    } catch (e) {
      setLoadError(describeError(e));
    }
  }, []);

  useEffect(() => {
    void reload();
  }, [reload]);

  const kindsFor = (asset: GrantTarget): AiGrantKind[] =>
    drafts[asset.id] ?? (grants[asset.id]?.kinds.length ? grants[asset.id].kinds : ["terminal_write"]);

  const toggleKind = (asset: GrantTarget, kind: AiGrantKind) => {
    const current = kindsFor(asset);
    const next = current.includes(kind) ? current.filter((k) => k !== kind) : [...current, kind];
    setDrafts((prev) => ({ ...prev, [asset.id]: next }));
  };

  const enableGrant = async (asset: GrantTarget) => {
    const kinds = kindsFor(asset);
    if (kinds.length === 0) return;
    const scope = kinds.map((k) => KIND_LABEL[k]).join("、");
    const confirmed = await ask(
      `为「${asset.name}」开启设备长期授权？\n\n开启后，该设备上的${scope}无需逐次确认，只读模式下也会放行；高危或无法判断的操作仍按当前权限模式处理，禁止操作始终拒绝。${grantScopeNote}。`,
      { kind: "warning" },
    );
    if (!confirmed) return;
    setBusy((prev) => ({ ...prev, [asset.id]: true }));
    try {
      const saved = await grantApi.set(asset.id, kinds);
      setGrants((prev) => ({ ...prev, [asset.id]: saved }));
      pushToast("success", `已开启「${asset.name}」的设备授权（${scope}）`);
    } catch (e) {
      pushToast("error", `开启授权失败：${describeError(e)}`);
    } finally {
      setBusy((prev) => ({ ...prev, [asset.id]: false }));
    }
  };

  const revoke = async (asset: GrantTarget) => {
    const scope = (grants[asset.id]?.kinds ?? []).map((k) => KIND_LABEL[k]).join("、");
    const confirmed = await ask(
      `撤销「${asset.name}」的设备授权？\n\n撤销后，该设备上的${scope || "终端写入、命令执行"}将按当前权限模式处理，授权记录会从${grantStoreName}删除。`,
      { kind: "warning" },
    );
    if (!confirmed) return;
    setBusy((prev) => ({ ...prev, [asset.id]: true }));
    try {
      await grantApi.revoke(asset.id);
      setGrants((prev) => {
        const next = { ...prev };
        delete next[asset.id];
        return next;
      });
      pushToast("success", `已撤销「${asset.name}」的设备授权，相关操作按当前权限模式处理`);
    } catch (e) {
      pushToast("error", `撤销授权失败：${describeError(e)}`);
    } finally {
      setBusy((prev) => ({ ...prev, [asset.id]: false }));
    }
  };

  const actionNeedsPath = RULE_ACTION_OPTIONS.find((o) => o.value === ruleAction)?.needsPath ?? true;

  const addRule = async () => {
    const device = allTargets.find((a) => a.id === ruleDevice);
    if (!device || ruleBusy) return;
    const pattern = rulePath.trim();
    if (actionNeedsPath && pattern === "") {
      pushToast("error", `请填写${RULE_ACTION_LABEL[ruleAction]}规则的路径范围，例如 /var/log/**`);
      return;
    }
    const expiry = RULE_EXPIRY_OPTIONS.find((o) => o.value === ruleExpiry) ?? RULE_EXPIRY_OPTIONS[0];
    const scope = actionNeedsPath
      ? `${RULE_ACTION_LABEL[ruleAction]} ${pattern}`
      : RULE_ACTION_LABEL[ruleAction];
    const confirmed = await ask(
      `为「${device.name}」添加授权规则？\n\n范围：${scope}\n有效期：${expiry.label}\n\n匹配规则的普通 AI 操作无需逐次确认，只读模式下也会放行；高危或无法判断的操作仍按当前权限模式处理，禁止操作始终拒绝。规则保存在${grantStoreName}，可随时撤销。`,
      { kind: "warning" },
    );
    if (!confirmed) return;
    setRuleBusy(true);
    try {
      const saved = await grantApi.ruleSet(
        device.id,
        ruleAction,
        actionNeedsPath ? pattern : "",
        expiry.millis === 0 ? 0 : Date.now() + expiry.millis,
      );
      setRules((prev) => [...prev.filter((r) => r.id !== saved.id), saved]);
      setRulePath("");
      setRuleFormOpen(false);
      pushToast("success", `已添加授权规则：${device.name} ${scope}`);
    } catch (e) {
      pushToast("error", `添加授权规则失败：${describeError(e)}`);
    } finally {
      setRuleBusy(false);
    }
  };

  const revokeRule = async (rule: AiGrantRule) => {
    const device = allTargets.find((a) => a.id === rule.deviceId);
    const confirmed = await ask(
      `撤销授权规则「${device?.name ?? rule.deviceId} ${ruleScopeText(rule)}」？\n\n撤销后，相关操作将按当前权限模式处理，规则记录会从${grantStoreName}删除。`,
      { kind: "warning" },
    );
    if (!confirmed) return;
    setRuleBusy(true);
    try {
      await grantApi.ruleRevoke(rule.id);
      setRules((prev) => prev.filter((r) => r.id !== rule.id));
      pushToast("success", "已撤销授权规则");
    } catch (e) {
      pushToast("error", `撤销授权规则失败：${describeError(e)}`);
    } finally {
      setRuleBusy(false);
    }
  };

  // Revoked devices can no longer host sessions, so they are not offered as grant or rule targets;
  // existing rules that reference them still render by name via allTargets/assetName fallback.
  const deviceTargets: GrantTarget[] = devices
    .filter((d) => d.revoked_at === 0)
    .map((d) => ({ id: `device/${d.id}`, name: d.name, kind: "device" }));
  const allTargets: GrantTarget[] = [...(assets ?? []), ...deviceTargets];
  const assetName = (deviceId: string) => allTargets.find((a) => a.id === deviceId)?.name ?? deviceId;
  const grantAssets: GrantTarget[] | null =
    assets === null ? null : [...assets.filter((a) => !DB_ASSET_KINDS.has(a.kind)), ...deviceTargets];
  const now = Date.now();

  return (
    <div className="max-h-[55%] shrink-0 overflow-y-auto border-b border-neutral-800/60 bg-neutral-900/60 p-2.5">
      <div className="mb-1.5 flex items-center gap-1.5 text-[11.5px] font-medium text-neutral-200">
        <IconKey size={12} />
        设备长期授权
        <span className="nx-spacer" />
        <button className="nx-icon-btn nx-icon-btn-sm" title="关闭" onClick={onClose}>
          <IconClose size={11} />
        </button>
      </div>

      <div className="mb-2 px-0.5 text-[10.5px] leading-relaxed text-neutral-500">
        {`设备长期授权默认关闭。开启后，普通终端写入和命令执行无需逐次确认，只读模式下也会放行；高危或无法判断的操作仍按当前权限模式处理，禁止操作始终拒绝。${grantScopeNote}，请仅用于可信设备。数据库资产不支持设备授权。`}
      </div>

      {loadError && (
        <div className="mb-1.5 flex items-center gap-1.5 text-[11px] text-red-400">
          <span className="min-w-0 flex-1 truncate">加载失败：{loadError}</span>
          <button className="nx-btn nx-btn-outline nx-btn-xs" onClick={() => void reload()}>
            重试
          </button>
        </div>
      )}

      {!loadError && assets === null && (
        <div className="flex items-center gap-1.5 py-1 text-[11px] text-neutral-500">
          <IconLoader size={12} />
          加载资产与授权状态…
        </div>
      )}

      {grantAssets?.map((asset) => {
        const grant = grants[asset.id];
        const pending = busy[asset.id] === true;
        return (
          <div key={asset.id} className="mb-1 rounded-lg border border-neutral-800/60 px-2 py-1.5">
            <div className="flex items-center gap-1.5">
              <span className="min-w-0 flex-1 truncate text-[11.5px] text-neutral-200">{asset.name}</span>
              <span className="nx-count">{ASSET_KIND_LABEL[asset.kind] ?? asset.kind}</span>
              {grant ? (
                <button
                  className="nx-btn nx-btn-outline nx-btn-xs"
                  disabled={pending}
                  onClick={() => void revoke(asset)}
                >
                  {pending ? "撤销中…" : "撤销"}
                </button>
              ) : (
                <button
                  className="nx-btn nx-btn-outline nx-btn-xs"
                  disabled={pending || kindsFor(asset).length === 0}
                  onClick={() => void enableGrant(asset)}
                >
                  {pending ? "开启中…" : "开启授权"}
                </button>
              )}
            </div>
            {grant ? (
              <div className="mt-0.5 text-[10.5px] text-green-300">
                已授权：{grant.kinds.map((k) => KIND_LABEL[k]).join("、")}
              </div>
            ) : (
              <div className="mt-1 flex flex-wrap gap-1">
                {GRANT_KIND_OPTIONS.map((opt) => {
                  const active = kindsFor(asset).includes(opt.value);
                  return (
                    <button
                      key={opt.value}
                      className={`nx-btn nx-btn-outline nx-btn-xs ${active ? "border-blue-500/50 text-blue-300" : ""}`}
                      title={opt.hint}
                      onClick={() => toggleKind(asset, opt.value)}
                    >
                      {active ? "● " : "○ "}
                      {opt.label}
                    </button>
                  );
                })}
                <span className="self-center text-[10px] text-neutral-500">未授权</span>
              </div>
            )}
          </div>
        );
      })}

      {!loadError && grantAssets !== null && grantAssets.length === 0 && (
        <div className="py-1 text-[11px] text-neutral-500">
          {assets !== null && assets.length > 0
            ? "数据库资产不支持 AI 授权，可授权的资产为空。"
            : "暂无资产，添加资产后可在此管理授权。"}
        </div>
      )}

      <div className="mb-1.5 mt-2 flex items-center gap-1.5 border-t border-neutral-800/60 pt-2 text-[11.5px] font-medium text-neutral-200">
        <IconKey size={12} />
        授权规则
        <span className="nx-count">{rules.length > 0 ? `${rules.length} 条` : "未设置"}</span>
      </div>

      <div className="mb-2 px-0.5 text-[10.5px] leading-relaxed text-neutral-500">
        授权规则按设备、动作和路径范围放行，例如仅允许写入 /var/log/** 下的文件。规则可设有效期，到期自动失效，也可随时撤销。
      </div>

      {rules.map((rule) => (
        <div key={rule.id} className="mb-1 flex items-center gap-1.5 rounded-lg border border-neutral-800/60 px-2 py-1.5">
          <span className="min-w-0 flex-1 truncate text-[11.5px] text-neutral-200" title={ruleScopeText(rule)}>
            {assetName(rule.deviceId)} · {ruleScopeText(rule)}
          </span>
          <span className={`shrink-0 text-[10.5px] ${rule.expiresAt !== 0 && rule.expiresAt <= now ? "text-red-300" : "text-neutral-500"}`}>
            {ruleExpiryText(rule, now)}
          </span>
          <button
            className="nx-btn nx-btn-outline nx-btn-xs shrink-0"
            disabled={ruleBusy}
            onClick={() => void revokeRule(rule)}
          >
            撤销
          </button>
        </div>
      ))}

      {assets !== null && grantAssets !== null && grantAssets.length > 0 && (
        <div className="mt-2">
          <button
            type="button"
            className="nx-btn nx-btn-outline nx-btn-xs"
            aria-expanded={ruleFormOpen}
            aria-controls="ai-grant-rule-form"
            onClick={() => setRuleFormOpen((v) => !v)}
          >
            <IconPlus size={11} />
            添加规则
          </button>
          {ruleFormOpen && (
            <div id="ai-grant-rule-form" className="mt-1.5 rounded-lg border border-neutral-800/60 px-2 py-1.5">
              <div className="flex flex-wrap items-center gap-1">
                <select
                  className="nx-input max-w-32 py-0.5 text-[11px]"
                  aria-label="规则设备"
                  value={ruleDevice}
                  onChange={(event) => setRuleDevice(event.target.value)}
                >
                  <option value="">选择设备</option>
                  {grantAssets?.map((asset) => (
                    <option key={asset.id} value={asset.id}>
                      {asset.name}
                    </option>
                  ))}
                </select>
                <select
                  className="nx-input py-0.5 text-[11px]"
                  aria-label="规则动作"
                  value={ruleAction}
                  onChange={(event) => setRuleAction(event.target.value as AiGrantRuleAction)}
                >
                  {RULE_ACTION_OPTIONS.map((opt) => (
                    <option key={opt.value} value={opt.value}>
                      {opt.label}
                    </option>
                  ))}
                </select>
                {actionNeedsPath && (
                  <input
                    className="nx-input min-w-36 flex-1 py-0.5 font-mono text-[11px]"
                    placeholder="/var/log/**"
                    aria-label="规则路径范围"
                    value={rulePath}
                    onChange={(event) => setRulePath(event.target.value)}
                  />
                )}
                <select
                  className="nx-input py-0.5 text-[11px]"
                  aria-label="规则有效期"
                  value={ruleExpiry}
                  onChange={(event) => setRuleExpiry(event.target.value)}
                >
                  {RULE_EXPIRY_OPTIONS.map((opt) => (
                    <option key={opt.value} value={opt.value}>
                      {opt.label}
                    </option>
                  ))}
                </select>
                <button
                  className="nx-btn nx-btn-outline"
                  disabled={ruleBusy || ruleDevice === "" || (actionNeedsPath && rulePath.trim() === "")}
                  onClick={() => void addRule()}
                >
                  {ruleBusy ? "保存中…" : "添加"}
                </button>
              </div>
              <div className="mt-1 text-[10px] leading-relaxed text-neutral-500">
                {RULE_ACTION_OPTIONS.find((o) => o.value === ruleAction)?.hint}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
