import { useEffect, useState } from "react";
import { describeError } from "../../ui/errorText";
import {
  DEFAULT_FORWARD_RECOVERY_SETTINGS,
  useForwardSettingsApi,
  type ForwardRecoverySettings,
  type ForwardRuleSettings,
} from "./forwardSettingsApi";

export function ForwardSettingsPanel() {
  const api = useForwardSettingsApi();
  const [rules, setRules] = useState<ForwardRuleSettings[] | null>(null);
  const [recovery, setRecovery] = useState<ForwardRecoverySettings>(DEFAULT_FORWARD_RECOVERY_SETTINGS);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const [listed, settings] = await Promise.all([api.listRules(), api.getRecoverySettings()]);
        if (cancelled) return;
        setRules(listed);
        setRecovery(settings);
      } catch (e) {
        if (!cancelled) setError(describeError(e));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [api]);

  const toggleAutoStart = async (rule: ForwardRuleSettings, next: boolean) => {
    setBusy(rule.id);
    setError(null);
    try {
      await api.setRuleAutoStart(rule.id, next);
      setRules((current) => (current ?? []).map((r) => (r.id === rule.id ? { ...r, autoStart: next } : r)));
    } catch (e) {
      setError(describeError(e));
    } finally {
      setBusy(null);
    }
  };

  const toggleRecovery = async (enabled: boolean) => {
    setBusy("recovery");
    setError(null);
    try {
      await api.setRecoverySettings({ ...recovery, enabled });
      setRecovery((current) => ({ ...current, enabled }));
    } catch (e) {
      setError(describeError(e));
    } finally {
      setBusy(null);
    }
  };

  const recoverNow = async () => {
    setBusy("recover");
    setError(null);
    try {
      await api.recoverNow();
    } catch (e) {
      setError(describeError(e));
    } finally {
      setBusy(null);
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
                  127.0.0.1:{rule.listenPort} → {rule.targetHost}:{rule.targetPort}
                </span>
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
            aria-checked={recovery.enabled}
            aria-label="启动时自动恢复转发"
            className="nx-btn nx-btn-ghost nx-btn-xs"
            disabled={busy !== null}
            onClick={() => void toggleRecovery(!recovery.enabled)}
          >
            {recovery.enabled ? "已开启" : "已关闭"}
          </button>
          <span className="text-[11px] text-neutral-500">默认关闭</span>
        </div>
        <ul className="nx-hint list-inside list-disc space-y-0.5">
          <li>仅恢复标记为自动启动的规则</li>
          <li>最多同时建立 {recovery.maxConcurrentConnections} 个设备连接</li>
          <li>自动跳过生产项目资产</li>
        </ul>
        {recovery.enabled && (
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
