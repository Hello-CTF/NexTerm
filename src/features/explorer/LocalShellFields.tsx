import { useEffect, useId, useState } from "react";
import { localShellApi, type LocalShellProfile } from "../../ipc/commands";
import { Select } from "../../ui/Select";
import { t } from "../../i18n";
import { formatShellArgs, parseShellArgs } from "./shellArgs";

const DEFAULT_CHOICE = "";
const CUSTOM_CHOICE = "__custom__";

function profileChoice(profiles: readonly LocalShellProfile[], shell: string, args: string): string {
  const path = shell.trim();
  if (!path && !args.trim()) return DEFAULT_CHOICE;
  const parsed = parseShellArgs(args);
  const match = profiles.find(
    (profile) => profile.path === path && profile.args.length === parsed.length && profile.args.every((arg, index) => arg === parsed[index]),
  );
  return match ? match.id : CUSTOM_CHOICE;
}

// The shell part of a local asset: a picker of the shells found on the host that runs local terminals, which fills
// the executable and its start arguments, and both fields as free text for anything the picker does not list.
export function LocalShellFields({
  shell,
  args,
  argsError,
  onShellChange,
  onArgsChange,
}: {
  shell: string;
  args: string;
  argsError: string | null;
  onShellChange: (value: string) => void;
  onArgsChange: (value: string) => void;
}) {
  const argsErrorId = useId();
  const [profiles, setProfiles] = useState<LocalShellProfile[]>([]);
  useEffect(() => {
    let active = true;
    // A host that cannot list its shells (an older backend, a multi-user server) leaves the free-text fields.
    localShellApi.profiles().then(
      (found) => { if (active) setProfiles(Array.isArray(found) ? found : []); },
      () => { if (active) setProfiles([]); },
    );
    return () => { active = false; };
  }, []);
  const [customPicked, setCustomPicked] = useState(false);
  const derived = profileChoice(profiles, shell, args);
  const choice = customPicked && derived === DEFAULT_CHOICE ? CUSTOM_CHOICE : derived;
  const choose = (value: string) => {
    setCustomPicked(value === CUSTOM_CHOICE);
    if (value === CUSTOM_CHOICE) return;
    const profile = profiles.find((item) => item.id === value);
    onShellChange(profile?.path ?? "");
    onArgsChange(profile ? formatShellArgs(profile.args) : "");
  };
  return (
    <>
      <div className="nx-form-row">
        <label className="nx-label">{t("默认 Shell")}</label>
        {profiles.length > 0 && (
          <Select
            className="mb-2"
            ariaLabel={t("选择本机 Shell")}
            value={choice}
            onChange={choose}
            options={[
              { value: DEFAULT_CHOICE, label: t("默认（账户登录 shell）") },
              ...profiles.map((profile) => ({ value: profile.id, label: profile.name, description: [profile.path, ...profile.args].join(" ") })),
              { value: CUSTOM_CHOICE, label: t("自定义") },
            ]}
          />
        )}
        <input
          className="nx-input font-mono text-[12px]"
          aria-label={t("Shell 路径")}
          value={shell}
          onChange={(e) => onShellChange(e.target.value)}
          placeholder={t("留空使用账户登录 shell（Windows 为 pwsh，未安装时为 powershell.exe）；填写后优先于账户设置")}
          spellCheck={false}
        />
      </div>
      <div className="nx-form-row">
        <label className="nx-label">{t("Shell 启动参数")}</label>
        <input
          className="nx-input font-mono text-[12px]"
          aria-label={t("Shell 启动参数")}
          aria-invalid={argsError ? true : undefined}
          aria-describedby={argsError ? argsErrorId : undefined}
          value={args}
          onChange={(e) => onArgsChange(e.target.value)}
          placeholder={t("可选，空格分隔，含空格的参数用引号括起，如 -d Ubuntu 或 --login -i")}
          spellCheck={false}
        />
        {argsError && (
          <p id={argsErrorId} role="alert" className="mt-1 text-[11px] text-red-400">
            {argsError}
          </p>
        )}
      </div>
    </>
  );
}
