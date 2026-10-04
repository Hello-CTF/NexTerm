import { IconLoader } from "../../ui/icons";
import type { ReachabilityEntry } from "./assetReachability";

export function ReachabilityDot({ entry }: { entry: ReachabilityEntry | undefined }) {
  if (!entry) return null;
  if (entry.state === "checking") {
    return (
      <span className="flex shrink-0 items-center text-neutral-500" aria-label="可达性检测中">
        <IconLoader size={11} className="animate-spin" />
      </span>
    );
  }
  if (entry.state === "reachable") {
    return (
      <span
        className="h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-400"
        role="img"
        aria-label="可达"
        title={`可达 · ${entry.durationMs ?? 0}ms`}
      />
    );
  }
  return (
    <span
      className="h-1.5 w-1.5 shrink-0 rounded-full bg-red-400"
      role="img"
      aria-label="不可达"
      title={`不可达：${entry.error ?? "未知原因"}`}
    />
  );
}
