// 已知主机（SSH 主机密钥）管理：列出 / 撤销信任。
//
// # 为什么只有「列出 / 删除」，没有「添加」
//
// 主机信任的唯一入口是首次连接时的 SSH 指纹确认（Strict 流程：连接触发
// `host_key_pending`，确认框里展示的是**这次连接真实协商出的**指纹，
// 见 store.connectAsset）。这里刻意不提供按指纹手动添加的表单 ——
// 手抄的指纹一旦和真实协商结果对不上，等于亲手给中间人开门，Strict 就形同虚设。
//
// # 删除 = 撤销信任
//
// 删掉一条之后，下次连接这台机器会重新走首次连接的指纹确认。
// 服务器重装 / 更换主机密钥之后，正确做法就是删掉旧条目、重新确认。
import { useCallback, useEffect, useRef, useState } from "react";
import { assetApi } from "../../ipc/commands";
import type { KnownHostDto } from "../../ipc/types";
import { useUi } from "../../app/store";
import { DEMO } from "../../demo";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { IconRefresh, IconShield, IconTrash, IconXCircle } from "../../ui/icons";

export function KnownHostsCard() {
  const { pushToast } = useUi();
  const [hosts, setHosts] = useState<KnownHostDto[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  /** 正在撤销的那一条 id（同时只允许一个，避免连点把状态搞乱）。 */
  const [revokingId, setRevokingId] = useState<string | null>(null);

  // 陈旧完成防护：卸载后、或更新的读取已发出后，旧请求的迟到结果一律丢弃。
  const aliveRef = useRef(true);
  const loadGenRef = useRef(0);
  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
      loadGenRef.current++;
    };
  }, []);

  const reload = useCallback(async () => {
    const gen = ++loadGenRef.current;
    setLoading(true);
    setError(null);
    try {
      const list = await assetApi.knownHostList();
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      setHosts(list);
    } catch (e) {
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      setError(describeError(e));
    } finally {
      if (aliveRef.current && gen === loadGenRef.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (DEMO) return;
    void reload();
  }, [reload]);

  if (DEMO) return null;

  const revoke = async (host: KnownHostDto) => {
    // 确认走应用内 DialogHost（role=alertdialog，焦点受管），不是 window.confirm。
    const ok = await ask(
      `撤销对 ${host.host}:${host.port} 的信任？\n指纹(${host.keyType}): ${host.fingerprint}\n\n撤销后下次连接这台机器会重新弹出指纹确认。`,
      { title: "撤销已知主机", kind: "warning" },
    );
    if (!ok) return;
    setRevokingId(host.id);
    try {
      await assetApi.knownHostRemove(host.id);
      pushToast("success", `已撤销 ${host.host}:${host.port} 的信任`);
      await reload();
    } catch (e) {
      pushToast("error", `撤销失败：${describeError(e)}`);
    } finally {
      if (aliveRef.current) setRevokingId(null);
    }
  };

  return (
    <section className="nx-card">
      <div className="mb-1 flex items-center gap-2">
        <IconShield size={15} className="text-neutral-400" />
        <span className="nx-card-title">已知主机</span>
        {hosts && <span className="nx-badge">{hosts.length} 台</span>}
        <div className="nx-spacer" />
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void reload()}>
          <IconRefresh size={11} className={loading ? "animate-spin" : undefined} />
          刷新
        </button>
      </div>
      <p className="nx-hint mb-3.5">
        首次连接一台 SSH 服务器、指纹确认通过后就会记在这里。删除一条 = 撤销对它的信任：
        下次连接会重新弹出指纹确认（服务器重装或更换主机密钥后，删掉旧条目重新确认即可）。
      </p>

      {hosts === null ? (
        error ? (
          <div className="nx-alert nx-alert-danger flex items-start gap-2">
            <IconXCircle size={13} className="mt-0.5 shrink-0" />
            <div className="min-w-0 flex-1">
              <div className="break-words">{error}</div>
              <button className="nx-btn nx-btn-outline nx-btn-sm mt-2" onClick={() => void reload()}>
                <IconRefresh size={11} />
                重试
              </button>
            </div>
          </div>
        ) : (
          <div className="nx-hint py-2 text-[12px]">读取中…</div>
        )
      ) : hosts.length === 0 ? (
        <div className="nx-hint py-2 text-[12px]">还没有信任过任何主机。</div>
      ) : (
        <div className="flex flex-col gap-1">
          {error && (
            <div className="nx-alert nx-alert-danger mb-1 flex items-center gap-2 text-[12px]">
              <IconXCircle size={13} className="shrink-0" />
              <span className="min-w-0 break-words">{error}</span>
            </div>
          )}
          {hosts.map((h) => (
            <div key={h.id} className="flex items-center gap-2">
              <div className="min-w-0 flex-1">
                <div className="text-[12.5px] text-neutral-200">
                  {h.host}:{h.port}
                  <span className="nx-hint ml-2">{h.keyType}</span>
                </div>
                <div className="truncate font-mono text-[11px] text-neutral-500" title={h.fingerprint}>
                  {h.fingerprint}
                </div>
              </div>
              <button
                className="nx-btn nx-btn-outline nx-btn-sm shrink-0"
                disabled={revokingId !== null}
                title={`撤销对 ${h.host}:${h.port} 的信任`}
                onClick={() => void revoke(h)}
              >
                {revokingId === h.id ? (
                  <IconRefresh size={11} className="animate-spin" />
                ) : (
                  <IconTrash size={11} />
                )}
                撤销信任
              </button>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}
