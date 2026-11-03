import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  fleetAlertsClient,
  isForbiddenError,
  type FleetAlertSettings,
  type FleetAlertSettingsInput,
} from "../../ipc/fleetAlertsApi";
import type { FleetDevice } from "../../ipc/fleetApi";
import { describeError } from "../../ui/errorText";
import { useUi } from "../../app/store";
import { clockSkewText, deviceActiveAlerts, ruleName, RULE_SUSTAIN_MINUTES, useFleetAlerts } from "./deviceAlerts";
import { formatTime } from "./format";

const FORBIDDEN_TEXT = "仅设备所有者可修改告警设置";

interface Form {
  disk_enabled: boolean;
  disk_threshold: string;
  mem_enabled: boolean;
  mem_threshold: string;
  cpu_enabled: boolean;
  cpu_threshold: string;
  offline_enabled: boolean;
  offline_minutes: string;
  expected_offline: boolean;
  quiet_enabled: boolean;
  quiet_start: string;
  quiet_end: string;
  quiet_tz: string;
}

function minuteToTime(minute: number): string {
  const m = Math.min(1439, Math.max(0, Math.floor(minute)));
  return `${String(Math.floor(m / 60)).padStart(2, "0")}:${String(m % 60).padStart(2, "0")}`;
}

function timeToMinute(value: string): number | null {
  const match = /^(\d{1,2}):(\d{2})$/.exec(value);
  if (!match) return null;
  const hours = Number(match[1]);
  const minutes = Number(match[2]);
  return hours < 24 && minutes < 60 ? hours * 60 + minutes : null;
}

function toForm(s: FleetAlertSettings): Form {
  return {
    disk_enabled: s.disk_enabled,
    disk_threshold: String(s.disk_threshold),
    mem_enabled: s.mem_enabled,
    mem_threshold: String(s.mem_threshold),
    cpu_enabled: s.cpu_enabled,
    cpu_threshold: String(s.cpu_threshold),
    offline_enabled: s.offline_enabled,
    offline_minutes: String(s.offline_minutes),
    expected_offline: s.expected_offline,
    quiet_enabled: s.quiet_enabled,
    quiet_start: minuteToTime(s.quiet_start),
    quiet_end: minuteToTime(s.quiet_end),
    quiet_tz: s.quiet_tz,
  };
}

function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "";
  } catch {
    return "";
  }
}

function inRange(text: string, min: number, max: number, integer: boolean): number | null {
  if (text.trim() === "") return null;
  const value = Number(text);
  if (!Number.isFinite(value) || value < min || value > max) return null;
  if (integer && !Number.isInteger(value)) return null;
  return value;
}

// parseForm returns the PUT body, or the first problem found.
function parseForm(form: Form): { input: FleetAlertSettingsInput } | { error: string } {
  const disk = inRange(form.disk_threshold, 1, 100, false);
  const mem = inRange(form.mem_threshold, 1, 100, false);
  const cpu = inRange(form.cpu_threshold, 1, 100, false);
  if (disk === null || mem === null || cpu === null) return { error: "阈值需在 1 到 100 之间" };
  const offline = inRange(form.offline_minutes, 1, 1440, true);
  if (offline === null) return { error: "离线分钟数需为 1 到 1440 的整数" };
  const start = timeToMinute(form.quiet_start);
  const end = timeToMinute(form.quiet_end);
  if (start === null || end === null) return { error: "请填写静音开始和结束时间" };
  if (form.quiet_enabled && start === end) return { error: "静音开始和结束时间不能相同" };
  return {
    input: {
      disk_enabled: form.disk_enabled,
      disk_threshold: disk,
      mem_enabled: form.mem_enabled,
      mem_threshold: mem,
      cpu_enabled: form.cpu_enabled,
      cpu_threshold: cpu,
      offline_enabled: form.offline_enabled,
      offline_minutes: offline,
      expected_offline: form.expected_offline,
      quiet_enabled: form.quiet_enabled,
      quiet_start: start,
      quiet_end: end,
      quiet_tz: form.quiet_enabled && form.quiet_tz === "" ? browserTimeZone() : form.quiet_tz,
    },
  };
}

