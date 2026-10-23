import { Fragment, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { defaultRangeExtractor, useVirtualizer } from "@tanstack/react-virtual";
import { ask, askChoice, pickKeyFile, promptText } from "../../ui/dialogs";
import { assetApi, sessionApi, vaultApi, type Asset, type AssetGroup } from "../../ipc/commands";
import { WEB } from "../../ipc/env";
import { onAssetDataChanged } from "../../ipc/assetEvents";
import { browserFilesAvailable } from "../../ipc/webFiles";
import {
  assetConnectionProblemKey,
  connectAsset,
  connectQuickTarget,
  useConnectionProblem,
  useUi,
} from "../../app/store";
import { copyAssetDeepLink } from "../../app/deepLink";
import { isMobile, useCoarsePointer } from "../../app/platform";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { ContextMenu, type MenuItem } from "../../ui/ContextMenu";
import { copyText } from "../../ui/clipboard";
import { describeError } from "../../ui/errorText";
import { resolveInlineKeyContent, useInlineKeyPicker } from "../credentials/keyStaging";
import { useVaultInitGate } from "../credentials/useVaultInitGate";
import { cloneAsset } from "./assetClone";
import { useAssetVisibility } from "./assetVisibility";
import { useConnectTemplates, type ConnectTemplate } from "./connectTemplates";
import { formatSshCommand, sshCommandCopyable } from "./sshCommand";
import { useGroupCollapse } from "./groupCollapse";
import { normalizeAssetTags } from "./assetSearch";
import {
  ASSET_ICON_NAMES,
  assetIcon,
  IconChevronDown,
  IconChevronRight,
  IconClose,
  IconCommand,
  IconCopy,
  IconDownload,
  IconEdit,
  IconEye,
  IconEyeOff,
  IconFolder,
  IconGlobe,
  IconLoader,
  IconList,
  IconPlay,
  IconPlug,
  IconPlus,
  IconSearch,
  IconServer,
  IconTerminal,
  IconTrash,
  IconXCircle,
} from "../../ui/icons";
import { SnippetsPanel } from "./SnippetsPanel";
import { SshImportDialog } from "./SshImportDialog";
import { InventoryDialog } from "./InventoryDialog";
import { DeviceTreeSection } from "../fleet/DeviceTreeSection";
import { BulkMaintenanceDialog } from "../execbatch/BulkMaintenancePanel";
import { ProjectSelector } from "./ProjectSelector";
import { assetInventoryMetadata, inventorySourceLabel, inventoryTargetText, isInventoryOnlyKind } from "./inventory";
import { EVENTS, listenEvent } from "../../ipc/events";
import {
  ALL_PROJECTS,
  UNGROUPED_PROJECT,
  filterAssetsForProject,
  filterGroupsForProject,
} from "../../app/projectFocus";

const ASSET_IDENTITY_KEY = "nexterm.assetIdentity.v1";

function loadDetailedAssetIdentity(): boolean {
  try {
    return localStorage.getItem(ASSET_IDENTITY_KEY) !== "compact";
  } catch {
    return true;
  }
}

const assetIdentityMeasurements = new Map<Element, () => void>();
let assetIdentityObserver: ResizeObserver | null = null;

function remeasureAssetIdentities(): void {
  for (const measure of new Set(assetIdentityMeasurements.values())) measure();
}

function watchAssetIdentityWidth(nodes: Element[], measure: () => void): () => void {
  if (assetIdentityMeasurements.size === 0) window.addEventListener("resize", remeasureAssetIdentities);
  if (!assetIdentityObserver && typeof ResizeObserver !== "undefined") {
    assetIdentityObserver = new ResizeObserver((entries) => {
      const callbacks = new Set<() => void>();
      for (const entry of entries) {
        const callback = assetIdentityMeasurements.get(entry.target);
        if (callback) callbacks.add(callback);
      }
      for (const callback of callbacks) callback();
    });
  }
  for (const node of nodes) {
    assetIdentityMeasurements.set(node, measure);
    assetIdentityObserver?.observe(node);
  }
  return () => {
    for (const node of nodes) {
      assetIdentityObserver?.unobserve(node);
      assetIdentityMeasurements.delete(node);
    }
    if (assetIdentityMeasurements.size === 0) {
      assetIdentityObserver?.disconnect();
      assetIdentityObserver = null;
      window.removeEventListener("resize", remeasureAssetIdentities);
    }
  };
}

const KIND_LABEL: Record<string, string> = {
  ssh: "SSH",
  winrm: "WinRM",
  local: "本地终端",
  docker: "Docker 主机",
  "docker-context": "Docker context（仅清单）",
  kubernetes: "Kubernetes（仅清单）",
  mysql: "MySQL / MariaDB",
  postgres: "PostgreSQL",
  redis: "Redis",
};

const DATABASE_ASSET_KINDS = new Set<string>(["mysql", "postgres", "redis"]);

const DRAG_ASSET = "application/x-nexterm-asset";

const ASSET_FIELD_IDLE_VALIDATION_MS = 500;

function validateAssetHost(value: string): string | null {
  const h = value.trim();
  if (!h) return "请填写主机";
  if (/\s/.test(h)) return "主机不能包含空格";
  return null;
}

function validateAssetPort(port: number): string | null {
  if (!Number.isInteger(port) || port < 1 || port > 65535) return "端口需在 1-65535 之间";
  return null;
}

const TERMINAL_ENCODINGS = ["utf-8", "gbk", "gb18030", "big5", "latin1"];

const ENV_LINE_PATTERN = /^([A-Za-z_][A-Za-z0-9_]*)=(.*)$/;

function isNonDefaultEncoding(value: unknown): boolean {
  return typeof value === "string" && value.trim() !== "" && value.trim().toLowerCase() !== "utf-8" && value.trim().toLowerCase() !== "utf8";
}

function formatEnvText(value: unknown): string {
  if (!value || typeof value !== "object" || Array.isArray(value)) return "";
  return Object.entries(value as Record<string, unknown>)
    .map(([key, item]) => `${key}=${typeof item === "string" ? item : String(item)}`)
    .join("\n");
}

function parseEnvText(text: string): { ok: true; env: Record<string, string> } | { ok: false; error: string } {
  const env: Record<string, string> = {};
  const lines = text.split("\n");
  for (let index = 0; index < lines.length; index++) {
    const line = lines[index].trim();
    if (!line) continue;
    const match = ENV_LINE_PATTERN.exec(line);
    if (!match) {
      return { ok: false, error: `环境变量第 ${index + 1} 行不是有效的 KEY=VALUE：${line}` };
    }
    env[match[1]] = match[2];
  }
  return { ok: true, env };
}

function treeRowKeyDown(event: React.KeyboardEvent<HTMLElement>): void {
  if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
  const tree = event.currentTarget.closest("[role='tree']");
  if (!tree) return;
  const rows = [...tree.querySelectorAll<HTMLElement>("[role='treeitem']")];
  const index = rows.indexOf(event.currentTarget);
  const next = event.key === "ArrowDown" ? index + 1 : index - 1;
  if (index < 0 || next < 0 || next >= rows.length) return;
  event.preventDefault();
  rows[next]?.focus();
}

const SKELETON_ROW_WIDTHS = ["w-3/5", "w-2/5", "w-1/2", "w-3/5", "w-2/5"];

function AssetTreeSkeleton() {
  return (
    <div role="status" aria-label="正在加载资产">
      <span className="nx-sr-only">正在加载资产…</span>
      <div aria-hidden="true">
        {SKELETON_ROW_WIDTHS.map((width, i) => (
          <div key={i} className="nx-row cursor-default">
            <span className="h-3.5 w-3.5 shrink-0 animate-pulse rounded-sm bg-neutral-800 motion-reduce:animate-none" />
            <span
              className={`h-3 min-w-0 animate-pulse rounded-sm bg-neutral-800 motion-reduce:animate-none ${width}`}
            />
            <span className="nx-spacer" />
            <span className="h-3 w-12 shrink-0 animate-pulse rounded-sm bg-neutral-800/70 motion-reduce:animate-none" />
          </div>
        ))}
      </div>
    </div>
  );
}

type AssetTreeItem =
  | { key: string; kind: "asset"; asset: Asset; orphaned?: boolean }
  | { key: string; kind: "devices" }
  | { key: string; kind: "group"; node: GroupTreeNode };

interface GroupTreeNode {
  group: AssetGroup;
  assets: Asset[];
  children: GroupTreeNode[];
  totalAssets: number;
  path: string;
  issue: "missing-parent" | "cycle" | null;
}

function groupHierarchy(groups: AssetGroup[], assetsByGroup: Map<string | null, Asset[]>): GroupTreeNode[] {
  const groupsById = new Map(groups.map((group) => [group.id, group]));
  const issues = new Map<string, GroupTreeNode["issue"]>();
  for (const group of groups) {
    if (group.parentId && !groupsById.has(group.parentId)) issues.set(group.id, "missing-parent");
  }
  const done = new Set<string>();
  for (const group of groups) {
    const chain: AssetGroup[] = [];
    const seen = new Map<string, number>();
    let cursor: AssetGroup | undefined = group;
    while (cursor && !done.has(cursor.id)) {
      const previous = seen.get(cursor.id);
      if (previous !== undefined) {
        const cycle = chain.slice(previous).map((item) => item.id).sort();
        issues.set(cycle[0], "cycle");
        break;
      }
      seen.set(cursor.id, chain.length);
      chain.push(cursor);
      cursor = cursor.parentId ? groupsById.get(cursor.parentId) : undefined;
    }
    for (const item of chain) done.add(item.id);
  }
  const nodes = new Map<string, GroupTreeNode>(groups.map((group) => [group.id, {
    group, assets: assetsByGroup.get(group.id) ?? [], children: [], totalAssets: 0,
    path: group.name, issue: issues.get(group.id) ?? null,
  }]));
  const roots: GroupTreeNode[] = [];
  for (const group of groups) {
    const node = nodes.get(group.id)!;
    const parent = node.issue ? null : (group.parentId ? nodes.get(group.parentId) : null);
    if (parent) parent.children.push(node);
    else roots.push(node);
  }
  const decorate = (node: GroupTreeNode, parentPath = "") => {
    node.path = parentPath ? `${parentPath} / ${node.group.name}` : node.group.name;
    node.totalAssets = node.assets.length;
    for (const child of node.children) {
      decorate(child, node.path);
      node.totalAssets += child.totalAssets;
    }
  };
  for (const root of roots) decorate(root);
  return roots;
}

function groupContainsAsset(node: GroupTreeNode, assetId: string): boolean {
  return node.assets.some((asset) => asset.id === assetId) || node.children.some((child) => groupContainsAsset(child, assetId));
}

function groupRowEstimate(node: GroupTreeNode): number {
  return 32 + node.assets.length * 30 + node.children.reduce((height, child) => height + groupRowEstimate(child), 0);
}

function groupPathOptions(groups: AssetGroup[]): { id: string; path: string }[] {
  const options: { id: string; path: string }[] = [];
  const visit = (node: GroupTreeNode) => {
    options.push({ id: node.group.id, path: node.path });
    node.children.forEach(visit);
  };
  groupHierarchy(groups, new Map()).forEach(visit);
  return options;
}

function groupIsDescendant(groups: AssetGroup[], candidateId: string, ancestorId: string): boolean {
  const byId = new Map(groups.map((group) => [group.id, group]));
  const seen = new Set<string>();
  let current: AssetGroup | undefined = byId.get(candidateId);
  while (current?.parentId && !seen.has(current.id)) {
    if (current.parentId === ancestorId) return true;
    seen.add(current.id);
    current = byId.get(current.parentId);
  }
  return false;
}

type AssetMoveError = {
  targetGroupId: string | null;
  sourceName: string;
  targetName: string;
  message: string;
};

export function AssetTree() {
  const qc = useQueryClient();
  const { pushToast, leftOpen, leftWidth, activeProject } = useUi();
  const sessions = useUi((state) => state.sessions);
  const [bulkMaintenanceOpen, setBulkMaintenanceOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [detailedIdentity, setDetailedIdentity] = useState(loadDetailedAssetIdentity);
  const [editing, setEditing] = useState<"none" | "asset" | "group">("none");
  const [editingAsset, setEditingAsset] = useState<Asset | null>(null);
  const [presetGroup, setPresetGroup] = useState<string | null>(null);
  const [renamingGroup, setRenamingGroup] = useState<AssetGroup | null>(null);
  const [groupBusyId, setGroupBusyId] = useState<string | null>(null);
  const [groupError, setGroupError] = useState<{ id: string; message: string; action: "删除" | "修复" } | null>(null);
  const [assetError, setAssetError] = useState<{ id: string; message: string; mode?: "keep" | "disconnect" | "close" } | null>(null);
  const [moveErrors, setMoveErrors] = useState<Record<string, AssetMoveError>>({});
  const [movingTargets, setMovingTargets] = useState<Record<string, string>>({});
  const pendingMovesRef = useRef(new Set<string>());
  const [snippetsOpen, setSnippetsOpen] = useState(false);
  const [importing, setImporting] = useState(false);
  const [inventoryOpen, setInventoryOpen] = useState(false);
  const [rowMenu, setRowMenu] = useState<{ x: number; y: number; asset: Asset; full?: boolean; inlineCount?: number } | null>(null);
  const [treeMenu, setTreeMenu] = useState<{ x: number; y: number } | null>(null);
  const treeRef = useRef<HTMLDivElement>(null);
  const assetRowElementsRef = useRef(new Map<string, HTMLDivElement>());
  const focusedAssetIdRef = useRef<string | null>(null);
  const [restoreFocusId, setRestoreFocusId] = useState<string | null>(null);
  const { hiddenIds, showHidden, setShowHidden, hide, unhide } = useAssetVisibility();

  const assets = useQuery({
    queryKey: ["assets"],
    queryFn: () => assetApi.list(),
    refetchOnWindowFocus: false,
  });
  const groups = useQuery({
    queryKey: ["groups"],
    queryFn: () => assetApi.groupList(),
    refetchOnWindowFocus: false,
  });
  const snippets = useQuery({
    queryKey: ["snippets"],
    queryFn: () => assetApi.snippetList(),
    refetchOnWindowFocus: false,
  });
  const results = useQuery({
    queryKey: ["asset-search", query],
    queryFn: () => assetApi.searchMeta(query),
    enabled: query.trim().length > 0,
  });

  useEffect(() => {
    const refresh = () => {
      void qc.invalidateQueries({ queryKey: ["assets"] });
      void qc.invalidateQueries({ queryKey: ["groups"] });
      void qc.invalidateQueries({ queryKey: ["asset-search"] });
    };
    const offLocal = onAssetDataChanged(refresh);
    let disposed = false;
    let offRemote: (() => void) | undefined;
    void listenEvent(EVENTS.inventoryChanged, refresh).then((off) => {
      if (disposed) off();
      else offRemote = off;
    }).catch(() => undefined);
    return () => {
      disposed = true;
      offLocal();
      offRemote?.();
    };
  }, [qc]);

  const filtered = useMemo(
    () => query.trim() ? (results.data?.assets ?? []) : (assets.data ?? []),
    [query, results.data?.assets, assets.data],
  );
  const projectFiltered = useMemo(
    () => filterAssetsForProject(filtered, activeProject, groups.data ?? []),
    [filtered, activeProject, groups.data],
  );
  const searchSummary = query.trim() && results.data
    ? { total: results.data.total, returned: results.data.assets.length, truncated: results.data.truncated }
    : null;
  const listError = query.trim()
    ? results.isError
      ? results.error
      : null
    : assets.isError
      ? assets.error
      : null;
  const retryList = () => {
    if (query.trim()) void results.refetch();
    else void assets.refetch();
  };
  const isHidden = (id: string) => hiddenIds.includes(id);
  const visible = useMemo(
    () => projectFiltered.filter((a) => showHidden || !hiddenIds.includes(a.id)),
    [projectFiltered, hiddenIds, showHidden],
  );
  const duplicateNames = useMemo(() => {
    const seen = new Set<string>();
    const duplicates = new Set<string>();
    for (const asset of assets.data ?? []) {
      const name = asset.name.trim();
      if (seen.has(name)) duplicates.add(name);
      else seen.add(name);
    }
    return duplicates;
  }, [assets.data]);
  const duplicateGroupNames = useMemo(() => {
    const seen = new Set<string>();
    const duplicates = new Set<string>();
    for (const group of groups.data ?? []) {
      if (seen.has(group.name)) duplicates.add(group.name);
      else seen.add(group.name);
    }
    return duplicates;
  }, [groups.data]);

  const byGroup = useMemo(() => {
    const map = new Map<string | null, Asset[]>();
    for (const a of visible) {
      const list = map.get(a.groupId) ?? [];
      list.push(a);
      map.set(a.groupId, list);
    }
    return map;
  }, [visible]);
  const focusedGroups = useMemo(
    () => filterGroupsForProject(groups.data ?? [], activeProject),
    [groups.data, activeProject],
  );
  const groupRoots = useMemo(() => groupHierarchy(focusedGroups, byGroup), [focusedGroups, byGroup]);
  const groupPaths = useMemo(() => {
    const paths = new Map<string, string>();
    const visit = (node: GroupTreeNode) => {
      paths.set(node.group.id, node.path);
      node.children.forEach(visit);
    };
    groupRoots.forEach(visit);
    return paths;
  }, [groupRoots]);

  // The device section is anchored after "current device/local terminal" (the builtin local asset) and before other assets/groups;
  // devices are excluded from asset search, so the section is not rendered while searching. Falls back to rendering first when the builtin asset is missing.
  const treeItems = useMemo(() => {
    const items: AssetTreeItem[] = [];
    const orphaned = groups.data
      ? visible.filter((asset) => asset.groupId !== null && !groupPaths.has(asset.groupId))
      : [];
    const orphanedIds = new Set(orphaned.map((asset) => asset.id));
    const ungrouped = [...(byGroup.get(null) ?? []), ...orphaned];
    const localIndex = ungrouped.findIndex((a) => a.builtin && a.kind === "local");
    const headAssets = localIndex >= 0 ? ungrouped.slice(0, localIndex + 1) : [];
    const restAssets = localIndex >= 0 ? ungrouped.slice(localIndex + 1) : ungrouped;
    for (const asset of headAssets) items.push({ key: `asset:${asset.id}`, kind: "asset", asset, orphaned: orphanedIds.has(asset.id) });
    if (!query.trim() && (activeProject === ALL_PROJECTS || activeProject === UNGROUPED_PROJECT)) {
      items.push({ key: "devices", kind: "devices" });
    }
    for (const asset of restAssets) items.push({ key: `asset:${asset.id}`, kind: "asset", asset, orphaned: orphanedIds.has(asset.id) });
    for (const node of groupRoots) {
      items.push({ key: `group:${node.group.id}`, kind: "group", node });
    }
    return items;
  }, [activeProject, byGroup, groupRoots, groupPaths, groups.data, query, visible]);
  const treeWindowCount = treeItems.reduce(
    (count, item) => count + (item.kind === "group" ? 1 + item.node.totalAssets + item.node.children.length : 1),
    0,
  );
  const treeVirtualized = treeWindowCount > 40;
  const treeVirtualizer = useVirtualizer({
    count: treeItems.length,
    getScrollElement: () => treeRef.current,
    estimateSize: (index) => {
      const item = treeItems[index];
      if (item.kind === "group") return groupRowEstimate(item.node);
      return item.kind === "devices" ? 160 : item.kind === "asset" && item.orphaned ? 58 : 30;
    },
    overscan: 6,
    rangeExtractor: (range) => {
      const indexes = defaultRangeExtractor(range);
      if (!query.trim()) return indexes;
      const matched = new Set(indexes);
      treeItems.forEach((item, index) => {
        if (item.kind === "asset" || (item.kind === "group" && item.node.totalAssets > 0)) matched.add(index);
      });
      return [...matched].sort((a, b) => a - b);
    },
    enabled: treeVirtualized,
    getItemKey: (index) => treeItems[index]?.key ?? index,
    initialRect: { width: leftWidth, height: 600 },
  });

  useEffect(() => {
    if (!restoreFocusId || query.trim()) return;
    const index = treeItems.findIndex((item) =>
      item.kind === "asset"
        ? item.asset.id === restoreFocusId
        : item.kind === "group" && groupContainsAsset(item.node, restoreFocusId),
    );
    if (index < 0) {
      setRestoreFocusId(null);
      return;
    }
    const row = assetRowElementsRef.current.get(restoreFocusId);
    if (row?.isConnected) {
      row.focus({ preventScroll: true });
      setRestoreFocusId(null);
      return;
    }
    if (treeVirtualized) treeVirtualizer.scrollToIndex(index, { align: "auto" });
  }, [restoreFocusId, query, treeItems, treeVirtualized, treeVirtualizer]);

  const onClone = async (a: Asset) => {
    try {
      const created = await cloneAsset(a);
      if (!created) return;
      void qc.invalidateQueries({ queryKey: ["assets"] });
      void qc.invalidateQueries({ queryKey: ["asset-search"] });
      pushToast("success", `已克隆为「${created.name}」`);
    } catch (e) {
      pushToast("error", `克隆失败：${describeError(e)}`);
    }
  };

  const onHide = (a: Asset) => {
    hide(a.id);
    pushToast("info", `已隐藏「${a.name}」— 点右上角眼睛按钮可找回`);
  };

  const onUnhide = (a: Asset) => {
    unhide(a.id);
    pushToast("info", `已取消隐藏「${a.name}」`);
  };

  const onCopySshCommand = async (a: Asset) => {
    const command = formatSshCommand(a);
    try {
      await copyText(command);
      pushToast("success", `已复制 SSH 命令：${command}`);
    } catch (e) {
      pushToast("error", `复制失败：${describeError(e)}`);
    }
  };

  const onConnectAsUser = async (a: Asset) => {
    const host = a.host?.trim();
    if (!host) return;
    const epoch = useUi.getState().accountEpoch;
    const username = (await promptText(`用其他用户名连接「${a.name}」`, a.username ?? ""))?.trim();
    if (!username || useUi.getState().accountEpoch !== epoch) return;
    void connectQuickTarget({ host, port: a.port ?? 22, username });
  };

  const runDeleteAsset = async (a: Asset, epoch: number, mode: "keep" | "disconnect" | "close" = "keep") => {
    // The confirmed delete is account-scoped: after every await, a changed epoch aborts the
    // flow before the next RPC/cache/UI side effect — most importantly before assetApi.delete,
    // which a late disconnect resolution would otherwise issue under the new account.
    const stale = () => useUi.getState().accountEpoch !== epoch;
    setAssetError(null);
    let closedWorkspaceCount = 0;
    try {
      if (mode === "disconnect") {
        const sessionIds = useUi.getState().sessions
          .filter((s) => s.assetId === a.id && s.status !== "disconnected" && s.status !== "failed")
          .map((s) => s.id);
        for (const id of sessionIds) {
          await sessionApi.disconnect(id);
          if (stale()) return;
        }
        await useUi.getState().resyncSessions({ silent: true });
        if (stale()) return;
      }
      if (mode === "close") {
        const workspaceIds = useUi.getState().workspaces.filter((w) => w.assetId === a.id).map((w) => w.id);
        for (const id of workspaceIds) {
          if (!useUi.getState().workspaces.some((w) => w.id === id)) continue;
          if (!(await useUi.getState().closeWorkspace(id))) return;
          if (stale()) return;
          closedWorkspaceCount++;
        }
      }
      if (stale()) return;
      await assetApi.delete(a.id);
      if (stale()) return;
      if (mode !== "close") useUi.getState().markDeletedAssetWorkspaces(a.id);
      unhide(a.id);
      void qc.invalidateQueries({ queryKey: ["assets"] });
      void qc.invalidateQueries({ queryKey: ["asset-search"] });
      void qc.invalidateQueries({ queryKey: ["credentials"] });
      pushToast("info", "已删除");
    } catch (e) {
      if (stale()) return;
      const detail = closedWorkspaceCount > 0
        ? `${describeError(e)}；已关闭的工作区可从资产列表重新打开`
        : describeError(e);
      setAssetError({ id: a.id, message: detail, mode });
    }
  };

  const onDelete = async (a: Asset) => {
    const epoch = useUi.getState().accountEpoch;
    const state = useUi.getState();
    const workspaces = state.workspaces.filter((w) => w.assetId === a.id);
    const liveSessions = state.sessions.filter((s) =>
      s.assetId === a.id && s.status !== "disconnected" && s.status !== "failed",
    );
    if (workspaces.length > 0 || liveSessions.length > 0) {
      const workspaceNotice = workspaces.length > 0 ? `当前有 ${workspaces.length} 个工作区使用此资产。` : "";
      const sessionNotice = liveSessions.length > 0 ? `当前有 ${liveSessions.length} 个活动会话使用此资产。` : "";
      const action = await askChoice(
        `删除资产「${a.name}」？\n\n${workspaceNotice}${sessionNotice}资产会从列表移除；关联的凭据会保留，已产生的终端历史仍可查看。`,
        {
          title: "删除资产及打开的工作区",
          level: "warning",
          choices: [
            { key: "keep", label: workspaces.length > 0 ? "保留工作区并删除资产" : "保留会话并删除资产", hint: "工作区会显示目标已删除，可继续查看或关闭。", primary: true },
            ...(liveSessions.length > 0 ? [{ key: "disconnect", label: "断开会话并删除资产", hint: "显式断开会话并停止其端口转发；工作区保留。", danger: true }] : []),
            ...(workspaces.length > 0 ? [{ key: "close", label: "关闭工作区并删除资产", hint: "先关闭关联工作区，再删除资产；取消关闭时不会删除。", danger: true }] : []),
          ],
        },
      );
      if (action !== "keep" && action !== "disconnect" && action !== "close") return;
      if (useUi.getState().accountEpoch !== epoch) return;
      await runDeleteAsset(a, epoch, action);
      return;
    }
    const ok = await ask(`删除资产「${a.name}」？\n\n资产会从列表移除；关联的凭据会保留，已产生的终端历史仍可查看。`, { kind: "warning" });
    if (ok && useUi.getState().accountEpoch === epoch) await runDeleteAsset(a, epoch);
  };

  const moveAsset = async (assetId: string, groupId: string | null) => {
    if (pendingMovesRef.current.has(assetId)) return;
    const asset = (assets.data ?? []).find((item) => item.id === assetId) ?? filtered.find((item) => item.id === assetId);
    if (!asset) {
      pushToast("error", "移动失败：资产已不在当前列表，请刷新后重试");
      return;
    }
    const groupName = (id: string | null) =>
      id === null ? "根目录" : groupPaths.get(id) ?? "未知分组";
    pendingMovesRef.current.add(assetId);
    setMovingTargets((prev) => ({ ...prev, [assetId]: groupName(groupId) }));
    setMoveErrors((prev) => {
      const next = { ...prev };
      delete next[assetId];
      return next;
    });
    try {
      await assetApi.update({ id: assetId, groupId });
      const updateCachedGroup = (rows: Asset[] | undefined) =>
        rows?.map((row) => row.id === assetId ? { ...row, groupId } : row);
      qc.setQueryData<Asset[]>(["assets"], updateCachedGroup);
      void qc.invalidateQueries({ queryKey: ["assets"] });
      void qc.invalidateQueries({ queryKey: ["asset-search"] });
      pushToast("info", groupId ? "已移入分组" : "已移出分组");
    } catch (e) {
      const message = describeError(e);
      setMoveErrors((prev) => ({
        ...prev,
        [assetId]: {
          targetGroupId: groupId,
          sourceName: groupName(asset.groupId),
          targetName: groupName(groupId),
          message,
        },
      }));
      if (asset.groupId && useGroupCollapse.getState().collapsedIds.includes(asset.groupId)) {
        useGroupCollapse.getState().toggle(asset.groupId);
      }
      pushToast("error", `移动失败：${message}；「${asset.name}」仍在原位置，可在资产行重试`);
    } finally {
      pendingMovesRef.current.delete(assetId);
      setMovingTargets((prev) => {
        const next = { ...prev };
        delete next[assetId];
        return next;
      });
    }
  };

  const dismissMoveError = (assetId: string) => setMoveErrors((prev) => {
    const next = { ...prev };
    delete next[assetId];
    return next;
  });

  const runDeleteGroup = async (g: AssetGroup, epoch: number) => {
    // The confirmed group delete is account-scoped: a late resolution after a switch must not
    // settle UI or invalidations in the new account.
    const stale = () => useUi.getState().accountEpoch !== epoch;
    setGroupBusyId(g.id);
    setGroupError(null);
    try {
      await assetApi.groupDelete(g.id);
      if (stale()) return;
      void qc.invalidateQueries({ queryKey: ["groups"] });
      void qc.invalidateQueries({ queryKey: ["assets"] });
      void qc.invalidateQueries({ queryKey: ["asset-search"] });
      pushToast("info", "已删除分组");
    } catch (e) {
      if (stale()) return;
      setGroupError({ id: g.id, message: describeError(e), action: "删除" });
    } finally {
      setGroupBusyId(null);
    }
  };

  const deleteGroup = async (g: AssetGroup) => {
    if (groupBusyId) return;
    const epoch = useUi.getState().accountEpoch;
    const ok = await ask(`删除分组「${g.name}」？\n组内资产会移到「未分组」；子分组会被一并删除。`, {
      kind: "warning",
    });
    if (!ok || useUi.getState().accountEpoch !== epoch) return;
    await runDeleteGroup(g, epoch);
  };

  const repairGroup = async (g: AssetGroup) => {
    if (groupBusyId) return;
    const epoch = useUi.getState().accountEpoch;
    const stale = () => useUi.getState().accountEpoch !== epoch;
    setGroupBusyId(g.id);
    setGroupError(null);
    try {
      await assetApi.groupUpdate(g.id, undefined, null);
      if (stale()) return;
      await qc.invalidateQueries({ queryKey: ["groups"] });
      if (stale()) return;
      void qc.invalidateQueries({ queryKey: ["asset-search"] });
      pushToast("success", `已将「${g.name}」移到根目录`);
    } catch (e) {
      if (stale()) return;
      setGroupError({ id: g.id, message: describeError(e), action: "修复" });
    } finally {
      setGroupBusyId(null);
    }
  };

  const buildRowMenu = (asset: Asset, full: boolean, inlineCount = 0): MenuItem[] => {
    const remove: MenuItem | null = asset.builtin
      ? null
      : {
          kind: "item",
          label: "删除",
          icon: <IconClose size={13} />,
          danger: true,
          onSelect: () => void onDelete(asset),
        };
    const primary: MenuItem[] = full
      ? [
          ...(!isInventoryOnlyKind(asset.kind) ? [{
            kind: "item" as const,
            label: "连接",
            icon: <IconPlay size={13} />,
            onSelect: () => void connectAsset(asset),
          }] : []),
          {
            kind: "item",
            label: "编辑",
            icon: <IconEdit size={13} />,
            onSelect: () => setEditingAsset(asset),
          },
          ...(remove ? [remove] : []),
          { kind: "separator" },
        ]
      : [];
    const copySsh: MenuItem | null = sshCommandCopyable(asset)
      ? {
          kind: "item",
          label: "复制 SSH 命令",
          icon: <IconTerminal size={13} />,
          onSelect: () => void onCopySshCommand(asset),
        }
      : null;
    const connectAs: MenuItem | null =
      !WEB && asset.kind === "ssh" && asset.host?.trim()
        ? {
            kind: "item",
            label: "用其他用户名连接",
            icon: <IconPlug size={13} />,
            hint: "走快速连接，用密码或 agent 认证",
            onSelect: () => void onConnectAsUser(asset),
          }
        : null;
    return [
      ...primary.slice(inlineCount),
      {
        kind: "item",
        label: "克隆",
        icon: <IconCopy size={13} />,
        hint: "共用原资产的凭据引用与私钥路径",
        onSelect: () => void onClone(asset),
      },
      ...(copySsh ? [copySsh] : []),
      ...(connectAs ? [connectAs] : []),
      {
        kind: "item",
        label: "复制链接",
        icon: <IconGlobe size={13} />,
        hint: "nexterm:// 链接，可打开 NexTerm 客户端",
        onSelect: () => copyAssetDeepLink(asset.id),
      },
      isHidden(asset.id)
        ? {
            kind: "item",
            label: "取消隐藏",
            icon: <IconEye size={13} />,
            onSelect: () => onUnhide(asset),
          }
        : {
            kind: "item",
            label: "隐藏",
            icon: <IconEyeOff size={13} />,
            hint: "仅界面隐藏",
            onSelect: () => onHide(asset),
          },
    ];
  };

  const registerAssetRow = (asset: Asset, node: HTMLDivElement | null) => {
    if (node) assetRowElementsRef.current.set(asset.id, node);
    else assetRowElementsRef.current.delete(asset.id);
  };
  const onAssetFocus = (asset: Asset) => {
    focusedAssetIdRef.current = asset.id;
  };
  const renderAssetRow = (asset: Asset, level: number) => (
    <AssetRow
      asset={asset}
      level={level}
      rowRef={(node) => registerAssetRow(asset, node)}
      onFocus={() => onAssetFocus(asset)}
      hidden={isHidden(asset.id)}
      showIdentity={detailedIdentity || duplicateNames.has(asset.name.trim())}
      onDelete={() => void onDelete(asset)}
      onEdit={() => setEditingAsset(asset)}
      onMore={(x, y, full, inlineCount) => setRowMenu({ x, y, asset, full, inlineCount })}
      error={assetError?.id === asset.id ? assetError.message : null}
      onRetry={() => void runDeleteAsset(asset, useUi.getState().accountEpoch, assetError?.mode)}
      onDismissError={() => setAssetError(null)}
      moveError={moveErrors[asset.id]}
      movingTarget={movingTargets[asset.id]}
      onRetryMove={() => {
        const failure = moveErrors[asset.id];
        if (failure) void moveAsset(asset.id, failure.targetGroupId);
      }}
      onCancelMove={() => dismissMoveError(asset.id)}
    />
  );

  const renderGroupNode = (node: GroupTreeNode, level: number): React.ReactNode => {
    const group = node.group;
    return (
      <GroupNode
        key={group.id}
        group={group}
        path={node.path}
        contextPath={group.parentId ? groupPaths.get(group.parentId) ?? "父分组缺失" : "根目录"}
        showPath={duplicateGroupNames.has(group.name)}
        issue={node.issue}
        level={level}
        assets={node.assets}
        totalAssets={node.totalAssets}
        children={node.children}
        renderChildGroup={(child) => renderGroupNode(child, level + 1)}
        virtualizeAssets={treeVirtualized && node.assets.length > 40}
        searching={Boolean(query.trim())}
        restoreFocusId={restoreFocusId}
        getTreeScrollElement={() => treeRef.current}
        registerAssetRow={registerAssetRow}
        onAssetFocus={onAssetFocus}
        hiddenIds={hiddenIds}
        duplicateNames={duplicateNames}
        detailedIdentity={detailedIdentity}
        onDelete={(asset) => void onDelete(asset)}
        onEdit={(asset) => setEditingAsset(asset)}
        onMore={(asset, x, y, full, inlineCount) => setRowMenu({ x, y, asset, full, inlineCount })}
        onMoveAsset={(assetId, groupId) => void moveAsset(assetId, groupId)}
        onCreateIn={() => {
          setPresetGroup(group.id);
          setEditing("asset");
        }}
        onCreateChild={() => {
          setPresetGroup(group.id);
          setEditing("group");
        }}
        busy={groupBusyId === group.id}
        error={groupError?.id === group.id ? groupError.message : null}
        errorAction={groupError?.id === group.id ? groupError.action : "删除"}
        onRename={() => setRenamingGroup(group)}
        onDeleteGroup={() => void deleteGroup(group)}
        onRepair={() => void repairGroup(group)}
        onRetry={() => void (groupError?.id === group.id && groupError.action === "修复" ? repairGroup(group) : runDeleteGroup(group, useUi.getState().accountEpoch))}
        onDismissError={() => setGroupError(null)}
        assetError={assetError}
        onRetryAssetDelete={(asset) => void runDeleteAsset(asset, useUi.getState().accountEpoch, assetError?.mode)}
        onDismissAssetError={() => setAssetError(null)}
        moveErrors={moveErrors}
        movingTargets={movingTargets}
        onRetryMove={(asset) => {
          const failure = moveErrors[asset.id];
          if (failure) void moveAsset(asset.id, failure.targetGroupId);
        }}
        onCancelMove={(asset) => dismissMoveError(asset.id)}
      />
    );
  };
  const renderTreeItem = (item: AssetTreeItem) => {
    if (item.kind === "asset") return item.orphaned ? (
      <div>
        {renderAssetRow(item.asset, 1)}
        <div className="mx-1 mb-1 flex items-center gap-2 rounded bg-amber-500/10 px-2 py-1.5 text-[11.5px] text-amber-300">
          <span className="min-w-0 flex-1">所属分组不存在，已暂列根目录。</span>
          <button className="nx-link shrink-0" onClick={() => void moveAsset(item.asset.id, null)}>移到根目录</button>
        </div>
      </div>
    ) : renderAssetRow(item.asset, 1);
    if (item.kind === "devices") return <DeviceTreeSection />;
    return renderGroupNode(item.node, 1);
  };

  if (!leftOpen) return null;

  return (
    <div
      className="flex h-full shrink-0 flex-col border-r border-neutral-800/60 bg-neutral-950"
      style={{ width: leftWidth }}
    >
      <div className="flex h-[34px] shrink-0 items-center gap-1 px-2.5">
        <span className="text-xs font-semibold tracking-wide text-neutral-200">资产</span>
        <div className="nx-spacer" />
        {hiddenIds.length > 0 && (
          <button
            className={`nx-icon-btn nx-icon-btn-sm ${showHidden ? "is-active" : ""}`}
            title={showHidden ? "收起已隐藏的资产" : `显示已隐藏的资产（${hiddenIds.length}）`}
            aria-label={showHidden ? "收起已隐藏的资产" : "显示已隐藏的资产"}
            onClick={() => setShowHidden(!showHidden)}
          >
            {showHidden ? <IconEyeOff size={14} /> : <IconEye size={14} />}
          </button>
        )}
        <button
          className={`nx-icon-btn nx-icon-btn-sm ${detailedIdentity ? "is-active" : ""}`}
          title={detailedIdentity ? "紧凑显示资产" : "显示资产主机身份"}
          aria-label={detailedIdentity ? "紧凑显示资产" : "显示资产主机身份"}
          aria-pressed={detailedIdentity}
          onClick={() => {
            const next = !detailedIdentity;
            setDetailedIdentity(next);
            try {
              localStorage.setItem(ASSET_IDENTITY_KEY, next ? "detailed" : "compact");
            } catch {
              // Keep the current display choice for this session when storage is unavailable.
            }
          }}
        >
          <IconList size={14} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="新建资产"
          aria-label="新建资产"
          onClick={() => {
            setPresetGroup(null);
            setEditing("asset");
          }}
        >
          <IconPlus size={14} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="新建分组"
          aria-label="新建分组"
          onClick={() => setEditing("group")}
        >
          <IconFolder size={14} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="批量维护"
          aria-label="批量维护"
          onClick={() => setBulkMaintenanceOpen(true)}
        >
          <IconServer size={14} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="从 SSH 配置或 Termius 导入"
          aria-label="导入 SSH 主机与密钥"
          onClick={() => setImporting(true)}
        >
          <IconDownload size={14} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="动态清单源：SSH config、Docker contexts、Kubernetes"
          aria-label="动态清单源"
          onClick={() => setInventoryOpen(true)}
        >
          <IconGlobe size={14} />
        </button>
      </div>

      <ProjectSelector groups={groups.data ?? []} />

      <div className="px-2 pb-2">
        <div className="nx-field">
          <span className="nx-field-icon">
            <IconSearch size={13} />
          </span>
          <input
            className="nx-input nx-input-sm"
            placeholder="搜索资产 / 主机 / 用户"
            aria-label="搜索资产 / 主机 / 用户"
            value={query}
            onChange={(e) => {
              const next = e.target.value;
              if (query.trim() && !next.trim() && focusedAssetIdRef.current) {
                setRestoreFocusId(focusedAssetIdRef.current);
              }
              setQuery(next);
            }}
          />
        </div>
      </div>

      {searchSummary && !listError && (
        <div className="px-2 pb-1 text-[11px] text-neutral-500" role="status">
          共 {searchSummary.total} 条匹配
          {searchSummary.truncated ? `，仅返回前 ${searchSummary.returned} 条（结果已截断）` : ""}
        </div>
      )}

      <div
        ref={treeRef}
        role="tree"
        aria-label="资产"
        className="min-h-0 flex-1 overflow-y-auto px-1.5 pb-2"
        onDragOver={(e) => {
          if (e.dataTransfer.types.includes(DRAG_ASSET)) e.preventDefault();
        }}
        onDrop={(e) => {
          const id = e.dataTransfer.getData(DRAG_ASSET);
          if (id) void moveAsset(id, null);
        }}
        onContextMenu={(e) => {
          if (e.target !== e.currentTarget) return;
          e.preventDefault();
          setTreeMenu({ x: e.clientX, y: e.clientY });
        }}
      >
        {assets.isPending ? (
          <AssetTreeSkeleton />
        ) : listError ? (
          <div role="alert" className="px-2 py-8 text-center">
            <div className="flex items-center justify-center gap-1.5 text-[12px] text-red-400">
              <IconXCircle size={13} /> 加载失败：{describeError(listError)}
            </div>
            <button
              className="nx-btn nx-btn-outline nx-btn-sm mt-3"
              onClick={retryList}
            >
              重试
            </button>
          </div>
        ) : (
          <>
            {treeVirtualized ? (
              <div className="relative" style={{ height: treeVirtualizer.getTotalSize() }}>
                {treeVirtualizer.getVirtualItems().map((virtualItem) => (
                  <div
                    key={virtualItem.key}
                    ref={treeVirtualizer.measureElement}
                    data-index={virtualItem.index}
                    className="absolute left-0 right-0 top-0 flow-root"
                    style={{ transform: `translateY(${virtualItem.start}px)` }}
                  >
                    {renderTreeItem(treeItems[virtualItem.index])}
                  </div>
                ))}
              </div>
            ) : (
              treeItems.map((item) => <Fragment key={item.key}>{renderTreeItem(item)}</Fragment>)
            )}
            {visible.length === 0 && (
              <div className="nx-hint px-2 py-8 text-center">
                {query
                  ? "没有匹配的资产"
                  : activeProject !== ALL_PROJECTS
                    ? "当前筛选没有可见资产"
                    : (assets.data?.length ?? 0) > 0
                      ? "资产都隐藏了 — 点右上角眼睛按钮显示"
                      : "还没有资产 — 点右上角的 + 新建一个"}
              </div>
            )}
          </>
        )}
      </div>

      <button
        className="mx-1.5 mb-1 mt-auto flex h-[32px] shrink-0 items-center gap-2 rounded-md border border-transparent px-2.5 text-[12.5px] text-neutral-400 transition-colors hover:bg-neutral-800/70 hover:text-neutral-100"
        title="命令片段（插入当前终端）"
        onClick={() => setSnippetsOpen(true)}
      >
        <IconCommand size={14} className="shrink-0 text-neutral-500" />
        <span className="flex-1 text-left">命令片段</span>
        <span className="nx-count">{snippets.data?.length ?? "…"}</span>
        <IconChevronRight size={12} className="shrink-0 text-neutral-600" />
      </button>

      {editing !== "none" && (
        <AssetEditor
          kind={editing}
          presetGroupId={presetGroup}
          onClose={() => {
            setEditing("none");
            setPresetGroup(null);
          }}
          onSaved={() => {
            setEditing("none");
            setPresetGroup(null);
            void qc.invalidateQueries({ queryKey: ["assets"] });
            void qc.invalidateQueries({ queryKey: ["asset-search"] });
            void qc.invalidateQueries({ queryKey: ["groups"] });
            void qc.invalidateQueries({ queryKey: ["credentials"] });
          }}
        />
      )}
      {editingAsset && (
        <AssetEditor
          kind="asset"
          initial={editingAsset}
          onClose={() => setEditingAsset(null)}
          onSaved={() => {
            setEditingAsset(null);
            void qc.invalidateQueries({ queryKey: ["assets"] });
            void qc.invalidateQueries({ queryKey: ["asset-search"] });
            void qc.invalidateQueries({ queryKey: ["credentials"] });
          }}
        />
      )}
      {renamingGroup && (
        <GroupRenameDialog
          group={renamingGroup}
          groups={groups.data ?? []}
          onClose={() => setRenamingGroup(null)}
          onRenamed={() => {
            setRenamingGroup(null);
            void qc.invalidateQueries({ queryKey: ["groups"] });
            void qc.invalidateQueries({ queryKey: ["asset-search"] });
          }}
        />
      )}
      {snippetsOpen && <SnippetsPanel onClose={() => setSnippetsOpen(false)} />}
      {importing && <SshImportDialog onClose={() => setImporting(false)} />}
      {inventoryOpen && <InventoryDialog onClose={() => setInventoryOpen(false)} />}
      {bulkMaintenanceOpen && <BulkMaintenanceDialog sessions={sessions} onClose={() => setBulkMaintenanceOpen(false)} />}
      <ContextMenu
        state={
          rowMenu
            ? {
                x: rowMenu.x,
                y: rowMenu.y,
                title: rowMenu.asset.name,
                items: buildRowMenu(rowMenu.asset, rowMenu.full === true, rowMenu.inlineCount),
              }
            : null
        }
        onClose={() => setRowMenu(null)}
      />
      <ContextMenu
        state={
          treeMenu
            ? {
                x: treeMenu.x,
                y: treeMenu.y,
                items: [
                  {
                    kind: "item",
                    label: "新建分组",
                    icon: <IconFolder size={13} />,
                    onSelect: () => setEditing("group"),
                  },
                ],
              }
            : null
        }
        onClose={() => setTreeMenu(null)}
      />
    </div>
  );
}

// The row badge is a transient hint; the workspace problem log keeps the full record.
const CONNECTION_BADGE_TTL_MS = 60_000;

function useRecentProblem<T extends { createdAt: number }>(problem: T | undefined): T | undefined {
  const expiresAt = problem ? problem.createdAt + CONNECTION_BADGE_TTL_MS : 0;
  const [, setExpired] = useState(0);
  const live = expiresAt > Date.now();
  useEffect(() => {
    if (!live) return;
    const timer = window.setTimeout(() => setExpired(expiresAt), expiresAt - Date.now());
    return () => window.clearTimeout(timer);
  }, [expiresAt, live]);
  return live ? problem : undefined;
}

function AssetRow({
  asset,
  level,
  rowRef,
  onFocus,
  hidden,
  showIdentity,
  onDelete,
  onEdit,
  onMore,
  error,
  onRetry,
  onDismissError,
  moveError,
  movingTarget,
  onRetryMove,
  onCancelMove,
}: {
  asset: Asset;
  level: number;
  rowRef?: (node: HTMLDivElement | null) => void;
  onFocus?: () => void;
  hidden?: boolean;
  showIdentity?: boolean;
  onDelete: () => void;
  onEdit: () => void;
  onMore: (x: number, y: number, full?: boolean, inlineCount?: number) => void;
  error?: string | null;
  onRetry?: () => void;
  onDismissError?: () => void;
  moveError?: AssetMoveError;
  movingTarget?: string;
  onRetryMove?: () => void;
  onCancelMove?: () => void;
}) {
  const coarse = useCoarsePointer();
  const Icon = assetIcon(asset.kind, typeof asset.options?.icon === "string" ? asset.options.icon : undefined);
  const connecting = useUi((s) => s.connectingAssetIds.includes(asset.id));
  const connectionProblem = useRecentProblem(useConnectionProblem(assetConnectionProblemKey(asset.id)));
  const managed = assetInventoryMetadata(asset);
  const inventoryOnly = isInventoryOnlyKind(asset.kind);
  const label = asset.name;
  const target = asset.host ? `${asset.host}${asset.port ? `:${asset.port}` : ""}` : "";
  const targetSummary = inventoryTargetText(asset.options);
  const identity = target ? `${asset.username?.trim() ? `${asset.username.trim()}@` : ""}${target}` : targetSummary || (asset.builtin ? "本机" : "");
  const env = asset.options?.env;
  const envCount = env && typeof env === "object" && !Array.isArray(env) ? Object.keys(env).length : 0;
  const identityAreaRef = useRef<HTMLSpanElement>(null);
  const nameMeasureRef = useRef<HTMLSpanElement>(null);
  const actionsRef = useRef<HTMLSpanElement>(null);
  const moreRef = useRef<HTMLButtonElement>(null);
  const actionCount = (inventoryOnly ? 0 : 1) + 1 + (asset.builtin ? 0 : 1);
  const [inlineCount, setInlineCount] = useState(actionCount);
  const [identitySpace, setIdentitySpace] = useState(true);
  useLayoutEffect(() => {
    const area = identityAreaRef.current;
    const nameNode = nameMeasureRef.current;
    const actions = actionsRef.current;
    const more = moreRef.current;
    if (!area || !nameNode || !more) return;
    const measure = () => {
      const available = area.clientWidth;
      const nameWidth = nameNode.getBoundingClientRect().width;
      setIdentitySpace(available === 0 || available - nameWidth > 6);
      const actionsWidth = actions?.getBoundingClientRect().width ?? 0;
      if (coarse || !actions || (available === 0 && actionsWidth === 0)) return;
      const rowGap = parseFloat(getComputedStyle(area.parentElement!).columnGap) || 7;
      const actionGap = parseFloat(getComputedStyle(actions).columnGap) || 2;
      const buttonWidth = more.getBoundingClientRect().width || 22;
      const total = available + actionsWidth + rowGap;
      const remaining = total - nameWidth - rowGap - buttonWidth;
      const identityWidth = showIdentity && identity ? Math.min(36, Math.max(0, remaining)) : 0;
      setInlineCount(Math.max(0, Math.min(actionCount, Math.floor((remaining - identityWidth) / (buttonWidth + actionGap)))));
    };
    measure();
    return watchAssetIdentityWidth([area, nameNode, more, ...(actions ? [actions] : [])], measure);
  }, [coarse, actionCount, label, showIdentity, identity]);
  const compactActions = coarse;
  const tip = [
    asset.name,
    target || targetSummary || (asset.builtin ? "本机" : ""),
    managed ? `由 ${inventorySourceLabel(managed.source)} 管理 · 来源字段只读` : "",
    asset.builtin ? "内置资产 · 不可删除" : "",
    hidden ? "已隐藏" : "",
    connectionProblem ? `连接失败：${connectionProblem.summary}` : "",
    inventoryOnly ? "仅清单记录 · 终端接入未实现" : "双击连接 · 可拖入分组",
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <>
    <div
      ref={rowRef}
      role="treeitem"
      aria-level={level}
      tabIndex={0}
      className={`nx-row nx-row-oneline nx-row-reserve-actions nx-asset-row ${hidden ? "opacity-55" : ""}`}
      onFocusCapture={onFocus}
      draggable={!movingTarget && !managed}
      onDragStart={(e) => {
        if (managed) {
          e.preventDefault();
          return;
        }
        e.dataTransfer.setData(DRAG_ASSET, asset.id);
        e.dataTransfer.effectAllowed = "move";
      }}
      onDoubleClick={() => {
        if (!inventoryOnly) void connectAsset(asset);
      }}
      onContextMenu={(e) => {
        e.preventDefault();
        onMore(e.clientX, e.clientY, coarse);
      }}
      onKeyDown={(e) => {
        if (e.target !== e.currentTarget) return;
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          if (!inventoryOnly) void connectAsset(asset);
          return;
        }
        treeRowKeyDown(e);
      }}
      title={tip}
    >
      <Icon size={14} className="shrink-0 text-neutral-500" />
      <span ref={identityAreaRef} className="nx-row-name min-w-0 flex-1 overflow-hidden">
        <span className="nx-asset-row-name break-all" style={{ whiteSpace: "normal", overflowX: "visible" }}>{label}</span>
        {showIdentity && identity && identitySpace && (
          <span className="nx-asset-row-identity text-[11px] text-neutral-400" title={identity}>{identity}</span>
        )}
      </span>
      <span aria-hidden="true" className="nx-asset-row-measures pointer-events-none invisible whitespace-nowrap" style={{ position: "absolute", width: 0, height: 0, overflow: "hidden" }}>
        <span ref={nameMeasureRef} className="nx-asset-row-name-measure inline-block whitespace-nowrap">{label}</span>
      </span>
      {managed && <span className="nx-badge nx-badge-blue shrink-0" title={`由 ${inventorySourceLabel(managed.source)} 管理；来源字段只读`}>{inventorySourceLabel(managed.source)}</span>}
      {envCount > 0 && <span className="nx-badge nx-badge-amber" title={`${envCount} 个环境变量`}>env {envCount}</span>}
      {hidden && <span className="nx-badge">已隐藏</span>}
      {asset.builtin && <span className="nx-badge nx-badge-blue">本机</span>}
      {connectionProblem && !inventoryOnly && (
        <button
          type="button"
          className="nx-badge nx-badge-red shrink-0 cursor-pointer"
          title={`连接失败：${connectionProblem.summary}；点击重试`}
          aria-label={`重试连接 ${asset.name}`}
          onClick={(event) => {
            event.stopPropagation();
            void connectAsset(asset);
          }}
          onDoubleClick={(event) => event.stopPropagation()}
        >
          连接失败 · 重试
        </button>
      )}
      {compactActions ? (
        <button
          ref={moreRef}
          className="nx-icon-btn nx-icon-btn-sm nx-row-more"
          title="更多操作"
          aria-label={`更多操作 ${asset.name}`}
          onClick={(e) => {
            e.stopPropagation();
            const r = e.currentTarget.getBoundingClientRect();
            onMore(r.left, r.bottom, true);
          }}
          onDoubleClick={(e) => e.stopPropagation()}
        >
          ⋯
        </button>
      ) : (
        <span ref={actionsRef} className="nx-row-actions" onDoubleClick={(e) => e.stopPropagation()}>
          {!inventoryOnly && inlineCount > 0 && (connecting ? (
            <span
              className="nx-icon-btn nx-icon-btn-sm"
              title="连接中…"
              aria-label={`${asset.name} 连接中`}
            >
              <IconLoader size={12} className="animate-spin" />
            </span>
          ) : (
            <button
              className="nx-icon-btn nx-icon-btn-sm"
              title="连接"
              aria-label={`连接 ${asset.name}`}
              onClick={(e) => {
                e.stopPropagation();
                void connectAsset(asset);
              }}
            >
              <IconPlay size={12} />
            </button>
          ))}
          {inlineCount > (inventoryOnly ? 0 : 1) && <button
            className="nx-icon-btn nx-icon-btn-sm"
            title="编辑"
            aria-label={`编辑 ${asset.name}`}
            onClick={(e) => {
              e.stopPropagation();
              onEdit();
            }}
          >
            <IconEdit size={12} />
          </button>}
          {!asset.builtin && inlineCount > (inventoryOnly ? 1 : 2) && (
            <button
              className="nx-icon-btn nx-icon-btn-sm is-danger"
              title="删除"
              aria-label={`删除 ${asset.name}`}
              onClick={(e) => {
                e.stopPropagation();
                onDelete();
              }}
            >
              <IconClose size={12} />
            </button>
          )}
          <button
            ref={moreRef}
            className={`nx-icon-btn nx-icon-btn-sm ${inlineCount === 0 ? "nx-row-more" : ""}`}
            title="更多操作"
            aria-label={`更多操作 ${asset.name}`}
            onClick={(e) => {
              e.stopPropagation();
              const r = e.currentTarget.getBoundingClientRect();
              onMore(r.left, r.bottom, inlineCount < actionCount, inlineCount < actionCount ? inlineCount : 0);
            }}
          >
            ⋯
          </button>
        </span>
      )}
    </div>
    {error && (
      <div className="mx-1 mb-1 flex items-center gap-2 rounded bg-red-500/10 px-2 py-1.5 text-[11.5px] text-red-400">
        <IconXCircle size={12} className="shrink-0" />
        <span className="min-w-0 flex-1 truncate" title={error}>
          删除失败：{error}
        </span>
        <button className="nx-link shrink-0" onClick={onRetry}>
          重试
        </button>
        <button className="nx-link shrink-0" onClick={onDismissError}>
          知道了
        </button>
      </div>
    )}
    {moveError && (
      <div role="alert" className="mx-1 mb-1 flex items-center gap-2 rounded bg-red-500/10 px-2 py-1.5 text-[11.5px] text-red-400">
        <IconXCircle size={12} className="shrink-0" />
        <span className="min-w-0 flex-1" title={moveError.message}>
          移动失败：{moveError.message}。仍在「{moveError.sourceName}」，目标「{moveError.targetName}」。
        </span>
        <button className="nx-link shrink-0" onClick={onRetryMove}>重试移动</button>
        <button className="nx-link shrink-0" onClick={onCancelMove}>取消移动</button>
      </div>
    )}
    {movingTarget && (
      <div role="status" className="mx-1 mb-1 rounded bg-sky-500/10 px-2 py-1.5 text-[11.5px] text-sky-300">
        正在移至「{movingTarget}」…
      </div>
    )}
    </>
  );
}

function GroupNode({
  group,
  path,
  contextPath,
  showPath,
  issue,
  level,
  assets,
  totalAssets,
  children,
  renderChildGroup,
  virtualizeAssets,
  searching,
  restoreFocusId,
  getTreeScrollElement,
  registerAssetRow,
  onAssetFocus,
  hiddenIds,
  duplicateNames,
  detailedIdentity,
  onDelete,
  onEdit,
  onMore,
  onMoveAsset,
  onCreateIn,
  onCreateChild,
  busy,
  error,
  errorAction,
  onRename,
  onDeleteGroup,
  onRepair,
  onRetry,
  onDismissError,
  assetError,
  onRetryAssetDelete,
  onDismissAssetError,
  moveErrors,
  movingTargets,
  onRetryMove,
  onCancelMove,
}: {
  group: AssetGroup;
  path: string;
  contextPath: string;
  showPath: boolean;
  issue: GroupTreeNode["issue"];
  level: number;
  assets: Asset[];
  totalAssets: number;
  children: GroupTreeNode[];
  renderChildGroup: (child: GroupTreeNode) => React.ReactNode;
  virtualizeAssets: boolean;
  searching: boolean;
  restoreFocusId: string | null;
  getTreeScrollElement: () => HTMLDivElement | null;
  registerAssetRow: (asset: Asset, node: HTMLDivElement | null) => void;
  onAssetFocus: (asset: Asset) => void;
  hiddenIds: string[];
  duplicateNames: Set<string>;
  detailedIdentity: boolean;
  onDelete: (a: Asset) => void;
  onEdit: (a: Asset) => void;
  onMore: (a: Asset, x: number, y: number, full?: boolean, inlineCount?: number) => void;
  onMoveAsset: (assetId: string, groupId: string | null) => void;
  onCreateIn: () => void;
  onCreateChild: () => void;
  busy: boolean;
  error: string | null;
  errorAction: "删除" | "修复";
  onRename: () => void;
  onDeleteGroup: () => void;
  onRepair: () => void;
  onRetry: () => void;
  onDismissError: () => void;
  assetError: { id: string; message: string } | null;
  onRetryAssetDelete: (a: Asset) => void;
  onDismissAssetError: () => void;
  moveErrors: Record<string, AssetMoveError>;
  movingTargets: Record<string, string>;
  onRetryMove: (a: Asset) => void;
  onCancelMove: (a: Asset) => void;
}) {
  const collapsed = useGroupCollapse((s) => s.collapsedIds.includes(group.id));
  const toggleCollapsed = useGroupCollapse((s) => s.toggle);
  const open = !collapsed;
  const [over, setOver] = useState(false);
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  const coarse = useCoarsePointer();
  const assetsRef = useRef<HTMLDivElement>(null);
  const focusedGroupAssetIdRef = useRef<string | null>(null);
  const [assetsScrollMargin, setAssetsScrollMargin] = useState(0);
  const assetVirtualizer = useVirtualizer({
    count: assets.length,
    getScrollElement: getTreeScrollElement,
    estimateSize: () => 30,
    overscan: 6,
    rangeExtractor: (range) => searching ? assets.map((_, index) => index) : defaultRangeExtractor(range),
    enabled: virtualizeAssets && open,
    getItemKey: (index) => assets[index]?.id ?? index,
    scrollMargin: assetsScrollMargin,
    initialRect: { width: 360, height: 600 },
  });
  useLayoutEffect(() => {
    if (!virtualizeAssets || !open) return;
    const list = assetsRef.current;
    const scroller = getTreeScrollElement();
    if (!list || !scroller) return;
    const next = list.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop;
    setAssetsScrollMargin((current) => (Math.abs(current - next) < 1 ? current : next));
  }, [virtualizeAssets, open, getTreeScrollElement]);
  useEffect(() => {
    if (!restoreFocusId || !virtualizeAssets || !open) return;
    const index = assets.findIndex((asset) => asset.id === restoreFocusId);
    if (index >= 0) assetVirtualizer.scrollToIndex(index, { align: "auto" });
  });
  useEffect(() => {
    if (!open || !virtualizeAssets) return;
    const focusedId = focusedGroupAssetIdRef.current;
    if (!focusedId) return;
    const index = assets.findIndex((asset) => asset.id === focusedId);
    if (index >= 0) assetVirtualizer.scrollToIndex(index, { align: "auto" });
  }, [open, assets, assetVirtualizer, virtualizeAssets]);
  const isAssetDrag = (e: React.DragEvent) =>
    e.dataTransfer.types.includes(DRAG_ASSET);
  const renderGroupAsset = (a: Asset) => (
    <AssetRow
      key={a.id}
      asset={a}
      level={level + 1}
      rowRef={(node) => registerAssetRow(a, node)}
      onFocus={() => {
        focusedGroupAssetIdRef.current = a.id;
        onAssetFocus(a);
      }}
      hidden={hiddenIds.includes(a.id)}
      showIdentity={detailedIdentity || duplicateNames.has(a.name.trim())}
      onDelete={() => onDelete(a)}
      onEdit={() => onEdit(a)}
      onMore={(x, y, full, inlineCount) => onMore(a, x, y, full, inlineCount)}
      error={assetError?.id === a.id ? assetError.message : null}
      onRetry={() => onRetryAssetDelete(a)}
      onDismissError={onDismissAssetError}
      moveError={moveErrors[a.id]}
      movingTarget={movingTargets[a.id]}
      onRetryMove={() => onRetryMove(a)}
      onCancelMove={() => onCancelMove(a)}
    />
  );
  return (
    <div
      role="treeitem"
      aria-expanded={open}
      aria-level={level}
      tabIndex={0}
      className={`mb-0.5 rounded ${over ? "bg-sky-500/10 ring-1 ring-inset ring-sky-500/40" : ""}`}
      onKeyDown={(e) => {
        if (e.target !== e.currentTarget) return;
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          toggleCollapsed(group.id);
          return;
        }
        if (e.key === "ArrowRight") {
          if (!open) {
            e.preventDefault();
            toggleCollapsed(group.id);
          }
          return;
        }
        if (e.key === "ArrowLeft") {
          if (open) {
            e.preventDefault();
            toggleCollapsed(group.id);
          }
          return;
        }
        treeRowKeyDown(e);
      }}
      onContextMenu={(e) => {
        if (e.target instanceof Element && e.target.closest("[role='treeitem']") !== e.currentTarget) return;
        e.preventDefault();
        setMenu({ x: e.clientX, y: e.clientY });
      }}
      onDragOver={(e) => {
        if (isAssetDrag(e)) {
          e.preventDefault();
          setOver(true);
        }
      }}
      onDragLeave={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node)) setOver(false);
      }}
      onDrop={(e) => {
        if (!isAssetDrag(e)) return;
        e.preventDefault();
        e.stopPropagation();
        setOver(false);
        const id = e.dataTransfer.getData(DRAG_ASSET);
        if (id) onMoveAsset(id, group.id);
      }}
    >
      <div className="nx-row nx-row-oneline nx-row-reserve-actions w-full text-neutral-400">
        <button
          className="flex min-w-0 flex-auto items-center gap-1 text-left"
          onClick={() => toggleCollapsed(group.id)}
        >
          {open ? (
            <IconChevronDown size={12} className="shrink-0" />
          ) : (
            <IconChevronRight size={12} className="shrink-0" />
          )}
          <IconFolder size={13} className="shrink-0 text-neutral-500" />
          <span className="nx-row-name min-w-0 flex-1 truncate text-xs font-medium" title={path}>{group.name}</span>
          {showPath && <span className="nx-hint max-w-[90px] shrink truncate text-[10px]" title={contextPath}>{contextPath}</span>}
        </button>
        <span className="nx-count">{totalAssets}</span>
        {coarse ? (
          <button
            className="nx-icon-btn nx-icon-btn-sm nx-row-more"
            title="更多操作"
            aria-label={`更多操作 ${group.name}`}
            disabled={busy}
            onClick={(e) => {
              e.stopPropagation();
              const r = e.currentTarget.getBoundingClientRect();
              setMenu({ x: r.left, y: r.bottom });
            }}
            onDoubleClick={(e) => e.stopPropagation()}
          >
            ⋯
          </button>
        ) : (
          <span className="nx-row-actions">
            <button
              className="nx-icon-btn nx-icon-btn-sm"
              title="在分组内新建资产"
              aria-label={`在分组「${group.name}」内新建资产`}
              disabled={busy}
              onClick={(e) => {
                e.stopPropagation();
                onCreateIn();
              }}
            >
              <IconPlus size={12} />
            </button>
            <button
              className="nx-icon-btn nx-icon-btn-sm"
              title="重命名分组"
              aria-label={`重命名分组「${group.name}」`}
              disabled={busy}
              onClick={(e) => {
                e.stopPropagation();
                onRename();
              }}
            >
              <IconEdit size={12} />
            </button>
            <button
              className="nx-icon-btn nx-icon-btn-sm is-danger"
              title="删除分组"
              aria-label={`删除分组「${group.name}」`}
              disabled={busy}
              onClick={(e) => {
                e.stopPropagation();
                onDeleteGroup();
              }}
            >
              {busy ? <IconLoader size={12} className="animate-spin" /> : <IconTrash size={12} />}
            </button>
          </span>
        )}
      </div>
      {issue && (
        <div className="mx-1 mb-1 flex items-center gap-2 rounded bg-amber-500/10 px-2 py-1.5 text-[11.5px] text-amber-300">
          <IconXCircle size={12} className="shrink-0" />
          <span className="min-w-0 flex-1">{issue === "cycle" ? "分组父子关系形成循环，已暂列根目录。" : "父分组不存在，已暂列根目录。"}</span>
          <button className="nx-link shrink-0" disabled={busy} onClick={onRepair}>移到根目录</button>
        </div>
      )}
      {error && (
        <div className="mx-1 mb-1 flex items-center gap-2 rounded bg-red-500/10 px-2 py-1.5 text-[11.5px] text-red-400">
          <IconXCircle size={12} className="shrink-0" />
          <span className="min-w-0 flex-1 truncate" title={error}>
            {errorAction}失败：{error}
          </span>
          <button className="nx-link shrink-0" onClick={onRetry}>
            重试
          </button>
          <button className="nx-link shrink-0" onClick={onDismissError}>
            知道了
          </button>
        </div>
      )}
      {open && <div role="group" className="ml-3.5">{virtualizeAssets ? (
        <div
          ref={assetsRef}
          className="relative"
          style={{ height: assetVirtualizer.getTotalSize() }}
        >
          {assetVirtualizer.getVirtualItems().map((virtualItem) => (
            <div
              key={virtualItem.key}
              ref={assetVirtualizer.measureElement}
              data-index={virtualItem.index}
              className="absolute left-0 right-0 top-0 flow-root"
              style={{ transform: `translateY(${virtualItem.start - assetsScrollMargin}px)` }}
            >
              {renderGroupAsset(assets[virtualItem.index])}
            </div>
          ))}
        </div>
      ) : (
        <div>
          {assets.map(renderGroupAsset)}
          {assets.length === 0 && children.length === 0 && (
            <div className="px-2 py-1.5 text-[11px] text-neutral-500">（空分组 · 可拖资产进来）</div>
          )}
        </div>
      )}{children.map(renderChildGroup)}</div>}
      <ContextMenu
        state={
          menu
            ? {
                x: menu.x,
                y: menu.y,
                title: path,
                items: [
                  {
                    kind: "item",
                    label: "在分组内新建资产",
                    icon: <IconPlus size={13} />,
                    disabled: busy,
                    onSelect: onCreateIn,
                  },
                  {
                    kind: "item",
                    label: "新建子分组",
                    icon: <IconFolder size={13} />,
                    disabled: busy,
                    onSelect: onCreateChild,
                  },
                  {
                    kind: "item",
                    label: "重命名分组",
                    icon: <IconEdit size={13} />,
                    disabled: busy,
                    onSelect: onRename,
                  },
                  {
                    kind: "item",
                    label: "删除分组",
                    icon: <IconTrash size={13} />,
                    danger: true,
                    disabled: busy,
                    onSelect: onDeleteGroup,
                  },
                ] satisfies MenuItem[],
              }
            : null
        }
        onClose={() => setMenu(null)}
      />
    </div>
  );
}

