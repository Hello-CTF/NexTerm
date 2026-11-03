import type { Asset } from "../../ipc/commands";
import type { MenuItem } from "../../ui/ContextMenu";
import type { DetectedTarget } from "./terminalLinkProvider";

export interface TargetMenuDeps {
  sessionId: string;
  listAssets: () => Promise<Asset[]>;
  openHostDetail: (assetId: string) => void;
  openAssetDraft: (draft: { kind: "ssh"; name: string; host: string; port: number }) => void;
  // Registered local-forward integration, if any.
  createLocalForward?: (request: { sessionId: string; host: string; port: number }) => void | Promise<void>;
  // Fallback: the session's port forwarding tab.
  openForwardTab: (sessionId: string) => void;
  copy: (text: string) => Promise<void>;
  toast: (kind: "info" | "success" | "error", text: string) => void;
  describeError: (error: unknown) => string;
}

export function isIpAddress(host: string): boolean {
  const bare = host.startsWith("[") && host.endsWith("]") ? host.slice(1, -1) : host;
  const v4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(bare);
  if (v4) return v4.slice(1).every((part) => Number(part) <= 255);
  return bare.includes(":") && /^[0-9a-fA-F:.]+$/.test(bare) && bare.split(":").length <= 9;
}

async function openExistingAsset(host: string, deps: TargetMenuDeps): Promise<void> {
  let assets: Asset[];
  try {
    assets = await deps.listAssets();
  } catch (error) {
    deps.toast("error", `读取资产列表失败：${deps.describeError(error)}`);
    return;
  }
  const wanted = host.toLowerCase();
  const matches = assets.filter((asset) => (asset.host ?? "").toLowerCase() === wanted);
  if (matches.length === 0) {
    deps.toast("info", `没有主机为 ${host} 的资产`);
    return;
  }
  deps.openHostDetail(matches[0].id);
  if (matches.length > 1) {
    deps.toast("info", `共有 ${matches.length} 个资产的主机为 ${host}，已打开第一个：${matches[0].name}`);
  }
}

export function buildTargetMenu(target: DetectedTarget, deps: TargetMenuDeps): MenuItem[] {
  const items: MenuItem[] = [{ kind: "group", label: `地址 ${target.text}` }];
  const bareHost = target.host.replace(/^\[|\]$/g, "");
  if (isIpAddress(target.host)) {
    items.push(
      { kind: "item", label: "打开已有资产", onSelect: () => void openExistingAsset(bareHost, deps) },
      {
        kind: "item",
        label: "新建 SSH 资产",
        onSelect: () => deps.openAssetDraft({ kind: "ssh", name: bareHost, host: bareHost, port: 22 }),
      },
    );
  }
  if (target.kind === "hostport" && target.port !== undefined) {
    const port = target.port;
    items.push({
      kind: "item",
      label: `建立本地转发 → ${bareHost}:${port}`,
      onSelect: () => {
        if (deps.createLocalForward) {
          void Promise.resolve(deps.createLocalForward({ sessionId: deps.sessionId, host: bareHost, port })).catch(
            (error: unknown) => deps.toast("error", `建立本地转发失败：${deps.describeError(error)}`),
          );
          return;
        }
        deps.openForwardTab(deps.sessionId);
        deps.toast("info", `请在端口转发中填写目标 ${bareHost}:${port}`);
      },
    });
  }
  items.push({
    kind: "item",
    label: "复制地址",
    onSelect: () =>
      void deps.copy(target.text).then(
        () => deps.toast("success", "已复制地址"),
        (error: unknown) => deps.toast("error", `复制地址失败：${deps.describeError(error)}`),
      ),
  });
  return items;
}
