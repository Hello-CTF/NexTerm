// 连接诊断 card of a managed device's detail page: how the client reaches the device (relay or direct) and why.
// It reads on first show and on refresh, and again when the device's path changes. Desktop and mobile only.

import { useCallback, useEffect, useRef, useState } from "react";
import { fleetPathApi, type DeviceP2PDiagnostics } from "../../ipc/fleetPathApi";
import { describeError } from "../../ui/errorText";
import { IconNetwork, IconRefresh } from "../../ui/icons";
import { HostSection, SectionHint } from "../hosts/HostSections";
import { formatTime } from "./format";
import { connectionModeLabel, devicePathLabel, devicePathReasonText } from "./devicePath";
import { t } from "../../i18n";

const MAX_SWITCHES = 5;

function natLabel(nat: string): string {
  switch (nat) {
    case "independent":
      return t("端点无关映射");
    case "dependent":
      return t("端点相关映射");
    case "blocked":
      return t("UDP 受阻");
    default:
      return t("未知");
  }
}

function ms(value: number | null | undefined): string {
  return value == null ? "—" : `${Math.round(value)} ms`;
}

export function DeviceDiagnosticsSection({ deviceId, visible }: { deviceId: string; visible: boolean }) {
  const [data, setData] = useState<DeviceP2PDiagnostics | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const request = useRef(0);
  const shown = useRef(false);

  const load = useCallback(async () => {
    const id = ++request.current;
    setLoading(true);
    try {
      const next = await fleetPathApi.diagnostics(deviceId);
      if (request.current !== id) return;
      setData(next);
      setError(null);
    } catch (cause) {
      if (request.current === id) setError(describeError(cause));
    } finally {
      if (request.current === id) setLoading(false);
    }
  }, [deviceId]);

  useEffect(() => {
    if (!visible || shown.current) return;
    shown.current = true;
    void load();
  }, [visible, load]);

  useEffect(() => {
    if (!visible) return;
    let cancelled = false;
    let off: (() => void) | undefined;
    void fleetPathApi
      .onPathChange((status) => {
        if (status.deviceId === deviceId) void load();
      })
      .then((unlisten) => {
        if (cancelled) unlisten();
        else off = unlisten;
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
      off?.();
    };
  }, [visible, deviceId, load]);

  useEffect(
    () => () => {
      request.current += 1;
    },
    [],
  );

  const status = data?.status;
  const reason = status && status.path !== "direct" ? devicePathReasonText(status.reason) : "";
  const rows: { label: string; value: string }[] = data && status
    ? [
        { label: t("当前路径"), value: reason ? `${devicePathLabel(status.path)}（${reason}）` : devicePathLabel(status.path) },
        { label: t("连接方式"), value: connectionModeLabel(status.mode) },
        { label: t("本机 NAT"), value: natLabel(data.localNat) },
        { label: t("设备 NAT"), value: natLabel(data.remoteNat) },
        { label: "STUN", value: data.stunReachable ? t("可达") : t("不可达") },
        { label: t("丢包"), value: data.lossPct == null ? "—" : `${data.lossPct.toFixed(1)}%` },
        { label: t("直连 RTT"), value: ms(data.directRttMs) },
        { label: t("中转 RTT"), value: ms(data.relayRttMs) },
      ]
    : [];
  const pair = data?.selectedPair;
  const switches = data ? [...data.recentSwitches].sort((a, b) => b.at - a.at).slice(0, MAX_SWITCHES) : [];
  const policy = data?.policy;

  return (
    <HostSection
      title={t("连接诊断")}
      icon={<IconNetwork size={14} />}
      testId="host-path-diagnostics"
      actions={
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={loading} onClick={() => void load()}>
          <IconRefresh size={11} className={loading ? "animate-spin" : ""} />
          {t("刷新")}
        </button>
      }
    >
      {error && <p className="m-0 text-[12px] text-red-300">{t("读取失败：{error}", { error })}</p>}
      {!data ? (
        !error && <SectionHint>{t("正在读取…")}</SectionHint>
      ) : (
        <>
          <dl className="m-0 grid grid-cols-2 gap-x-4 gap-y-1.5 text-[12px]">
            {rows.map((row) => (
              <div key={row.label} className="min-w-0">
                <dt className="nx-hint text-[11px]">{row.label}</dt>
                <dd className="m-0 break-words font-mono text-neutral-100">{row.value}</dd>
              </div>
            ))}
            <div className="col-span-2 min-w-0">
              <dt className="nx-hint text-[11px]">{t("选中的候选对")}</dt>
              <dd className="m-0 break-all font-mono text-neutral-100">
                {pair ? `${pair.local} ↔ ${pair.remote}${pair.type ? ` · ${pair.type}` : ""}` : "—"}
              </dd>
            </div>
            {policy && (
              <div className="col-span-2 min-w-0">
                <dt className="nx-hint text-[11px]">{t("策略")}</dt>
                <dd className="m-0 break-words text-neutral-100">
                  {[
                    policy.server ? t("服务器允许直连") : t("服务器已禁用直连"),
                    policy.device ? t("设备允许直连") : t("设备不允许直连"),
                    t("连接方式：{mode}", { mode: connectionModeLabel(policy.mode) }),
                  ].join(" · ")}
                </dd>
              </div>
            )}
          </dl>
          <div className="flex min-w-0 flex-col gap-0.5 text-[12px]">
            <span className="nx-hint text-[11px]">{t("最近切换")}</span>
            {switches.length === 0 ? (
              <SectionHint>{t("暂无切换记录")}</SectionHint>
            ) : (
              <ul className="m-0 flex list-none flex-col gap-0.5 p-0" aria-label={t("最近切换")}>
                {switches.map((item) => {
                  const why = devicePathReasonText(item.reason);
                  return (
                    <li key={`${item.at}-${item.from}-${item.to}`} className="flex min-w-0 flex-wrap items-baseline gap-x-2 text-neutral-300">
                      <span className="shrink-0 font-mono text-[11px] text-neutral-500">{formatTime(item.at)}</span>
                      <span className="shrink-0">{`${devicePathLabel(item.from)} → ${devicePathLabel(item.to)}`}</span>
                      {why && <span className="nx-hint min-w-0 [overflow-wrap:anywhere]">{why}</span>}
                    </li>
                  );
                })}
              </ul>
            )}
          </div>
        </>
      )}
    </HostSection>
  );
}
