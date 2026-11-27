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

function serverPolicyLabel(state: string): string {
  if (state === "on") return t("已开启");
  if (state === "off") return t("已关闭");
  return t("未知");
}

function devicePolicyLabel(state: string, key: string): string {
  if (state === "on") return t("已启用");
  if (state === "off") return key === "pending" ? t("密钥待确认") : t("未注册直连密钥");
  return t("未知");
}

interface Verdict {
  // text is the one line on top: the current path and the first cause that keeps it off a direct connection.
  text: string;
  // mode is the stored connection mode, noting when a policy layer overrides it.
  mode: string;
}

// pathVerdict names the first blocking cause in the order the core checks them: the server switch, the device
// key, the user's relay choice, then whatever the last attempt reported. A layer that could not be read is never
// named as a cause.
function pathVerdict(data: DeviceP2PDiagnostics): Verdict {
  const { status, policy } = data;
  const path = devicePathLabel(status.path);
  const modeLabel = connectionModeLabel(status.mode);
  if (status.path !== "relay") return { text: t("当前：{path}", { path }), mode: modeLabel };
  const overridable = status.mode !== "relay";
  if (policy.server === "off") {
    return {
      text: t("当前：{path} — {cause}", { path, cause: t("服务器已关闭直连") }),
      mode: overridable ? t("{mode}（服务器未开启直连，暂用中转）", { mode: modeLabel }) : modeLabel,
    };
  }
  if (policy.device === "off") {
    const cause = policy.deviceKey === "pending" ? t("设备直连密钥待确认") : t("设备未启用直连（代理未注册直连密钥）");
    return {
      text: t("当前：{path} — {cause}", { path, cause }),
      mode: overridable ? t("{mode}（设备未启用直连，暂用中转）", { mode: modeLabel }) : modeLabel,
    };
  }
  // A refused grant is reported as server_disabled; with the server switch on it was the grant, not the switch.
  const cause =
    status.mode !== "relay" && status.reason === "server_disabled" && policy.server === "on"
      ? t("服务器拒绝了直连授权")
      : devicePathReasonText(status.mode === "relay" ? "mode_relay" : status.reason);
  return { text: cause ? t("当前：{path} — {cause}", { path, cause }) : t("当前：{path}", { path }), mode: modeLabel };
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

  const verdict = data ? pathVerdict(data) : null;
  const pair = data?.selectedPair;
  const switches = data ? [...data.recentSwitches].sort((a, b) => b.at - a.at).slice(0, MAX_SWITCHES) : [];
  const probed = data?.probed === true;
  const details: { label: string; value: string }[] = data
    ? [
        { label: t("服务器直连"), value: serverPolicyLabel(data.policy.server) },
        { label: t("设备直连"), value: devicePolicyLabel(data.policy.device, data.policy.deviceKey) },
        { label: t("本机 NAT"), value: probed ? natLabel(data.localNat) : t("未检测") },
        { label: t("设备 NAT"), value: probed ? natLabel(data.remoteNat) : t("未检测") },
        { label: "STUN", value: probed ? (data.stunReachable ? t("可达") : t("不可达")) : t("未检测") },
        { label: t("丢包"), value: data.lossPct == null ? "—" : `${data.lossPct.toFixed(1)}%` },
        { label: t("直连 RTT"), value: ms(data.directRttMs) },
        { label: t("中转 RTT"), value: ms(data.relayRttMs) },
      ]
    : [];

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
          <p className="m-0 break-words text-[12px] text-neutral-100" data-testid="host-path-verdict">
            {verdict?.text}
          </p>
          <dl className="m-0 text-[12px]">
            <div className="min-w-0">
              <dt className="nx-hint text-[11px]">{t("连接方式")}</dt>
              <dd className="m-0 break-words text-neutral-100">{verdict?.mode}</dd>
            </div>
          </dl>
          <SectionHint>{t("中转经服务器转发，始终可用；直连为点对点，延迟更低，需服务器与设备都支持。")}</SectionHint>
          <details className="min-w-0 text-[12px]">
            <summary className="nx-hint cursor-pointer select-none text-[11px]">{t("详细信息")}</summary>
            <dl className="m-0 mt-1.5 grid grid-cols-2 gap-x-4 gap-y-1.5">
              {details.map((row) => (
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
            </dl>
            <div className="mt-1.5 flex min-w-0 flex-col gap-0.5">
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
          </details>
        </>
      )}
    </HostSection>
  );
}
