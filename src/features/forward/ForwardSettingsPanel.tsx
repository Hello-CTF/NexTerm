import { useEffect, useRef, useState } from "react";
import { describeError } from "../../ui/errorText";
import {
  DEFAULT_FORWARD_RECOVERY_SETTINGS,
  isAutoStartEligible,
  useForwardSettingsApi,
  type ForwardRecoverySettings,
  type ForwardRuleSettings,
  type ForwardSettingsApi,
} from "./forwardSettingsApi";

interface SettingsSnapshot {
  api: ForwardSettingsApi;
  rules: ForwardRuleSettings[];
  recovery: ForwardRecoverySettings;
  error: string | null;
}

// The panel isolates every read and write by API/account generation: a new
// generation clears the previous snapshot immediately (old controls stay
// hidden and disabled until the new generation loads), and late completions
// only land when their originating generation is still current.
export function ForwardSettingsPanel() {
  const api = useForwardSettingsApi();
  const [snapshot, setSnapshot] = useState<SettingsSnapshot | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const generationRef = useRef(0);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      generationRef.current += 1;
    };
  }, []);

  useEffect(() => {
    generationRef.current += 1;
    const generation = generationRef.current;
    setSnapshot(null);
    setBusy(null);
    void (async () => {
      try {
        const [listed, settings] = await Promise.all([api.listRules(), api.getRecoverySettings()]);
        if (generationRef.current !== generation || !mountedRef.current) return;
        setSnapshot({ api, rules: listed, recovery: settings, error: null });
      } catch (e) {
        if (generationRef.current !== generation || !mountedRef.current) return;
        setSnapshot({ api, rules: [], recovery: DEFAULT_FORWARD_RECOVERY_SETTINGS, error: describeError(e) });
      }
    })();
  }, [api]);

  const loaded = snapshot !== null && snapshot.api === api;
  const rules = loaded ? snapshot.rules : null;
  const recovery = loaded ? snapshot.recovery : null;
  const error = loaded ? snapshot.error : null;

  const toggleAutoStart = async (rule: ForwardRuleSettings, next: boolean) => {
    if (!isAutoStartEligible(rule)) return;
    const generation = generationRef.current;
    setBusy(rule.id);
    try {
      await api.setRuleAutoStart(rule.id, next);
      if (generationRef.current !== generation || !mountedRef.current) return;
      setSnapshot((current) =>
        current && current.api === api
          ? { ...current, rules: current.rules.map((r) => (r.id === rule.id ? { ...r, autoStart: next } : r)) }
          : current,
      );
    } catch (e) {
      if (generationRef.current !== generation || !mountedRef.current) return;
      setSnapshot((current) => (current && current.api === api ? { ...current, error: describeError(e) } : current));
    } finally {
      if (mountedRef.current) setBusy(null);
    }
  };

  const toggleRecovery = async (enabled: boolean) => {
    if (recovery === null) return;
    const generation = generationRef.current;
    setBusy("recovery");
    try {
      await api.setRecoverySettings({ ...recovery, enabled });
      if (generationRef.current !== generation || !mountedRef.current) return;
      setSnapshot((current) =>
        current && current.api === api ? { ...current, recovery: { ...current.recovery, enabled } } : current,
      );
    } catch (e) {
      if (generationRef.current !== generation || !mountedRef.current) return;
      setSnapshot((current) => (current && current.api === api ? { ...current, error: describeError(e) } : current));
    } finally {
      if (mountedRef.current) setBusy(null);
    }
  };

  const recoverNow = async () => {
    if (recovery === null || !recovery.enabled) return;
    const generation = generationRef.current;
    setBusy("recover");
    try {
      await api.recoverNow();
      if (generationRef.current !== generation || !mountedRef.current) return;
    } catch (e) {
      if (generationRef.current !== generation || !mountedRef.current) return;
      setSnapshot((current) => (current && current.api === api ? { ...current, error: describeError(e) } : current));
    } finally {
      if (mountedRef.current) setBusy(null);
    }
  };

  return (
    <section aria-label="转发设置" className="flex flex-col gap-3 text-[12px]">
      <div>
        <div className="mb-1 font-medium text-neutral-200">自动启动规则</div>
        <div className="nx-hint mb-2">
          仅本地固定端口转发，只监听 127.0.0.1，不会对外暴露；SOCKS5、远程转发和公共监听不支持自动启动。
        </div>
        {rules === null ? (
          <div role="status" className="text-[11px] text-neutral-500">加载中…</div>
        ) : rules.length === 0 ? (
          <div className="text-[11px] text-neutral-500">还没有保存的转发规则</div>
        ) : (
          <div className="flex flex-col gap-1.5">
            {rules.map((rule) => (
              <div
                key={rule.id}
                className="flex flex-wrap items-center gap-2 rounded border border-neutral-800/70 px-2 py-1.5 text-[11px]"
              >
                <span className="min-w-0 flex-1 truncate font-medium text-neutral-200" title={rule.name}>
                  {rule.name}
                </span>
                <span className="nx-mono text-neutral-500">
                  {rule.listenHost}:{rule.listenPort} → {rule.targetHost}:{rule.targetPort}
                </span>
                {isAutoStartEligible(rule) ? (
                  <button
                    type="button"
                    role="switch"
                    aria-checked={rule.autoStart}
                    aria-label={`自动启动 ${rule.name}`}
                    className="nx-btn nx-btn-ghost nx-btn-xs"
                    disabled={busy !== null}
                    onClick={() => void toggleAutoStart(rule, !rule.autoStart)}
                  >
                    {rule.autoStart ? "已开启" : "已关闭"}
                  </button>
                ) : (
                  <span className="text-[11px] text-neutral-500">仅本地 127.0.0.1 规则支持自动启动</span>
                )}
              </div>
            ))}
          </div>
        )}
      </div>

      <div>
        <div className="mb-1 font-medium text-neutral-200">启动恢复</div>
        <div className="mb-2 flex items-center gap-2">
          <button
            type="button"
            role="switch"
            aria-checked={recovery?.enabled ?? false}
            aria-label="启动时自动恢复转发"
            className="nx-btn nx-btn-ghost nx-btn-xs"
            disabled={busy !== null || recovery === null}
            onClick={() => void toggleRecovery(!(recovery?.enabled ?? false))}
          >
            {recovery?.enabled ? "已开启" : "已关闭"}
          </button>
          <span className="text-[11px] text-neutral-500">默认关闭</span>
        </div>
        <ul className="nx-hint list-inside list-disc space-y-0.5">
          <li>仅恢复标记为自动启动的规则</li>
          <li>最多同时建立 {recovery?.maxConcurrentConnections ?? DEFAULT_FORWARD_RECOVERY_SETTINGS.maxConcurrentConnections} 个设备连接</li>
          <li>自动跳过生产项目资产</li>
        </ul>
        {recovery?.enabled && (
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-xs mt-2"
            disabled={busy !== null}
            onClick={() => void recoverNow()}
          >
            立即恢复
          </button>
        )}
      </div>

      {error && (
        <div role="alert" className="text-[11px] text-red-300">
          {error}
        </div>
      )}
    </section>
  );
}
