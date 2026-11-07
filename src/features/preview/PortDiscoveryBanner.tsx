import { useMemo } from "react";
import { IconClose } from "../../ui/icons";
import { forwardAndOpen, targetHostFor } from "./forwardAndOpen";
import { dismissDiscoveredPorts, usePortDiscovery } from "./portDiscovery";
import { t } from "../../i18n";

const MAX_SHOWN = 3;

export function PortDiscoveryBanner({ sessionId }: { sessionId: string }) {
  const { fresh, all } = usePortDiscovery(sessionId);
  const ports = useMemo(() => {
    const seen = new Set<number>();
    return fresh.filter((l) => !seen.has(l.port) && seen.add(l.port));
  }, [fresh]);
  if (ports.length === 0) return null;
  const shown = ports.slice(0, MAX_SHOWN);
  return (
    <div
      className="flex flex-wrap items-center gap-x-2 gap-y-1 border-b border-neutral-800/60 bg-neutral-900/70 px-2.5 py-1 text-[12px]"
      role="status"
      data-testid="port-discovery-banner"
    >
      <span className="text-neutral-300">{t("检测到新端口")}</span>
      {shown.map((l) => (
        <button
          key={l.port}
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-xs"
          title={t("建立临时转发到 {value}:{port}，仅监听 127.0.0.1，会话关闭时自动移除", { value: targetHostFor(l, all), port: l.port })}
          onClick={() => void forwardAndOpen({ sessionId, host: targetHostFor(l, all), port: l.port })}
        >
          {t("{port}，转发并打开", { port: l.port })}
        </button>
      ))}
      {ports.length > shown.length && <span className="nx-hint">{t("另有")} {ports.length - shown.length} {t("个")}</span>}
      <button
        type="button"
        className="nx-icon-btn ml-auto"
        aria-label={t("收起端口提示")}
        title={t("收起，同一端口不再提示")}
        onClick={() => dismissDiscoveredPorts(sessionId, ports.map((l) => l.port))}
      >
        <IconClose size={12} />
      </button>
    </div>
  );
}
