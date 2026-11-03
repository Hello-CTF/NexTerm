import { useId, useState } from "react";
import { Select } from "../../ui/Select";
import { RESOLUTION_PRESETS } from "../remote/remoteTypes";
import type { RemoteDesktopOptions } from "./assetKindFields";

const CUSTOM = "custom";

function ToggleRow({
  label,
  hint,
  checked,
  onChange,
}: {
  label: string;
  hint: string;
  checked: boolean;
  onChange: (checked: boolean) => void;
}) {
  return (
    <div className="nx-form-row">
      <label className="flex cursor-pointer items-start gap-3">
        <div className="min-w-0 flex-1">
          <div className="text-[12.5px] text-neutral-200">{label}</div>
          <p className="nx-hint mt-0.5">{hint}</p>
        </div>
        <input
          type="checkbox"
          className="mt-0.5 h-4 w-4 shrink-0"
          aria-label={label}
          checked={checked}
          onChange={(event) => onChange(event.target.checked)}
        />
      </label>
    </div>
  );
}

// The protocol-specific part of the asset form for RDP and VNC.
export function RemoteDesktopFields({
  protocol,
  values,
  onChange,
  resolutionError,
}: {
  protocol: "rdp" | "vnc";
  values: RemoteDesktopOptions;
  onChange: (patch: Partial<RemoteDesktopOptions>) => void;
  resolutionError: string | null;
}) {
  const widthId = useId();
  const heightId = useId();
  const errorId = useId();
  // Picking "自定义" while the size happens to equal a preset must keep the number inputs on screen.
  const preset = RESOLUTION_PRESETS.find((p) => p.width === values.width && p.height === values.height);
  // Sticky: typing a preset's numbers into the inputs must not swap them for the preset mid-edit.
  const [custom, setCustom] = useState(values.resolution === "fixed" && !preset);
  const selected = values.resolution === "follow" ? "follow" : custom || !preset ? CUSTOM : `${preset.width}x${preset.height}`;
  return (
    <>
      {protocol === "rdp" && (
        <>
          <div className="nx-form-row">
            <label className="nx-label">域</label>
            <input
              className="nx-input"
              aria-label="域"
              value={values.domain}
              onChange={(event) => onChange({ domain: event.target.value })}
              placeholder="可选，如 CORP"
            />
          </div>
          <div className="nx-form-row">
            <label className="nx-label">分辨率</label>
            <Select
              ariaLabel="分辨率"
              value={selected}
              onChange={(value) => {
                if (value === "follow") {
                  setCustom(false);
                  onChange({ resolution: "follow" });
                } else if (value === CUSTOM) {
                  setCustom(true);
                  onChange({ resolution: "fixed" });
                } else {
                  const [w, h] = value.split("x").map(Number);
                  setCustom(false);
                  onChange({ resolution: "fixed", width: w, height: h });
                }
              }}
              options={[
                { value: "follow", label: "跟随窗口" },
                ...RESOLUTION_PRESETS.map((p) => ({ value: `${p.width}x${p.height}`, label: `${p.width} x ${p.height}` })),
                { value: CUSTOM, label: "自定义" },
              ]}
            />
            <p className="nx-hint mt-0.5">连接时以此大小启动；服务器支持时，会话中也可在工具栏的“分辨率”里临时调整。</p>
            {selected === CUSTOM && (
              <div className="mt-1.5 flex items-center gap-2">
                <input
                  id={widthId}
                  type="number"
                  className="nx-input w-[96px]"
                  aria-label="宽度"
                  aria-invalid={resolutionError ? true : undefined}
                  aria-describedby={resolutionError ? errorId : undefined}
                  value={values.width}
                  onChange={(event) => {
                    setCustom(true);
                    onChange({ width: Number(event.target.value) });
                  }}
                />
                <span className="text-neutral-500">x</span>
                <input
                  id={heightId}
                  type="number"
                  className="nx-input w-[96px]"
                  aria-label="高度"
                  aria-invalid={resolutionError ? true : undefined}
                  aria-describedby={resolutionError ? errorId : undefined}
                  value={values.height}
                  onChange={(event) => {
                    setCustom(true);
                    onChange({ height: Number(event.target.value) });
                  }}
                />
              </div>
            )}
            {resolutionError && (
              <p id={errorId} role="alert" className="mt-1 text-[11px] text-red-400">
                {resolutionError}
              </p>
            )}
          </div>
        </>
      )}
      <ToggleRow
        label="每次连接时输入密码"
        hint="不保存密码。连接时弹出输入框，密码只用于本次连接。"
        checked={values.promptPassword}
        onChange={(promptPassword) => onChange({ promptPassword })}
      />
      <ToggleRow
        label="启用剪贴板"
        hint="允许在本机与远程桌面之间同步文本剪贴板。"
        checked={values.clipboard}
        onChange={(clipboard) => onChange({ clipboard })}
      />
      {protocol === "vnc" && (
        <ToggleRow
          label="默认只读"
          hint="连接后先以只读方式查看，不发送键盘和鼠标输入；可在工具栏中随时切换。"
          checked={values.viewOnly}
          onChange={(viewOnly) => onChange({ viewOnly })}
        />
      )}
    </>
  );
}