const THRESHOLD_RULES = [
  { rule: "disk", label: "磁盘使用率", enabled: "disk_enabled", threshold: "disk_threshold" },
  { rule: "mem", label: "内存使用率", enabled: "mem_enabled", threshold: "mem_threshold" },
  { rule: "cpu", label: "CPU 使用率", enabled: "cpu_enabled", threshold: "cpu_threshold" },
] as const;

export function DeviceAlertsCard({ device }: { device: FleetDevice }) {
  const pushToast = useUi((s) => s.pushToast);
  const qc = useQueryClient();
  const alerts = useFleetAlerts();
  const settingsKey = ["fleet-alert-settings", device.id];
  const query = useQuery({
    queryKey: settingsKey,
    queryFn: () => fleetAlertsClient().getSettings(device.id),
    retry: false,
    refetchOnWindowFocus: false,
  });
  const [form, setForm] = useState<Form | null>(null);
  const [saving, setSaving] = useState(false);
  const [forbidden, setForbidden] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const data = query.data;
  useEffect(() => {
    if (data) setForm(toForm(data));
  }, [data]);
  useEffect(() => {
    if (query.error && isForbiddenError(query.error)) setForbidden(true);
  }, [query.error]);

  const active = deviceActiveAlerts(alerts.data, device.id);
  const set = <K extends keyof Form>(key: K, value: Form[K]) => {
    setError(null);
    setForm((prev) => (prev ? { ...prev, [key]: value } : prev));
  };
  const toggleQuiet = (enabled: boolean) => {
    setError(null);
    setForm((prev) => {
      if (!prev) return prev;
      const tz = prev.quiet_tz || (enabled ? browserTimeZone() : "");
      return { ...prev, quiet_enabled: enabled, quiet_tz: tz };
    });
  };

  const parsed = form ? parseForm(form) : null;
  const disabled = forbidden || saving;
  const save = async () => {
    if (!form || !parsed) return;
    if ("error" in parsed) {
      setError(parsed.error);
      return;
    }
    setSaving(true);
    setError(null);
    try {
      const next = await fleetAlertsClient().putSettings(device.id, parsed.input);
      qc.setQueryData(settingsKey, next);
      setForm(toForm(next));
      pushToast("success", "告警设置已保存");
    } catch (e) {
      if (isForbiddenError(e)) setForbidden(true);
      else setError(describeError(e));
    } finally {
      setSaving(false);
    }
  };

  const tzShown = form ? form.quiet_tz || data?.server_tz || "服务器时区" : "";
  const suggestQuiet =
    form !== null &&
    !form.quiet_enabled &&
    (device.kind === "local-helper" || ["darwin", "windows"].includes((device.agent?.platform ?? "").toLowerCase()));

  return (
    <div className="flex min-w-0 flex-col gap-3" data-testid="device-alerts-card">
      {active.length > 0 && (
        <ul className="m-0 flex list-none flex-col gap-1 p-0" aria-label="当前活动告警">
          {active.map((alert) => (
            <li key={`${alert.rule}`} className="text-[12px] text-amber-300" data-testid="device-active-alert">
              {ruleName(alert.rule)}告警 · 自 {formatTime(alert.since)}
              {alert.value !== undefined && alert.rule !== "offline" ? ` · 当前 ${Math.round(alert.value * 10) / 10}%` : ""}
            </li>
          ))}
        </ul>
      )}
      {forbidden && (
        <p className="nx-hint m-0 text-[12px]" role="note">
          {FORBIDDEN_TEXT}
        </p>
      )}
      {clockSkewText(data?.clock_skew_ms) !== "" && (
        <p className="nx-hint m-0 text-[12px]" role="note" data-testid="device-clock-skew">
          {clockSkewText(data?.clock_skew_ms)}
        </p>
      )}
      {!form && !forbidden && (
        <p className="nx-hint m-0 text-[12px]">{query.isError ? `加载告警设置失败：${describeError(query.error)}` : "正在加载告警设置…"}</p>
      )}
      {form && (
        <>
          {THRESHOLD_RULES.map((r) => (
            <div key={r.rule} className="flex flex-wrap items-center gap-2 text-[12.5px]">
              <label className="inline-flex items-center gap-2">
                <input
                  type="checkbox"
                  aria-label={`${r.label}告警`}
                  checked={form[r.enabled]}
                  disabled={disabled}
                  onChange={(e) => set(r.enabled, e.target.checked)}
                />
                <span className="text-neutral-200">{r.label}超过</span>
              </label>
              <input
                type="number"
                min={1}
                max={100}
                className="nx-input nx-input-sm w-20"
                aria-label={`${r.label}阈值`}
                value={form[r.threshold]}
                disabled={disabled || !form[r.enabled]}
                onChange={(e) => set(r.threshold, e.target.value)}
              />
              <span className="text-neutral-400">%</span>
              <span className="nx-hint">持续 {RULE_SUSTAIN_MINUTES[r.rule]} 分钟</span>
            </div>
          ))}
          <div className="flex flex-col gap-1">
            <div className="flex flex-wrap items-center gap-2 text-[12.5px]">
              <label className="inline-flex items-center gap-2">
                <input
                  type="checkbox"
                  aria-label="离线告警"
                  checked={form.offline_enabled}
                  disabled={disabled}
                  onChange={(e) => set("offline_enabled", e.target.checked)}
                />
                <span className="text-neutral-200">离线满</span>
              </label>
              <input
                type="number"
                min={1}
                max={1440}
                className="nx-input nx-input-sm w-20"
                aria-label="离线分钟数"
                value={form.offline_minutes}
                disabled={disabled || !form.offline_enabled}
                onChange={(e) => set("offline_minutes", e.target.value)}
              />
              <span className="text-neutral-400">分钟告警</span>
            </div>
            <p className="nx-hint m-0">
              连续约 {data?.suspect_after_minutes ?? 5} 分钟未收到上报视为疑似离线，满 {form.offline_minutes || "X"} 分钟告警。
            </p>
          </div>
          <label className="inline-flex items-center gap-2 text-[12.5px]">
            <input
              type="checkbox"
              aria-label="预期离线"
              checked={form.expected_offline}
              disabled={disabled}
              onChange={(e) => set("expected_offline", e.target.checked)}
            />
            <span className="text-neutral-200">预期离线</span>
            <span className="nx-hint">离线时只留静音记录，不发告警</span>
          </label>
          <div className="flex flex-col gap-1">
            <div className="flex flex-wrap items-center gap-2 text-[12.5px]">
              <label className="inline-flex items-center gap-2">
                <input
                  type="checkbox"
                  aria-label="静音时段"
                  checked={form.quiet_enabled}
                  disabled={disabled}
                  onChange={(e) => toggleQuiet(e.target.checked)}
                />
                <span className="text-neutral-200">静音时段</span>
              </label>
              <input
                type="time"
                className="nx-input nx-input-sm"
                aria-label="静音开始时间"
                value={form.quiet_start}
                disabled={disabled || !form.quiet_enabled}
                onChange={(e) => set("quiet_start", e.target.value)}
              />
              <span className="text-neutral-400">至</span>
              <input
                type="time"
                className="nx-input nx-input-sm"
                aria-label="静音结束时间"
                value={form.quiet_end}
                disabled={disabled || !form.quiet_enabled}
                onChange={(e) => set("quiet_end", e.target.value)}
              />
            </div>
            <p className="nx-hint m-0">静音时段内离线不发告警，按 {tzShown} 计算，跨午夜有效。</p>
            {suggestQuiet && <p className="nx-hint m-0" data-testid="quiet-suggestion">笔记本或开发机建议开启静音时段（例如 19:00-09:00）</p>}
          </div>
          {error && (
            <p className="m-0 text-[12px] text-red-400" role="alert">
              {error}
            </p>
          )}
          <div>
            <button type="button" className="nx-btn nx-btn-primary nx-btn-sm" disabled={disabled} onClick={() => void save()}>
              {saving ? "正在保存…" : "保存"}
            </button>
          </div>
        </>
      )}
    </div>
  );
}