function GroupRenameDialog({
  group,
  groups,
  onClose,
  onRenamed,
}: {
  group: AssetGroup;
  groups: AssetGroup[];
  onClose: () => void;
  onRenamed: () => void;
}) {
  const pushToast = useUi((s) => s.pushToast);
  const [name, setName] = useState(group.name);
  const [parentId, setParentId] = useState<string | null>(group.parentId);
  const parentOptions = groupPathOptions(groups).filter((option) =>
    option.id !== group.id && !groupIsDescendant(groups, option.id, group.id));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const modalRef = useRef<HTMLDivElement>(null);
  const nameInputRef = useRef<HTMLInputElement>(null);
  const titleId = useId();
  const layer = useOverlayFocus(true, modalRef, {
    initialFocus: () => nameInputRef.current,
    selectInitial: true,
  });

  const requestClose = async () => {
    if (saving) return;
    if (name === group.name && parentId === group.parentId) {
      onClose();
      return;
    }
    const ok = await ask("放弃未保存的修改？", { kind: "warning" });
    if (ok) onClose();
  };

  const save = async () => {
    if (saving) return;
    const next = name.trim();
    if (!next) {
      setError("分组名不能为空");
      return;
    }
    if (next === group.name && parentId === group.parentId) {
      onClose();
      return;
    }
    setSaving(true);
    setError(null);
    try {
      if (parentId !== group.parentId) await assetApi.groupUpdate(group.id, next !== group.name ? next : undefined, parentId);
      else await assetApi.groupUpdate(group.id, next);
      pushToast("success", parentId !== group.parentId ? "已更新分组位置" : "已重命名");
      onRenamed();
    } catch (e) {
      setError(describeError(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="nx-overlay" onClick={() => void requestClose()}>
      <div
        ref={modalRef}
        className="nx-modal flex max-w-[340px] flex-col"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          e.stopPropagation();
          if (!layer.isTopmost()) return;
          if (e.key === "Escape" && !e.repeat && !isImeKeyEvent(e)) {
            e.preventDefault();
            void requestClose();
            return;
          }
          trapOverlayTab(e, modalRef.current);
        }}
      >
        <div className="nx-modal-header shrink-0">
          <span id={titleId} className="text-[13px] font-semibold text-neutral-100">
            重命名分组
          </span>
        </div>
        <div className="nx-modal-body min-h-0 flex-1 overflow-y-auto">
          <div className="nx-form-row">
            <label className="nx-label">名称</label>
            <input
              ref={nameInputRef}
              className="nx-input"
              value={name}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !isImeKeyEvent(e)) void save();
              }}
            />
          </div>
          <div className="nx-form-row">
            <label className="nx-label">父分组</label>
            <select className="nx-select" value={parentId ?? ""} onChange={(e) => setParentId(e.target.value || null)}>
              <option value="">（根目录）</option>
              {parentOptions.map((option) => <option key={option.id} value={option.id}>{option.path}</option>)}
            </select>
          </div>
          {error && (
            <div className="mb-2 flex items-center gap-1.5 text-[12px] text-red-400">
              <IconXCircle size={13} /> {error}
            </div>
          )}
        </div>
        <div className="nx-modal-footer shrink-0">
          <button className="nx-btn nx-btn-ghost" onClick={() => void requestClose()} disabled={saving}>
            取消
          </button>
          <button
            className="nx-btn nx-btn-primary"
            disabled={saving || !name.trim()}
            onClick={() => void save()}
          >
            {saving ? "保存中…" : "保存"}
          </button>
        </div>
      </div>
    </div>
  );
}

