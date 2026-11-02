import { useId } from "react";
import { Select } from "../../ui/Select";
import type { RemoteDesktopOptions } from "./assetKindFields";

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
              value={values.resolution}
              onChange={(value) => onChange({ resolution: value as "follow" | "fixed" })}
              options={[
                { value: "follow", label: "跟随窗口大小" },
                { value: "fixed", label: "固定分辨率" },
              ]}
            />
            {values.resolution === "fixed" && (
              <div className="mt-1.5 flex items-center gap-2">
                <input
                  id={widthId}
                  type="number"
                  className="nx-input w-[96px]"
                  aria-label="宽度"
                  aria-invalid={resolutionError ? true : undefined}
                  aria-describedby={resolutionError ? errorId : undefined}
                  value={values.width}
                  onChange={(event) => onChange({ width: Number(event.target.value) })}
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
                  onChange={(event) => onChange({ height: Number(event.target.value) })}
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
