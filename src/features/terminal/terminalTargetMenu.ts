import type { Asset } from "../../ipc/commands";
import type { MenuItem } from "../../ui/ContextMenu";
import type { DetectedTarget } from "./terminalLinkProvider";
import { t } from "../../i18n";

export interface TargetMenuDeps {
  sessionId: string;
  listAssets: () => Promise<Asset[]>;
  openHostDetail: (assetId: string) => void;
  openAssetDraft: (draft: { kind: "ssh"; name: string; host: string; port: number }) => void;
  // Registered local-forward integration, if any.
  createLocalForward?: (request: { sessionId: string; host: string; port: number }) => void | Promise<void>;
  // Fallback: the session's port forwarding tab.
  openForwardTab: (sessionId: string) => void;
  // False for a session without a command channel (Telnet), which cannot forward ports.
  forwardSupported?: boolean;
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
    deps.toast("error", t("读取资产列表失败：{describeError}", { describeError: deps.describeError(error) }));
    return;
  }
  const wanted = host.toLowerCase();
  const matches = assets.filter((asset) => (asset.host ?? "").toLowerCase() === wanted);
  if (matches.length === 0) {
    deps.toast("info", t("没有主机为 {host} 的资产", { host }));
    return;
  }
  deps.openHostDetail(matches[0].id);
  if (matches.length > 1) {
    deps.toast("info", t("共有 {length} 个资产的主机为 {host}，已打开第一个：{name}", { length: matches.length, host, name: matches[0].name }));
  }
}

export function buildTargetMenu(target: DetectedTarget, deps: TargetMenuDeps): MenuItem[] {
  const items: MenuItem[] = [{ kind: "group", label: t("地址 {text}", { text: target.text }) }];
  const bareHost = target.host.replace(/^\[|\]$/g, "");
  if (isIpAddress(target.host)) {
    items.push(
      { kind: "item", label: t("打开已有资产"), onSelect: () => void openExistingAsset(bareHost, deps) },
      {
        kind: "item",
        label: t("新建 SSH 资产"),
        onSelect: () => deps.openAssetDraft({ kind: "ssh", name: bareHost, host: bareHost, port: 22 }),
      },
    );
  }
  if (target.kind === "hostport" && target.port !== undefined && deps.forwardSupported !== false) {
    const port = target.port;
    items.push({
      kind: "item",
      label: t("建立本地转发 → {bareHost}:{port}", { bareHost, port }),
      onSelect: () => {
        if (deps.createLocalForward) {
          void Promise.resolve(deps.createLocalForward({ sessionId: deps.sessionId, host: bareHost, port })).catch(
            (error: unknown) => deps.toast("error", t("建立本地转发失败：{describeError}", { describeError: deps.describeError(error) })),
          );
          return;
        }
        deps.openForwardTab(deps.sessionId);
        deps.toast("info", t("请在端口转发中填写目标 {bareHost}:{port}", { bareHost, port }));
      },
    });
  }
  items.push({
    kind: "item",
    label: t("复制地址"),
    onSelect: () =>
      void deps.copy(target.text).then(
        () => deps.toast("success", t("已复制地址")),
        (error: unknown) => deps.toast("error", t("复制地址失败：{describeError}", { describeError: deps.describeError(error) })),
      ),
  });
  return items;
}
