import { IconLoader } from "../../ui/icons";
import type { ReachabilityEntry } from "./assetReachability";
import { t } from "../../i18n";

export function ReachabilityDot({ entry }: { entry: ReachabilityEntry | undefined }) {
  if (!entry) return null;
  if (entry.state === "checking") {
    return (
      <span className="flex shrink-0 items-center text-neutral-500" aria-label={t("可达性检测中")}>
        <IconLoader size={11} className="animate-spin" />
      </span>
    );
  }
  if (entry.state === "reachable") {
    return (
      <span
        className="h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-400"
        role="img"
        aria-label={t("可达")}
        title={t("可达 · {value}ms", { value: entry.durationMs ?? 0 })}
      />
    );
  }
  return (
    <span
      className="h-1.5 w-1.5 shrink-0 rounded-full bg-red-400"
      role="img"
      aria-label={t("不可达")}
      title={t("不可达：{value}", { value: entry.error ?? t("未知原因") })}
    />
  );
}