type CredChoice = "new" | "none" | string;

const CRED_KIND_LABEL: Record<string, string> = {
  password: "密码",
  private_key: "私钥",
  api_key: "API Key",
};

export function AssetEditor({
  kind,
  initial,
  presetGroupId,
  onClose,
  onSaved,
}: {
  kind: "asset" | "group";
  initial?: Asset;
  presetGroupId?: string | null;
  onClose: () => void;
  onSaved: (asset?: Asset) => void;
}) {
  const managed = assetInventoryMetadata(initial);
  const [name, setName] = useState(initial?.name ?? "");
  const [groupKind, setGroupKind] = useState<
    "ssh" | "winrm" | "local" | "docker" | "docker-context" | "kubernetes" | "mysql" | "postgres" | "redis"
  >((initial?.kind as "ssh") ?? "ssh");
  const inventoryOnly = isInventoryOnlyKind(groupKind);
  const [groupId, setGroupId] = useState<string | null>(initial?.groupId ?? presetGroupId ?? null);
  const [tags, setTags] = useState(initial?.tags ?? "");
  const [note, setNote] = useState(initial?.note ?? "");
  const [host, setHost] = useState(initial?.host ?? "");
  const [port, setPort] = useState(initial?.port ?? 22);
  const [username, setUsername] = useState(initial?.username ?? "root");
  const [authKind, setAuthKind] = useState(initial?.authKind ?? "password");
  const [keyPath, setKeyPath] = useState(initial?.keyPath ?? "");
  const [password, setPassword] = useState("");
  const [shell, setShell] = useState(
    typeof initial?.options?.shell === "string" ? (initial.options.shell as string) : "",
  );
  const [cwd, setCwd] = useState(
    typeof initial?.options?.cwd === "string" ? (initial.options.cwd as string) : "",
  );
  const [jumpAssetId, setJumpAssetId] = useState(
    typeof initial?.options?.jumpAssetId === "string" ? (initial.options.jumpAssetId as string) : "",
  );
  const [viaAssetId, setViaAssetId] = useState(
    DATABASE_ASSET_KINDS.has(initial?.kind ?? "") && typeof initial?.options?.viaAssetId === "string"
      ? (initial?.options?.viaAssetId as string)
      : "",
  );
  const [proxyCommand, setProxyCommand] = useState(
    typeof initial?.options?.proxyCommand === "string" ? (initial.options.proxyCommand as string) : "",
  );
  const [forwardAgent, setForwardAgent] = useState(initial?.options?.forwardAgent === true);
  const [agentSocket, setAgentSocket] = useState(
    typeof initial?.options?.agentSocket === "string" ? (initial.options.agentSocket as string) : "",
  );
  const [certPath, setCertPath] = useState(
    typeof initial?.options?.certPath === "string" ? (initial.options.certPath as string) : "",
  );
  const [startupCommand, setStartupCommand] = useState(
    typeof initial?.options?.startupCommand === "string" ? (initial.options.startupCommand as string) : "",
  );
  const [terminalMode, setTerminalMode] = useState<"auto" | "durable" | "regular">(
    initial?.options?.terminalMode === "regular" || initial?.options?.terminalMode === "durable"
      ? initial.options.terminalMode
      : "auto",
  );
  const [encoding, setEncoding] = useState(() => {
    const raw = typeof initial?.options?.encoding === "string" ? (initial.options.encoding as string) : "";
    const normalized = raw.trim().toLowerCase() === "utf8" ? "utf-8" : raw.trim().toLowerCase() === "iso-8859-1" ? "latin1" : raw.trim().toLowerCase();
    return TERMINAL_ENCODINGS.includes(normalized) ? normalized : "utf-8";
  });
  const [envText, setEnvText] = useState(() => formatEnvText(initial?.options?.env));
  const [icon, setIcon] = useState(
    typeof initial?.options?.icon === "string" ? (initial.options.icon as string) : "",
  );
  const [iconPickerOpen, setIconPickerOpen] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const pushToast = useUi((s) => s.pushToast);
  const [saving, setSaving] = useState(false);
  const savingRef = useRef(false);
  const [saveError, setSaveError] = useState<string | null>(null);

  const [probe, setProbe] = useState<{ status: "idle" | "pending" | "ok" | "fail"; error?: string }>({
    status: "idle",
  });
  const probeSeq = useRef(0);
  const probeAtRef = useRef(0);
  const hostRef = useRef(host);
  hostRef.current = host;
  const portRef = useRef(port);
  portRef.current = port;

  useEffect(() => {
    probeSeq.current += 1;
    setProbe({ status: "idle" });
  }, [host, port, groupKind]);

  const advancedPanelId = useId();
  const terminalModeId = useId();
  const IconPreview = assetIcon(groupKind, icon);
  const hostInputId = useId();
  const portInputId = useId();
  const hostErrorId = useId();
  const portErrorId = useId();
  const [hostError, setHostError] = useState<string | null>(null);
  const [portError, setPortError] = useState<string | null>(null);
  const hostValidateTimer = useRef<number | null>(null);
  const portValidateTimer = useRef<number | null>(null);

  useEffect(
    () => () => {
      if (hostValidateTimer.current !== null) window.clearTimeout(hostValidateTimer.current);
      if (portValidateTimer.current !== null) window.clearTimeout(portValidateTimer.current);
    },
    [],
  );

  const onHostChange = (value: string) => {
    setHost(value);
    if (hostValidateTimer.current !== null) window.clearTimeout(hostValidateTimer.current);
    hostValidateTimer.current = window.setTimeout(() => {
      hostValidateTimer.current = null;
      setHostError(validateAssetHost(value));
    }, ASSET_FIELD_IDLE_VALIDATION_MS);
  };
  const onHostBlur = () => {
    if (hostValidateTimer.current !== null) {
      window.clearTimeout(hostValidateTimer.current);
      hostValidateTimer.current = null;
    }
    setHostError(validateAssetHost(hostRef.current));
  };
  const onPortChange = (value: number) => {
    setPort(value);
    if (portValidateTimer.current !== null) window.clearTimeout(portValidateTimer.current);
    portValidateTimer.current = window.setTimeout(() => {
      portValidateTimer.current = null;
      setPortError(validateAssetPort(value));
    }, ASSET_FIELD_IDLE_VALIDATION_MS);
  };
  const onPortBlur = () => {
    if (portValidateTimer.current !== null) {
      window.clearTimeout(portValidateTimer.current);
      portValidateTimer.current = null;
    }
    setPortError(validateAssetPort(portRef.current));
  };

  const runProbe = async () => {
    const h = host.trim();
    const p = Number(port);
    if (!h || !Number.isFinite(p) || p <= 0 || p > 65535) {
      pushToast("error", "先填写有效的主机和端口");
      return;
    }
    if (probe.status === "pending") return;
    const now = Date.now();
    if (now - probeAtRef.current < 1500) return;
    probeAtRef.current = now;
    const seq = ++probeSeq.current;
    setProbe({ status: "pending" });
    try {
      const res = await sessionApi.probe(h, p, 3000);
      if (seq !== probeSeq.current) return;
      if (hostRef.current.trim() !== h || Number(portRef.current) !== p) {
        setProbe({ status: "idle" });
        return;
      }
      setProbe(res.open ? { status: "ok" } : { status: "fail", error: res.error || "端口不可达" });
    } catch (e) {
      if (seq !== probeSeq.current) return;
      setProbe({ status: "fail", error: describeError(e) });
    }
  };

  const [credChoice, setCredChoice] = useState<CredChoice>(initial?.credId ?? "new");
  const [credName, setCredName] = useState(initial?.name ?? "");
  const credNameTouched = useRef(false);
  const credentials = useQuery({
    queryKey: ["credentials"],
    queryFn: () => vaultApi.listCredentials(),
    refetchOnWindowFocus: false,
  });
  const qc = useQueryClient();
  const groups = useQuery({
    queryKey: ["groups"],
    queryFn: () => assetApi.groupList(),
    refetchOnWindowFocus: false,
  });
  const groupOptions = useMemo(() => groupPathOptions(groups.data ?? []), [groups.data]);
  const assets = useQuery({
    queryKey: ["assets"],
    queryFn: () => assetApi.list(),
    refetchOnWindowFocus: false,
  });
  const jumpCandidates = (assets.data ?? []).filter(
    (a) => (a.kind === "ssh" || a.kind === "docker") && a.id !== initial?.id,
  );
  const viaCandidates = (assets.data ?? []).filter(
    (a) => a.kind === "ssh" && a.id !== initial?.id,
  );

  const createGroupInline = async () => {
    const name = (await promptText("新建分组", ""))?.trim();
    if (!name) return;
    try {
      const group = groupId ? await assetApi.groupCreate(name, groupId) : await assetApi.groupCreate(name);
      await qc.invalidateQueries({ queryKey: ["groups"] });
      setGroupId(group.id);
    } catch (e) {
      pushToast("error", `新建分组失败：${describeError(e)}`);
    }
  };

  const boundCred = credentials.data?.find((c) => c.id === initial?.credId);
  const boundIsVaultKey = boundCred?.kind === "private_key";
  const boundIsRefKey = boundIsVaultKey && boundCred?.source === "file";
  const privateKeys = (credentials.data ?? []).filter((c) => c.kind === "private_key");

  const webFiles = browserFilesAvailable();
  const [keyOrigin, setKeyOrigin] = useState<"ref" | "vault">(
    boundIsRefKey || initial?.keyPath ? "ref" : "vault",
  );
  const keyOriginTouchedRef = useRef(false);
  const [vaultMode, setVaultMode] = useState<"new" | "existing">(
    boundIsVaultKey && !boundIsRefKey ? "existing" : "new",
  );
  const [keyContentMode, setKeyContentMode] = useState<"file" | "paste">("file");
  const [inlineKeyContent, setInlineKeyContent] = useState<string | null>(null);
  const [pastedKey, setPastedKey] = useState("");
  const [passphrase, setPassphrase] = useState("");
  const [vaultCredId, setVaultCredId] = useState(boundIsVaultKey ? (initial?.credId ?? "") : "");

  useEffect(() => {
    if (keyOriginTouchedRef.current || !boundIsRefKey) return;
    setKeyOrigin("ref");
    if (!keyPath && boundCred?.refPath) setKeyPath(boundCred.refPath);
  }, [boundIsRefKey, boundCred?.refPath, keyPath]);

  const isDb = DATABASE_ASSET_KINDS.has(groupKind);
  const keyAuth = authKind === "key";
  const usesCred = isDb || authKind === "password" || authKind === "keyboard-interactive";
  const credKind = "password";
  const hasBoundPassphrase = boundCred?.kind === "passphrase";

  const { ensureVaultInit, ensureVaultReady, vaultInitGate, vaultStatus } = useVaultInitGate();
  const vaultPromptedRef = useRef(false);
  const credFlowNeedsVault =
    kind === "asset" && !initial && (usesCred || (keyAuth && keyOrigin === "vault"));
  const editAddsVaultSecret =
    kind === "asset" &&
    !!initial &&
    !managed &&
    ((usesCred && credChoice === "new" && password.length > 0) ||
      (keyAuth && keyOrigin === "ref" && passphrase.length > 0) ||
      (keyAuth && keyOrigin === "vault" && vaultMode === "new"));
  const vaultGateNeeded = credFlowNeedsVault || editAddsVaultSecret;
  useEffect(() => {
    if (vaultPromptedRef.current || !vaultGateNeeded) return;
    if (!vaultStatus || vaultStatus.initialized) return;
    vaultPromptedRef.current = true;
    void ensureVaultInit();
  }, [vaultGateNeeded, vaultStatus, ensureVaultInit]);

  const syncCredName = (assetName: string) => {
    setName(assetName);
    if (!credNameTouched.current) setCredName(assetName);
  };

  const inlinePicker = useInlineKeyPicker();
  const pickInline = async () => {
    try {
      const selection = await inlinePicker.pick();
      if (!selection) return;
      setKeyPath(selection.path);
      setInlineKeyContent(selection.content);
      setDirty(true);
    } catch (e) {
      pushToast("error", describeError(e));
    }
  };
  const changeKeyOrigin = (next: "ref" | "vault") => {
    keyOriginTouchedRef.current = true;
    inlinePicker.invalidate();
    if (next === "ref" && inlineKeyContent !== null) {
      setKeyPath("");
      setInlineKeyContent(null);
    }
    setKeyOrigin(next);
    setDirty(true);
  };
  const changeInlinePath = (next: string) => {
    inlinePicker.invalidate();
    setKeyPath(next);
    setInlineKeyContent(null);
  };

  const sshConnectivityOptions = () => {
    const options: Record<string, unknown> = { ...(initial?.options ?? {}) };
    const setOrDelete = (key: string, value: string) => {
      if (value.trim()) options[key] = value.trim();
      else delete options[key];
    };
    setOrDelete("jumpAssetId", jumpAssetId);
    setOrDelete("proxyCommand", proxyCommand);
    setOrDelete("agentSocket", agentSocket);
    setOrDelete("certPath", certPath);
    setOrDelete("startupCommand", startupCommand);
    if (groupKind === "ssh" && terminalMode !== "auto") options.terminalMode = terminalMode;
    else delete options.terminalMode;
    if (encoding !== "utf-8") options.encoding = encoding;
    else delete options.encoding;
    const parsedEnv = parseEnvText(envText);
    if (parsedEnv.ok && Object.keys(parsedEnv.env).length > 0) options.env = parsedEnv.env;
    else delete options.env;
    if (forwardAgent) options.forwardAgent = true;
    else delete options.forwardAgent;
    return options;
  };

  const save = async () => {
    if (savingRef.current) return;
    // The save is account-scoped: after every await, a changed epoch aborts the flow before
    // the next RPC/cache/UI side effect — a credential or asset written after a switch would
    // otherwise land in the new account.
    const epoch = useUi.getState().accountEpoch;
    const stale = () => useUi.getState().accountEpoch !== epoch;
    if (kind === "asset" && groupKind !== "local" && !inventoryOnly) {
      const nextHostError = validateAssetHost(hostRef.current);
      const nextPortError = validateAssetPort(portRef.current);
      setHostError(nextHostError);
      setPortError(nextPortError);
      if (nextHostError || nextPortError) return;
    }
    if (kind === "asset" && (groupKind === "ssh" || groupKind === "docker")) {
      const parsedEnv = parseEnvText(envText);
      if (!parsedEnv.ok) {
        setSaveError(parsedEnv.error);
        return;
      }
    }
    savingRef.current = true;
    setSaving(true);
    setSaveError(null);
    try {
      if (kind === "group") {
        if (groupId) await assetApi.groupCreate(name, groupId);
        else await assetApi.groupCreate(name);
        if (stale()) return;
        onSaved();
        return;
      }
      const willWriteVault = !managed && (
        (usesCred && credChoice === "new" && password.length > 0) ||
        (keyAuth && keyOrigin === "ref" && (passphrase.length > 0 || boundIsRefKey)) ||
        (keyAuth && keyOrigin === "vault" && vaultMode === "new")
      );
      if (willWriteVault && !(await ensureVaultReady("保存凭据前需要解锁凭据库"))) return;
      if (stale()) return;
      let finalKeyPath: string | null = null;
      let credId: string | null = null;
      const reuseCredId = boundIsVaultKey ? (initial?.credId ?? undefined) : undefined;
      const credLabel = credName.trim() || name.trim();
      if (managed) {
        finalKeyPath = initial?.keyPath ?? null;
        credId = initial?.credId ?? null;
      } else if (keyAuth) {
        if (keyOrigin === "ref") {
          const path = keyPath.trim();
          if (!path && !initial) {
            pushToast("error", "请选择私钥文件，或改用「存入凭据库」");
            return;
          }
          if (passphrase || boundIsRefKey) {
            const res = await vaultApi.setCredential(credLabel, "private_key", path, {
              id: reuseCredId,
              source: "file",
              ...(passphrase ? { passphrase } : {}),
            });
            if (stale()) return;
            credId = res.id;
            finalKeyPath = null;
          } else {
            finalKeyPath = path || null;
            credId = hasBoundPassphrase ? (initial?.credId ?? null) : null;
          }
        } else if (vaultMode === "existing") {
          if (!vaultCredId) {
            pushToast("error", "请选择一条私钥凭据");
            return;
          }
          finalKeyPath = null;
          credId = vaultCredId;
        } else {
          if (keyContentMode === "paste") {
            if (!pastedKey.trim()) {
              pushToast("error", "请粘贴私钥内容");
              return;
            }
          } else if (!keyPath.trim()) {
            pushToast("error", "请选择私钥文件");
            return;
          }
          const content =
            keyContentMode === "paste"
              ? pastedKey.trim()
              : await resolveInlineKeyContent(
                  { path: keyPath.trim(), content: inlineKeyContent },
                  assetApi.readKeyFile,
                );
          if (stale()) return;
          const res = await vaultApi.setCredential(credLabel, "private_key", content, {
            id: reuseCredId,
            source: "inline",
            ...(passphrase ? { passphrase } : {}),
          });
          if (stale()) return;
          credId = res.id;
          finalKeyPath = null;
        }
      } else if (credChoice === "new") {
        if (usesCred && password) {
          const res = await vaultApi.setCredential(
            credName.trim() || name.trim(),
            credKind,
            password,
          );
          if (stale()) return;
          credId = res.id;
        }
      } else if (credChoice !== "none") {
        credId = credChoice;
      }
      const baseOptions =
        groupKind === "local"
          ? {
              ...(shell.trim() ? { shell: shell.trim() } : {}),
              ...(cwd.trim() ? { cwd: cwd.trim() } : {}),
            }
          : groupKind === "ssh" || groupKind === "docker"
            ? sshConnectivityOptions()
            : { ...(initial?.options ?? {}) };
      if (isDb && viaAssetId) baseOptions.viaAssetId = viaAssetId;
      else delete baseOptions.viaAssetId;
      if (icon.trim()) baseOptions.icon = icon.trim();
      else delete baseOptions.icon;
      const options = baseOptions;
      const fields = {
        kind: managed ? initial?.kind ?? groupKind : groupKind,
        name: managed ? initial?.name ?? name : name,
        groupId: managed ? initial?.groupId ?? null : groupId,
        host: managed ? initial?.host ?? null : groupKind === "local" ? null : host,
        port: managed ? initial?.port ?? null : groupKind === "local" ? null : Number(port),
        username: managed ? initial?.username ?? null : groupKind === "local" ? null : username,
        authKind: managed ? initial?.authKind ?? null : groupKind === "local" ? "none" : authKind,
        keyPath: managed ? initial?.keyPath ?? null : keyAuth ? finalKeyPath : null,
        credId,
        options,
        tags: normalizeAssetTags(tags),
        note,
      };
      const saved = initial
        ? await assetApi.update({ id: initial.id, ...fields })
        : await assetApi.create(fields);
      if (stale()) return;
      onSaved(saved);
    } catch (e) {
      if (stale()) return;
      setSaveError(describeError(e));
    } finally {
      savingRef.current = false;
      if (!stale()) setSaving(false);
    }
  };

  const modalRef = useRef<HTMLDivElement>(null);
  const nameInputRef = useRef<HTMLInputElement>(null);
  const titleId = useId();
  const [dirty, setDirty] = useState(false);
  const layer = useOverlayFocus(true, modalRef, {
    initialFocus: () => nameInputRef.current,
  });

  const templates = useConnectTemplates((s) => s.templates);
  const [templateId, setTemplateId] = useState("");

  const applyTemplate = (t: ConnectTemplate) => {
    setGroupKind(t.kind);
    setHostError(null);
    setPortError(null);
    setPort(t.port);
    setUsername(t.username);
    setAuthKind(t.authKind);
    setKeyOrigin(t.keyOrigin);
    setKeyPath(t.keyPath);
    setVaultCredId(t.vaultCredId);
    if (t.vaultCredId) setVaultMode("existing");
    setJumpAssetId(t.jumpAssetId);
    setProxyCommand(t.proxyCommand);
    setForwardAgent(t.forwardAgent);
    setAgentSocket(t.agentSocket);
    setCertPath(t.certPath);
    setStartupCommand(t.startupCommand);
    setEncoding(TERMINAL_ENCODINGS.includes(t.encoding) ? t.encoding : "utf-8");
    setEnvText(t.envText);
    setAdvancedOpen(
      !!t.jumpAssetId ||
        !!t.proxyCommand ||
        t.forwardAgent ||
        !!t.agentSocket ||
        !!t.certPath ||
        !!t.startupCommand ||
        isNonDefaultEncoding(t.encoding) ||
        !!t.envText.trim(),
    );
    setDirty(true);
  };

  const saveAsTemplate = async () => {
    const templateName = (await promptText("模板名称", name.trim()))?.trim();
    if (!templateName) return;
    useConnectTemplates.getState().add({
      name: templateName,
      kind: groupKind,
      port: validateAssetPort(port) === null ? port : 22,
      username,
      authKind,
      keyOrigin,
      keyPath,
      vaultCredId: keyAuth && keyOrigin === "vault" && vaultMode === "existing" ? vaultCredId : "",
      jumpAssetId,
      proxyCommand,
      forwardAgent,
      agentSocket,
      certPath,
      startupCommand,
      encoding,
      envText,
    });
    pushToast("success", `已存为模板「${templateName}」`);
  };

  const deleteTemplate = () => {
    useConnectTemplates.getState().remove(templateId);
    setTemplateId("");
    pushToast("info", "已删除模板");
  };

  const requestClose = async () => {
    if (savingRef.current) return;
    if (!dirty) {
      onClose();
      return;
    }
    const ok = await ask("放弃未保存的修改？", { kind: "warning" });
    if (ok) onClose();
  };

  return (
    <>
    <div className="nx-overlay" onClick={() => void requestClose()}>
      <div
        ref={modalRef}
        className="nx-modal flex max-w-[380px] flex-col"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          e.stopPropagation();
          if (!layer.isTopmost()) return;
          if (e.key === "Escape" && !e.repeat && !isImeKeyEvent(e)) {
            e.preventDefault();
            void requestClose();
            return;
          }
          trapOverlayTab(e, modalRef.current);
        }}
      >
        <div className="nx-modal-header shrink-0">
          <span id={titleId} className="text-[13px] font-semibold text-neutral-100">
            {kind === "group" ? "新建分组" : initial ? "编辑资产" : "新建资产"}
          </span>
        </div>
        <div
          className="nx-modal-body min-h-0 flex-1 overflow-y-auto"
          onChange={() => setDirty(true)}
        >
          {managed && (
            <div className="mb-3 rounded-md border border-blue-500/25 bg-blue-500/10 px-3 py-2 text-[12px] text-blue-300" role="note">
              由 {inventorySourceLabel(managed.source)} 动态管理。名称、分组、端点、认证与来源选项只读；图标、标签、备注及其他用户选项可编辑。
            </div>
          )}
          {kind === "asset" && (
            <div className="nx-form-row">
              <label className="nx-label">类型</label>
              <select
                className="nx-select"
                value={groupKind}
                disabled={initial?.builtin || managed !== null}
                onChange={(e) => {
                  const v = e.target.value as typeof groupKind;
                  setGroupKind(v);
                  setHostError(null);
                  setPortError(null);
                  if (v === "winrm") setPort(5985);
                  else if (v === "mysql") setPort(3306);
                  else if (v === "postgres") setPort(5432);
                  else if (v === "redis") setPort(6379);
                  else setPort(22);
                }}
              >
                {Object.entries(KIND_LABEL)
                  .filter(([k]) => (k !== "local" || !isMobile() || initial?.builtin === true) && (!isInventoryOnlyKind(k) || initial?.kind === k))
                  .map(([k, label]) => (
                    <option key={k} value={k}>
                      {label}
                    </option>
                  ))}
              </select>
              {initial?.builtin && (
                <div className="nx-hint mt-1">
                  ↳ 内置的「当前设备」，始终指向本机，类型不可更改（名字可以改）
                </div>
              )}
            </div>
          )}

          {kind === "asset" && !initial && templates.length > 0 && (
            <div className="nx-form-row">
              <label className="nx-label">模板</label>
              <div className="flex min-w-0 items-center gap-2">
                <select
                  className="nx-select"
                  value={templateId}
                  aria-label="从模板创建"
                  onChange={(e) => {
                    const id = e.target.value;
                    setTemplateId(id);
                    const t = templates.find((x) => x.id === id);
                    if (t) applyTemplate(t);
                  }}
                >
                  <option value="">从模板创建…</option>
                  {templates.map((t) => (
                    <option key={t.id} value={t.id}>
                      {t.name}
                    </option>
                  ))}
                </select>
                {templateId && (
                  <button
                    type="button"
                    className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
                    onClick={deleteTemplate}
                  >
                    删除模板
                  </button>
                )}
              </div>
            </div>
          )}

          <div className="nx-form-row">
            <label className="nx-label">名称</label>
            <input
              ref={nameInputRef}
              className="nx-input"
              value={name}
              disabled={managed !== null}
              onChange={(e) => (kind === "group" ? setName(e.target.value) : syncCredName(e.target.value))}
              placeholder={kind === "group" ? "生产环境" : "web-01"}
            />
          </div>

          {kind === "asset" && (
            <div className="nx-form-row">
              <label className="nx-label">图标</label>
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <button
                    type="button"
                    className="nx-btn nx-btn-outline nx-btn-sm"
                    aria-pressed={iconPickerOpen}
                    onClick={() => setIconPickerOpen((v) => !v)}
                  >
                    <IconPreview size={14} />
                    选择图标
                  </button>
                  {icon && (
                    <button
                      type="button"
                      className="nx-btn nx-btn-ghost nx-btn-sm"
                      onClick={() => {
                        setIcon("");
                        setDirty(true);
                      }}
                    >
                      恢复默认
                    </button>
                  )}
                </div>
                {iconPickerOpen && (
                  <div id="asset-icon-picker" className="mt-2 max-h-44 overflow-y-auto rounded-lg border border-neutral-800/60 p-1.5">
                    <div className="grid grid-cols-8 gap-1">
                      {ASSET_ICON_NAMES.map((name) => {
                        const Icon = assetIcon(groupKind, name);
                        return (
                          <button
                            key={name}
                            type="button"
                            className={`flex h-8 items-center justify-center rounded-md border ${
                              icon === name
                                ? "border-blue-500/60 bg-blue-500/15 text-blue-300"
                                : "border-transparent text-neutral-400 hover:bg-neutral-800/60 hover:text-neutral-100"
                            }`}
                            title={name}
                            aria-label={`选择图标 ${name}`}
                            onClick={() => {
                              setIcon(name);
                              setDirty(true);
                              setIconPickerOpen(false);
                            }}
                          >
                            <Icon size={15} />
                          </button>
                        );
                      })}
                    </div>
                  </div>
                )}
              </div>
            </div>
          )}

          {(kind === "asset" || kind === "group") && (
            <div className="nx-form-row">
              <label className="nx-label">{kind === "group" ? "父分组" : "分组"}</label>
              <div className="flex min-w-0 items-center gap-2">
                <select
                  className="nx-select"
                  value={groupId ?? ""}
                  disabled={managed !== null}
                  onChange={(e) => setGroupId(e.target.value || null)}
                >
                  <option value="">{kind === "group" ? "（根目录）" : "（不分组）"}</option>
                  {groupOptions.map((g) => (
                    <option key={g.id} value={g.id}>
                      {g.path}
                    </option>
                  ))}
                </select>
                {kind === "asset" && !managed && <button
                  type="button"
                  className="nx-btn nx-btn-outline shrink-0"
                  onClick={() => void createGroupInline()}
                >
                  <IconPlus size={11} />
                  新建分组
                </button>}
              </div>
            </div>
          )}

          {kind === "asset" && inventoryOnly && (
            <div className="nx-form-row">
              <span className="nx-label">来源目标</span>
              <div className="rounded-md border border-neutral-800/70 bg-neutral-950/40 px-3 py-2 text-[12px] text-neutral-400">
                <div className="break-all">{initial?.host || inventoryTargetText(initial?.options) || "仅清单记录"}</div>
                <div className="nx-hint mt-1">此类型只用于动态清单展示，终端接入未实现；来源目标由同步更新。</div>
              </div>
            </div>
          )}

          {kind === "asset" && groupKind === "local" && (
            <>
              <div className="nx-form-row">
                <label className="nx-label">默认 Shell</label>
                <input
                  className="nx-input font-mono text-[12px]"
                  value={shell}
                  onChange={(e) => setShell(e.target.value)}
                  placeholder="留空使用系统默认（$SHELL / pwsh）"
                />
              </div>
              <div className="nx-form-row">
                <label className="nx-label">起始目录</label>
                <input
                  className="nx-input font-mono text-[12px]"
                  value={cwd}
                  onChange={(e) => setCwd(e.target.value)}
                  placeholder="留空使用用户主目录"
                />
              </div>
              <div className="nx-hint mb-3">
                ↳ 只对新开的终端生效，已打开的标签不变。本机资产双击即连，不需要凭据。
              </div>
            </>
          )}

          {kind === "asset" && groupKind === "ssh" && (
            <div className="nx-form-row">
              <label className="nx-label" htmlFor={terminalModeId}>终端模式</label>
              <select
                id={terminalModeId}
                className="nx-select"
                value={terminalMode}
                onChange={(e) => setTerminalMode(e.target.value as "auto" | "durable" | "regular")}
              >
                <option value="auto">默认（使用全局设置）</option>
                <option value="durable">持久终端（断开后可在后台接管）</option>
                <option value="regular">普通终端（随连接断开结束）</option>
              </select>
              <div className="nx-hint mt-1.5">
                持久终端首次连接会自动安装助手，安装期间先使用普通终端，完成后新开的标签生效。只影响新开的终端；已有后台会话仍可在「后台会话」接管。
              </div>
            </div>
          )}

          {kind === "asset" && groupKind !== "local" && !inventoryOnly && (
            <>
              <fieldset disabled={managed !== null} className="contents">
              <div className="mb-3 flex gap-2">
                <div className="min-w-0 flex-1">
                  <label className="nx-label" htmlFor={hostInputId}>主机</label>
                  <input
                    id={hostInputId}
                    className="nx-input"
                    value={host}
                    onChange={(e) => onHostChange(e.target.value)}
                    onBlur={onHostBlur}
                    placeholder="1.2.3.4"
                    aria-invalid={hostError ? true : undefined}
                    aria-describedby={hostError ? hostErrorId : undefined}
                  />
                  {hostError && (
                    <p id={hostErrorId} role="alert" className="mt-1 break-words text-[11px] text-red-400">
                      {hostError}
                    </p>
                  )}
                </div>
                <div className="w-[86px] shrink-0">
                  <label className="nx-label" htmlFor={portInputId}>端口</label>
                  <input
                    id={portInputId}
                    type="number"
                    className="nx-input"
                    value={port}
                    onChange={(e) => onPortChange(Number(e.target.value))}
                    onBlur={onPortBlur}
                    aria-invalid={portError ? true : undefined}
                    aria-describedby={portError ? portErrorId : undefined}
                  />
                  {portError && (
                    <p id={portErrorId} role="alert" className="mt-1 break-words text-[11px] text-red-400">
                      {portError}
                    </p>
                  )}
                </div>
              </div>

              <div className="mb-3 flex items-center gap-2">
                <button
                  type="button"
                  className="nx-btn nx-btn-outline nx-btn-sm shrink-0"
                  disabled={!host.trim() || probe.status === "pending"}
                  onClick={() => void runProbe()}
                >
                  {probe.status === "pending" ? (
                    <IconLoader size={12} className="animate-spin" />
                  ) : (
                    <IconPlug size={12} />
                  )}
                  测试端口
                </button>
                {probe.status === "ok" && (
                  <span className="text-[11.5px] text-green-300">端口可达</span>
                )}
                {probe.status === "fail" && (
                  <span className="min-w-0 truncate text-[11.5px] text-red-400" title={probe.error}>
                    端口不可达：{probe.error}
                  </span>
                )}
                <span className="nx-hint ml-auto">只探测端口，不带凭据</span>
              </div>

              <div className="nx-form-row">
                <label className="nx-label">用户名</label>
                <input
                  className="nx-input"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                />
              </div>

              {!isDb && (
                <div className="nx-form-row">
                  <label className="nx-label">认证方式</label>
                  <select
                    className="nx-select"
                    value={authKind}
                    onChange={(e) => setAuthKind(e.target.value)}
                  >
                    <option value="password">密码</option>
                    <option value="keyboard-interactive">键盘交互（密码）</option>
                    <option value="key">私钥文件</option>
                    <option value="agent">SSH Agent</option>
                  </select>
                </div>
              )}

              {isDb && (
                <div className="nx-form-row">
                  <label className="nx-label">通过SSH资产连接</label>
                  <select
                    className="nx-select"
                    aria-label="通过SSH资产连接"
                    value={viaAssetId}
                    onChange={(e) => setViaAssetId(e.target.value)}
                  >
                    <option value="">（直连，不通过SSH）</option>
                    {viaAssetId && !viaCandidates.some((a) => a.id === viaAssetId) && (
                      <option value={viaAssetId}>当前配置的SSH资产（不在可选列表）</option>
                    )}
                    {viaCandidates.map((a) => (
                      <option key={a.id} value={a.id}>
                        {a.name}（{a.host}:{a.port}）
                      </option>
                    ))}
                  </select>
                  <div className="nx-hint mt-1.5">
                    ↳ 默认直连；选择后使用该资产已建立的 SSH 连接，不会自动连接 SSH。资产归属、类型和连接状态仍由后端校验。
                  </div>
                </div>
              )}

              {keyAuth && !isDb && (
                <div className="nx-form-row">
                  <label className="nx-label">私钥来源</label>
                  <div className="nx-segment mb-2">
                    <button
                      type="button"
                      className={`nx-segment-item ${keyOrigin === "vault" ? "is-active" : ""}`}
                      onClick={() => changeKeyOrigin("vault")}
                    >
                      存入凭据库
                    </button>
                    <button
                      type="button"
                      className={`nx-segment-item ${keyOrigin === "ref" ? "is-active" : ""}`}
                      onClick={() => changeKeyOrigin("ref")}
                    >
                      引用文件路径
                    </button>
                  </div>

                  {keyOrigin === "ref" ? (
                    <>
                      <div className="flex gap-1.5">
                        <input
                          className="nx-input font-mono text-[12px]"
                          value={keyPath}
                          onChange={(e) => setKeyPath(e.target.value)}
                          placeholder="选择或输入私钥路径"
                        />
                        {!webFiles && (
                          <button
                            type="button"
                            className="nx-btn nx-btn-outline shrink-0"
                            onClick={() =>
                              void pickKeyFile().then((p) => {
                                if (p) {
                                  setKeyPath(p);
                                  setDirty(true);
                                }
                              })
                            }
                          >
                            浏览…
                          </button>
                        )}
                      </div>
                      <div className="nx-hint mt-1.5">
                        {webFiles
                          ? "↳ Web 模式只能填写服务器上的私钥路径；要引用浏览器侧的私钥文件，请改用「存入凭据库」。"
                          : "↳ 仅保存路径并与系统 ssh 共用私钥文件，私钥内容不存入凭据库；文件移动或删除后，请更新此路径。"}
                      </div>
                    </>
                  ) : (
                    <>
                      <select
                        className="nx-select"
                        value={vaultMode}
                        onChange={(e) => setVaultMode(e.target.value as "new" | "existing")}
                      >
                        <option value="new">新建凭据</option>
                        <option value="existing">选已有私钥凭据</option>
                      </select>

                      {vaultMode === "new" ? (
                        <>
                          <div className="nx-segment mt-2 mb-2">
                            <button
                              type="button"
                              className={`nx-segment-item ${keyContentMode === "file" ? "is-active" : ""}`}
                              onClick={() => {
                                setDirty(true);
                                setKeyContentMode("file");
                              }}
                            >
                              私钥文件
                            </button>
                            <button
                              type="button"
                              className={`nx-segment-item ${keyContentMode === "paste" ? "is-active" : ""}`}
                              onClick={() => {
                                setDirty(true);
                                setKeyContentMode("paste");
                              }}
                            >
                              粘贴内容
                            </button>
                          </div>
                          {keyContentMode === "file" ? (
                            <div className="flex gap-1.5">
                              <input
                                className="nx-input font-mono text-[12px]"
                                value={keyPath}
                                onChange={(e) => changeInlinePath(e.target.value)}
                                placeholder="选择私钥文件"
                              />
                              <button
                                type="button"
                                className="nx-btn nx-btn-outline shrink-0"
                                onClick={() => void pickInline()}
                                disabled={inlinePicker.pending}
                              >
                                浏览…
                              </button>
                            </div>
                          ) : (
                            <textarea
                              className="nx-textarea font-mono text-[11.5px]"
                              rows={4}
                              value={pastedKey}
                              onChange={(e) => setPastedKey(e.target.value)}
                              placeholder={
                                "-----BEGIN OPENSSH PRIVATE KEY-----\n…\n-----END OPENSSH PRIVATE KEY-----"
                              }
                            />
                          )}
                          <div className="nx-hint mt-1.5">
                            {keyContentMode === "file"
                              ? "↳ 保存时读取私钥并加密入库，之后不再依赖原文件。"
                              : "↳ 保存时将粘贴的私钥加密入库。"}
                          </div>
                        </>
                      ) : (
                        <>
                          <select
                            className="nx-select mt-2"
                            value={vaultCredId}
                            onChange={(e) => setVaultCredId(e.target.value)}
                          >
                            <option value="">选择私钥凭据…</option>
                            {privateKeys.map((c) => (
                              <option key={c.id} value={c.id}>
                                {c.name}
                                {c.source === "file" ? "（引用文件）" : "（已入库）"}
                                {c.usedBy.length > 0
                                  ? ` · 被 ${c.usedBy.length} 个资产使用`
                                  : " · 未使用"}
                              </option>
                            ))}
                          </select>
                          {privateKeys.length === 0 && (
                            <div className="nx-hint mt-1.5">
                              ↳ 凭据库里还没有私钥：改成「新建凭据」，或先去左栏「凭据」里建一条。
                            </div>
                          )}
                          {vaultCredId && (
                            <div className="nx-hint mt-1.5">↳ 这条凭据可给多个资产共用；修改后，对所有使用它的资产生效。</div>
                          )}
                        </>
                      )}
                    </>
                  )}

                  <div className="mt-3 border-t border-neutral-800/60 pt-2.5">
                    <label className="nx-label">私钥口令</label>
                    <input
                      type="password"
                      className="nx-input font-mono"
                      autoComplete="off"
                      value={passphrase}
                      disabled={keyOrigin === "vault" && vaultMode === "existing"}
                      onChange={(e) => setPassphrase(e.target.value)}
                      placeholder={
                        keyOrigin === "vault" && vaultMode === "existing"
                          ? "口令随所选凭据管理，请在凭据库中修改"
                          : "无口令时留空"
                      }
                    />
                    <div className="nx-hint mt-1.5">
                      ↳ 私钥设有口令时填写，连接时自动使用。
                      {keyOrigin === "ref" && !passphrase
                        ? " 留空时仅引用本地文件，不保存私钥内容。"
                        : ""}
                    </div>
                  </div>
                </div>
              )}

              {usesCred && (
                <div className="nx-form-row">
                  <label className="nx-label">密码</label>
                  <select
                    className="nx-select"
                    value={credChoice}
                    onChange={(e) => setCredChoice(e.target.value)}
                  >
                    <option value="new">新建凭据</option>
                    {(credentials.data ?? []).map((c) => (
                      <option key={c.id} value={c.id}>
                        {c.name}（{CRED_KIND_LABEL[c.kind] ?? c.kind} ·{" "}
                        {c.usedBy.length > 0 ? `被 ${c.usedBy.length} 个资产使用` : "未使用"}）
                      </option>
                    ))}
                    <option value="none">（不使用凭据）</option>
                  </select>
                  {credChoice === "new" ? (
                    <>
                      <input
                        className="nx-input mt-1.5"
                        placeholder="凭据名称（默认同资产名）"
                        value={credName}
                        onChange={(e) => {
                          credNameTouched.current = true;
                          setCredName(e.target.value);
                        }}
                      />
                      <input
                        type="password"
                        className="nx-input mt-1.5"
                        value={password}
                        autoComplete="off"
                        onChange={(e) => setPassword(e.target.value)}
                        placeholder="密码"
                        aria-label="凭据密码"
                      />
                      <div className="nx-hint mt-1">
                        ↳ 保存后存入凭据库，可改名，其他资产也能选它。
                      </div>
                    </>
                  ) : credChoice !== "none" ? (
                    <div className="nx-hint mt-1">↳ 这条凭据可给多个资产共用；修改后，对所有使用它的资产生效。</div>
                  ) : null}
                </div>
              )}
              </fieldset>

              {(groupKind === "ssh" || groupKind === "docker") && (
                <div className="mt-3 border-t border-neutral-800/60 pt-2.5">
                  <button
                    type="button"
                    className="flex items-center gap-1.5 text-[12px] text-neutral-400 transition-colors hover:text-neutral-100"
                    aria-expanded={advancedOpen}
                    aria-controls={advancedPanelId}
                    onClick={() => setAdvancedOpen((v) => !v)}
                  >
                    {advancedOpen ? (
                      <IconChevronDown size={12} className="shrink-0" />
                    ) : (
                      <IconChevronRight size={12} className="shrink-0" />
                    )}
                    高级选项
                  </button>
                  {advancedOpen && (
                    <div id={advancedPanelId} className="mt-2.5">
                      <div className="nx-form-row">
                        <label className="nx-label">跳板机</label>
                        <select
                          className="nx-select"
                          value={jumpAssetId}
                          disabled={managed !== null}
                          onChange={(e) => setJumpAssetId(e.target.value)}
                        >
                          <option value="">（不使用跳板机）</option>
                          {jumpCandidates.map((a) => (
                            <option key={a.id} value={a.id}>
                              {a.name}（{a.host}:{a.port}）
                            </option>
                          ))}
                        </select>
                        <div className="nx-hint mt-1.5">
                          ↳ 先登录跳板机，再连接目标资产；两者分别验证凭据。
                        </div>
                      </div>

                      <div className="nx-form-row">
                        <label className="nx-label">ProxyCommand</label>
                        <input
                          className="nx-input font-mono text-[12px]"
                          value={proxyCommand}
                          onChange={(e) => setProxyCommand(e.target.value)}
                          placeholder="nc %h %p"
                        />
                        <div className="nx-hint mt-1.5">
                          ↳ 选填。使用外部命令建立连接；%h 为主机，%p 为端口，%% 为百分号。
                        </div>
                      </div>

                      <div className="nx-form-row">
                        <label className="flex cursor-pointer items-start gap-3">
                          <div className="min-w-0 flex-1">
                            <div className="text-[12.5px] text-neutral-200">Agent 转发</div>
                            <p className="nx-hint mt-0.5">
                              默认关闭；开启后，远端进程可请求本机 ssh-agent 签名并继续连接其他主机，私钥不会离开本机。仅对可信主机开启。
                            </p>
                          </div>
                          <input
                            type="checkbox"
                            className="mt-0.5 h-4 w-4 shrink-0"
                            aria-label="Agent 转发"
                            checked={forwardAgent}
                            onChange={(e) => setForwardAgent(e.target.checked)}
                          />
                        </label>
                        {forwardAgent && (
                          <input
                            className="nx-input font-mono text-[12px] mt-1.5"
                            value={agentSocket}
                            onChange={(e) => setAgentSocket(e.target.value)}
                            placeholder="留空使用 $SSH_AUTH_SOCK"
                          />
                        )}
                      </div>

                      {keyAuth && (
                        <div className="nx-form-row">
                          <label className="nx-label">SSH 证书</label>
                          <input
                            className="nx-input font-mono text-[12px]"
                            value={certPath}
                            onChange={(e) => setCertPath(e.target.value)}
                            placeholder="id_ed25519-cert.pub（可选）"
                          />
                          <div className="nx-hint mt-1.5">
                            ↳ 选填。与私钥配对的证书文件路径；不填则按普通私钥登录。
                          </div>
                        </div>
                      )}

                      <div className="mt-3 border-t border-neutral-800/60 pt-2.5">
                        <div className="nx-form-row">
                          <label className="nx-label">启动命令</label>
                          <input
                            className="nx-input font-mono text-[12px]"
                            value={startupCommand}
                            onChange={(e) => setStartupCommand(e.target.value)}
                            placeholder="连接成功后在该终端自动执行"
                          />
                          <div className="nx-hint mt-1.5">
                            ↳ 选填。相当于连接成功后手动输入并回车；输出与报错直接显示在终端里，执行失败不影响连接本身。仅 SSH/Docker
                            资产支持，WinRM 与本地终端不执行。
                          </div>
                        </div>

                        <div className="nx-form-row">
                          <label className="nx-label">终端编码</label>
                          <select
                            className="nx-select"
                            value={encoding}
                            onChange={(e) => setEncoding(e.target.value)}
                          >
                            {TERMINAL_ENCODINGS.map((option) => (
                              <option key={option} value={option}>
                                {option}
                              </option>
                            ))}
                          </select>
                          <div className="nx-hint mt-1.5">
                            ↳ 该资产终端的默认字符编码，连接成功后自动应用；也可在终端「更多终端操作」菜单里临时切换。
                          </div>
                        </div>

                        <div className="nx-form-row">
                          <label className="nx-label">环境变量</label>
                          <textarea
                            className="nx-textarea font-mono text-[11.5px]"
                            rows={3}
                            value={envText}
                            onChange={(e) => setEnvText(e.target.value)}
                            placeholder={"每行一个 KEY=VALUE\n例如：\nLANG=en_US.UTF-8"}
                          />
                          <div className="nx-hint mt-1.5">
                            ↳ 选填。连接时通过 SSH 请求设置这些环境变量；需服务端 sshd 配置 AcceptEnv，被拒绝时不影响连接。
                          </div>
                        </div>
                      </div>
                    </div>
                  )}
                </div>
              )}
            </>
          )}
          {kind === "asset" && (
            <>
              <div className="nx-form-row">
                <label className="nx-label">标签</label>
                <input
                  className="nx-input"
                  value={tags}
                  onChange={(e) => setTags(e.target.value)}
                  placeholder="用逗号分隔，如 production, 欧洲"
                />
                <div className="nx-hint mt-1.5">保存时会去除首尾空格并合并重复标签；已有标签内容不会被后台改写。</div>
              </div>
              <div className="nx-form-row">
                <label className="nx-label">备注</label>
                <textarea
                  className="nx-textarea"
                  rows={3}
                  value={note}
                  onChange={(e) => setNote(e.target.value)}
                  placeholder="备注（可选）"
                />
              </div>
            </>
          )}
          {saveError && (
            <div className="mb-2 flex items-center gap-1.5 text-[12px] text-red-400">
              <IconXCircle size={13} /> {saveError}
            </div>
          )}
        </div>
        <div className="nx-modal-footer shrink-0">
          {kind === "asset" && !initial && (
            <button
              type="button"
              className="nx-btn nx-btn-ghost mr-auto"
              disabled={saving}
              onClick={() => void saveAsTemplate()}
            >
              存为模板
            </button>
          )}
          <button className="nx-btn nx-btn-ghost" onClick={() => void requestClose()} disabled={saving}>
            取消
          </button>
          <button
            className="nx-btn nx-btn-primary"
            disabled={
              saving ||
              !name.trim() ||
              !!hostError ||
              !!portError ||
              (keyAuth &&
                keyOrigin === "vault" &&
                vaultMode === "new" &&
                keyContentMode === "file" &&
                inlinePicker.pending)
            }
            onClick={() => void save()}
          >
            {saving ? "保存中…" : "保存"}
          </button>
        </div>
      </div>
    </div>
    {vaultInitGate}
    </>
  );
}
