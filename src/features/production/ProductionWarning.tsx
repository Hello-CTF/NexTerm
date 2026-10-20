import { IconAlert } from "../../ui/icons";

// Reusable production visual warning for workspace, terminal, and AI scopes.
// The warning is purely presentational: it names the resolved environment and
// the write risk, and never infers authorization from session memory.

const builtInProductionNames = new Set(["production", "prod"]);

export function isProductionEnvironmentName(environment: string, configuredNames: string[] = []): boolean {
  const normalized = environment.trim().toLowerCase();
  if (builtInProductionNames.has(normalized)) {
    return true;
  }
  return configuredNames.some((name) => name.trim().toLowerCase() === normalized);
}

export function ProductionEnvironmentBadge({
  environment,
  configuredNames,
}: {
  environment: string;
  configuredNames?: string[];
}) {
  const production = isProductionEnvironmentName(environment, configuredNames);
  return (
    <span className={production ? "nx-badge nx-badge-amber" : "nx-badge"} title={production ? "生产环境" : undefined}>
      {production ? "生产 · " : ""}
      {environment}
    </span>
  );
}

export function ProductionWriteRiskNotice({
  environment,
  operation,
  configuredNames,
}: {
  environment: string;
  operation?: string;
  configuredNames?: string[];
}) {
  if (!isProductionEnvironmentName(environment, configuredNames)) {
    return null;
  }
  return (
    <div className="rounded border border-amber-900/50 bg-amber-950/10 px-2.5 py-2 text-[12px] leading-relaxed text-amber-300" role="note">
      <span className="flex items-center gap-1.5">
        <IconAlert size={14} />
        生产环境写入风险
      </span>
      <span className="mt-1 block text-neutral-200">
        当前目标环境为 <code className="text-amber-300">{environment}</code>
        {operation ? `，${operation}写入将需要逐次输入实际环境名确认` : "，写入将需要逐次输入实际环境名确认"}
        ；本会话不会记住授权。
      </span>
    </div>
  );
}
