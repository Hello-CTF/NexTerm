import { useCallback, useEffect, useState } from "react";
import { assetApi, type Asset } from "../../ipc/commands";
import { grantApi, type AiDeviceGrant, type AiGrantKind } from "../../ipc/grantApi";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { DEMO } from "../../demo";
import { IconClose, IconKey, IconLoader } from "../../ui/icons";

const GRANT_KIND_OPTIONS: { value: AiGrantKind; label: string; hint: string }[] = [
  { value: "terminal_write", label: "终端写入", hint: "AI 替你输入：按键直接发进终端" },
  { value: "session_exec", label: "命令执行", hint: "AI 直接在设备上运行命令；常规执行不再逐次确认，拦截命中或拿不准仍会问你" },
];

const KIND_LABEL: Record<AiGrantKind, string> = {
  terminal_write: "终端写入",
  session_exec: "命令执行",
};

const ASSET_KIND_LABEL: Record<string, string> = {
  ssh: "SSH",
  winrm: "WinRM",
  local: "本地终端",
  docker: "Docker",
  mysql: "MySQL",
  redis: "Redis",
};

export function GrantPanel({ onClose }: { onClose: () => void }) {
  const pushToast = useUi((s) => s.pushToast);
  const [assets, setAssets] = useState<Asset[] | null>(null);
  const [grants, setGrants] = useState<Record<string, AiDeviceGrant>>({});
  const [drafts, setDrafts] = useState<Record<string, AiGrantKind[]>>({});
  const [busy, setBusy] = useState<Record<string, boolean>>({});
  const [loadError, setLoadError] = useState<string | null>(null);

  const reload = useCallback(async () => {
    try {
      const [assetRows, grantRows] = await Promise.all([assetApi.list(), grantApi.list()]);
      const map: Record<string, AiDeviceGrant> = {};
      for (const grant of grantRows) map[grant.deviceId] = grant;
      setAssets(assetRows);
      setGrants(map);
      setLoadError(null);
    } catch (e) {
      setLoadError(describeError(e));
    }
  }, []);

  useEffect(() => {
    if (DEMO) return;
    void reload();
  }, [reload]);

  if (DEMO) return null;

  const kindsFor = (asset: Asset): AiGrantKind[] =>
    drafts[asset.id] ?? (grants[asset.id]?.kinds.length ? grants[asset.id].kinds : ["terminal_write"]);

  const toggleKind = (asset: Asset, kind: AiGrantKind) => {
    const current = kindsFor(asset);
    const next = current.includes(kind) ? current.filter((k) => k !== kind) : [...current, kind];
    setDrafts((prev) => ({ ...prev, [asset.id]: next }));
  };

  const enableGrant = async (asset: Asset) => {
    const kinds = kindsFor(asset);
    if (kinds.length === 0) return;
    const scope = kinds.map((k) => KIND_LABEL[k]).join("、");
    const confirmed = await ask(
      `为「${asset.name}」开启设备长期授权？\n\n开启后，AI 在该设备上进行${scope}时，原本要逐次确认的不再逐次确认，只读模式下也会被放开；拦截规则命中或拿不准的仍会问你（只读模式下会被拒绝）。授权保存在本安装（服务器）上，对该安装的所有用户生效，不是按用户隔离。`,
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

  const revoke = async (asset: Asset) => {
    const scope = (grants[asset.id]?.kinds ?? []).map((k) => KIND_LABEL[k]).join("、");
    const confirmed = await ask(
      `撤销「${asset.name}」的设备授权？\n\n撤销后，AI 在该设备上的${scope || "终端写入、命令执行"}不再享受长期授权，按当前权限模式处理：读写模式下恢复逐次确认，完全静默模式下直接执行不逐次问，只读与无人值守模式下会被拒绝。授权记录会从本安装（服务器）上删除。`,
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
        设备长期授权默认全部关闭。开启后，AI 在对应设备上原本要逐次确认的终端写入或命令执行不再逐次确认；只读模式下这两类常规操作也会被放开。授权不覆盖拦截规则命中或拿不准的操作：读写与完全静默下仍会问你，只读下仍会被拒绝。授权保存在本安装（服务器）上，对该安装的所有用户生效，不是按用户隔离，请只在你信任的设备上开启。
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

      {assets?.map((asset) => {
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
              <div className="mt-0.5 text-[10.5px] text-emerald-400/90">
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
                <span className="self-center text-[10px] text-neutral-600">未授权</span>
              </div>
            )}
          </div>
        );
      })}

      {!loadError && assets !== null && assets.length === 0 && (
        <div className="py-1 text-[11px] text-neutral-500">暂无资产，添加资产后可在此管理授权。</div>
      )}
    </div>
  );
}
