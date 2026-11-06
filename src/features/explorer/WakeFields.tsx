import { useId } from "react";
import type { WakeErrors, WakeOptions } from "./wakeOptions";
import { DEFAULT_WAKE_BROADCAST, DEFAULT_WAKE_PORT } from "./wakeOptions";

// The Wake-on-LAN part of the asset form: an optional MAC and, for hosts on another subnet, the broadcast address.
export function WakeFields({
  values,
  errors,
  onChange,
}: {
  values: WakeOptions;
  errors: WakeErrors;
  onChange: (patch: Partial<WakeOptions>) => void;
}) {
  const macErrorId = useId();
  const broadcastErrorId = useId();
  const portErrorId = useId();
  return (
    <div className="nx-form-row">
      <label className="nx-label">唤醒 MAC 地址</label>
      <input
        className="nx-input"
        aria-label="唤醒 MAC 地址"
        aria-invalid={errors.mac ? true : undefined}
        aria-describedby={errors.mac ? macErrorId : undefined}
        value={values.mac}
        onChange={(event) => onChange({ mac: event.target.value })}
        placeholder="可选，如 AA:BB:CC:DD:EE:FF"
        spellCheck={false}
        autoCapitalize="off"
        autoCorrect="off"
      />
      {errors.mac && (
        <p id={macErrorId} role="alert" className="mt-1 text-[11px] text-red-400">
          {errors.mac}
        </p>
      )}
      {values.mac.trim() && (
        <div className="mt-1.5 flex flex-wrap items-center gap-2">
          <input
            className="nx-input w-[160px]"
            aria-label="唤醒广播地址"
            aria-invalid={errors.broadcast ? true : undefined}
            aria-describedby={errors.broadcast ? broadcastErrorId : undefined}
            value={values.broadcast}
            onChange={(event) => onChange({ broadcast: event.target.value })}
            placeholder={DEFAULT_WAKE_BROADCAST}
            spellCheck={false}
          />
          <input
            className="nx-input w-[88px]"
            aria-label="唤醒端口"
            aria-invalid={errors.port ? true : undefined}
            aria-describedby={errors.port ? portErrorId : undefined}
            inputMode="numeric"
            value={values.port}
            onChange={(event) => onChange({ port: event.target.value })}
            placeholder={String(DEFAULT_WAKE_PORT)}
          />
        </div>
      )}
      {errors.broadcast && (
        <p id={broadcastErrorId} role="alert" className="mt-1 text-[11px] text-red-400">
          {errors.broadcast}
        </p>
      )}
      {errors.port && (
        <p id={portErrorId} role="alert" className="mt-1 text-[11px] text-red-400">
          {errors.port}
        </p>
      )}
      <p className="nx-hint mt-1">
        填写后可在右键菜单「唤醒」；跨子网时填写该子网的广播地址，主机需在 BIOS 和网卡中启用 Wake-on-LAN。
      </p>
    </div>
  );
}
