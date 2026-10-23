import type { Asset } from "../../ipc/commands";

export interface AssetInventoryMetadata {
  source: string;
  key: string;
  options: string[];
}

export const INVENTORY_ONLY_KINDS = new Set(["docker-context", "kubernetes"]);

export function isInventoryOnlyKind(kind: string): boolean {
  return INVENTORY_ONLY_KINDS.has(kind);
}

export function assetInventoryMetadata(asset?: Pick<Asset, "options"> | null): AssetInventoryMetadata | null {
  const raw = asset?.options?.inventory;
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  const value = raw as Record<string, unknown>;
  if (typeof value.source !== "string" || !value.source || typeof value.key !== "string" || !value.key) return null;
  return {
    source: value.source,
    key: value.key,
    options: Array.isArray(value.options)
      ? value.options.filter((item): item is string => typeof item === "string")
      : [],
  };
}

export function inventorySourceLabel(source: string): string {
  switch (source) {
    case "ssh-config":
      return "SSH 配置文件";
    case "docker-context":
      return "Docker context";
    case "kubernetes":
      return "Kubernetes";
    default:
      return source;
  }
}

export function inventoryTargetText(options: Record<string, unknown> | undefined): string {
  const raw = options?.inventoryTarget;
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return "";
  const target = raw as Record<string, unknown>;
  if (target.type === "pod") {
    const context = typeof target.context === "string" && target.context ? ` · ${target.context}` : "";
    const phase = typeof target.phase === "string" && target.phase ? ` · ${target.phase}` : "";
    return `pod${context}${phase}`;
  }
  if (target.type === "context" && typeof target.cluster === "string" && target.cluster) {
    return `cluster ${target.cluster}`;
  }
  if (target.type === "ssh-config" && typeof target.sourcePath === "string") {
    return target.sourcePath;
  }
  return "";
}

// inventoryExecTarget returns the raw exec target carried by a discovery or
// synced item, or null when the item has no executable target record.
export function inventoryExecTarget(options: Record<string, unknown> | undefined): Record<string, unknown> | null {
  const raw = options?.inventoryTarget;
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  return raw as Record<string, unknown>;
}

export function withoutInventoryMetadata(options: Record<string, unknown>): Record<string, unknown> {
  const cloned = { ...options };
  delete cloned.inventory;
  delete cloned.inventoryTarget;
  return cloned;
}
