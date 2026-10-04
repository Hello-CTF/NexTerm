import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ask, pickKeyFile } from "../../ui/dialogs";
import { assetApi, sessionApi, vaultApi, type Asset, type AssetGroup } from "../../ipc/commands";
import { connectAsset, openCredentialsSidebar, useUi } from "../../app/store";
import { isImeKeyEvent } from "../../ui/DialogHost";
import { ContextMenu, type MenuItem } from "../../ui/ContextMenu";
import { describeError } from "../../ui/errorText";
import { resolveInlineKeyContent, useInlineKeyPicker } from "../credentials/keyStaging";
import { cloneAsset } from "./assetClone";
import { useAssetVisibility } from "./assetVisibility";
import {
  assetIcon,
  IconChevronDown,
  IconChevronRight,
  IconClose,
  IconCommand,
  IconCopy,
  IconEdit,
  IconEye,
  IconEyeOff,
  IconFolder,
  IconKey,
  IconLoader,
  IconPlay,
  IconPlug,
  IconPlus,
  IconSearch,
  IconTrash,
  IconXCircle,
} from "../../ui/icons";
import { SnippetsPanel } from "./SnippetsPanel";

const KIND_LABEL: Record<string, string> = {
  ssh: "SSH (Linux)",
  winrm: "WinRM (Windows)",
  local: "本地终端",
  docker: "Docker 主机",
  mysql: "MySQL",
  redis: "Redis",
};

const DRAG_ASSET = "application/x-nexterm-asset";

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

export function AssetTree() {
  const qc = useQueryClient();
  const { pushToast, leftOpen, leftWidth } = useUi();
  const [query, setQuery] = useState("");
  const [editing, setEditing] = useState<"none" | "asset" | "group">("none");
  const [editingAsset, setEditingAsset] = useState<Asset | null>(null);
  const [presetGroup, setPresetGroup] = useState<string | null>(null);
  const [renamingGroup, setRenamingGroup] = useState<AssetGroup | null>(null);
  const [groupBusyId, setGroupBusyId] = useState<string | null>(null);
  const [groupError, setGroupError] = useState<{ id: string; message: string } | null>(null);
  const [snippetsOpen, setSnippetsOpen] = useState(false);
  const [rowMenu, setRowMenu] = useState<{ x: number; y: number; asset: Asset } | null>(null);
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
  const credentials = useQuery({
    queryKey: ["credentials"],
    queryFn: () => vaultApi.listCredentials(),
    refetchOnWindowFocus: false,
  });
  const snippets = useQuery({
    queryKey: ["snippets"],
    queryFn: () => assetApi.snippetList(),
    refetchOnWindowFocus: false,
  });
  const results = useQuery({
    queryKey: ["asset-search", query],
    queryFn: () => assetApi.search(query),
    enabled: query.trim().length > 0,
  });

  const filtered = query.trim() ? (results.data ?? []) : (assets.data ?? []);
  const isHidden = (id: string) => hiddenIds.includes(id);
  const visible = filtered.filter((a) => showHidden || !isHidden(a.id));

  const byGroup = useMemo(() => {
    const map = new Map<string | null, Asset[]>();
    for (const a of visible) {
      const list = map.get(a.groupId) ?? [];
      list.push(a);
      map.set(a.groupId, list);
    }
    return map;
  }, [visible]);

  const onClone = async (a: Asset) => {
    try {
      const created = await cloneAsset(a);
      if (!created) return;
      void qc.invalidateQueries({ queryKey: ["assets"] });
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

  const onDelete = async (a: Asset) => {
    const ok = await ask(`删除资产「${a.name}」？软删除，可恢复；关联凭据保留。`, {
      kind: "info",
    });
    if (!ok) return;
    await assetApi.delete(a.id);
    unhide(a.id);
    void qc.invalidateQueries({ queryKey: ["assets"] });
    void qc.invalidateQueries({ queryKey: ["credentials"] });
    pushToast("info", "已删除");
  };

  const moveAsset = async (assetId: string, groupId: string | null) => {
    try {
      await assetApi.update({ id: assetId, groupId });
      void qc.invalidateQueries({ queryKey: ["assets"] });
      pushToast("info", groupId ? "已移入分组" : "已移出分组");
    } catch (e) {
      pushToast("error", `移动失败：${describeError(e)}`);
    }
  };

  const runDeleteGroup = async (g: AssetGroup) => {
    setGroupBusyId(g.id);
    setGroupError(null);
    try {
      await assetApi.groupDelete(g.id);
      void qc.invalidateQueries({ queryKey: ["groups"] });
      void qc.invalidateQueries({ queryKey: ["assets"] });
      pushToast("info", "已删除分组");
    } catch (e) {
      setGroupError({ id: g.id, message: describeError(e) });
    } finally {
      setGroupBusyId(null);
    }
  };

  const deleteGroup = async (g: AssetGroup) => {
    if (groupBusyId) return;
    const ok = await ask(`删除分组「${g.name}」？\n组内资产会移到「未分组」；子分组会被一并删除。`, {
      kind: "warning",
    });
    if (!ok) return;
    await runDeleteGroup(g);
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
            title={showHidden ? "隐藏已隐藏的资产" : `显示已隐藏的资产（${hiddenIds.length}）`}
            aria-label={showHidden ? "隐藏已隐藏的资产" : "显示已隐藏的资产"}
            onClick={() => setShowHidden(!showHidden)}
          >
            {showHidden ? <IconEyeOff size={14} /> : <IconEye size={14} />}
          </button>
        )}
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
      </div>

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
            onChange={(e) => setQuery(e.target.value)}
          />
        </div>
      </div>

      <div
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
      >
        {(byGroup.get(null) ?? []).map((a) => (
          <AssetRow
            key={a.id}
            asset={a}
            level={1}
            hidden={isHidden(a.id)}
            onDelete={() => void onDelete(a)}
            onEdit={() => setEditingAsset(a)}
            onMore={(x, y) => setRowMenu({ x, y, asset: a })}
          />
        ))}
        {(groups.data ?? []).map((g: AssetGroup) => (
          <GroupNode
            key={g.id}
            group={g}
            assets={byGroup.get(g.id) ?? []}
            hiddenIds={hiddenIds}
            onDelete={(a) => void onDelete(a)}
            onEdit={(a) => setEditingAsset(a)}
            onMore={(a, x, y) => setRowMenu({ x, y, asset: a })}
            onMoveAsset={(assetId, groupId) => void moveAsset(assetId, groupId)}
            onCreateIn={() => {
              setPresetGroup(g.id);
              setEditing("asset");
            }}
            busy={groupBusyId === g.id}
            error={groupError?.id === g.id ? groupError.message : null}
            onRename={() => setRenamingGroup(g)}
            onDeleteGroup={() => void deleteGroup(g)}
            onRetry={() => void runDeleteGroup(g)}
            onDismissError={() => setGroupError(null)}
          />
        ))}
        {visible.length === 0 && (
          <div className="nx-hint px-2 py-8 text-center">
            {query
              ? "没有匹配的资产"
              : (assets.data?.length ?? 0) > 0
                ? "资产都隐藏了 — 点右上角眼睛按钮显示"
                : "还没有资产 — 点右上角的 + 新建一个"}
          </div>
        )}
      </div>

      <button
        className="mx-1.5 mb-1 mt-auto flex h-[32px] shrink-0 items-center gap-2 rounded-md border border-transparent px-2.5 text-[12.5px] text-neutral-400 transition-colors hover:bg-white/[.05] hover:text-neutral-100"
        title="命令片段（插入当前终端）"
        onClick={() => setSnippetsOpen(true)}
      >
        <IconCommand size={14} className="shrink-0 text-neutral-500" />
        <span className="flex-1 text-left">命令片段</span>
        <span className="nx-count">{snippets.data?.length ?? "…"}</span>
        <IconChevronRight size={12} className="shrink-0 text-neutral-600" />
      </button>

      <button
        className="mx-1.5 mb-1.5 flex h-[32px] shrink-0 items-center gap-2 rounded-md border border-transparent border-t-neutral-800/60 px-2.5 text-[12.5px] text-neutral-400 transition-colors hover:bg-white/[.05] hover:text-neutral-100"
        title="凭据库（左栏查看）"
        onClick={openCredentialsSidebar}
      >
        <IconKey size={14} className="shrink-0 text-neutral-500" />
        <span className="flex-1 text-left">凭据库</span>
        <span className="nx-count">{credentials.data?.length ?? "…"}</span>
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
            void qc.invalidateQueries({ queryKey: ["credentials"] });
          }}
        />
      )}
      {renamingGroup && (
        <GroupRenameDialog
          group={renamingGroup}
          onClose={() => setRenamingGroup(null)}
          onRenamed={() => {
            setRenamingGroup(null);
            void qc.invalidateQueries({ queryKey: ["groups"] });
          }}
        />
      )}
      {snippetsOpen && <SnippetsPanel onClose={() => setSnippetsOpen(false)} />}
      <ContextMenu
        state={
          rowMenu
            ? {
                x: rowMenu.x,
                y: rowMenu.y,
                title: rowMenu.asset.name,
                items: [
                  {
                    kind: "item",
                    label: "克隆",
                    icon: <IconCopy size={13} />,
                    hint: "共享凭据引用",
                    onSelect: () => void onClone(rowMenu.asset),
                  },
                  isHidden(rowMenu.asset.id)
                    ? {
                        kind: "item",
                        label: "取消隐藏",
                        icon: <IconEye size={13} />,
                        onSelect: () => onUnhide(rowMenu.asset),
                      }
                    : {
                        kind: "item",
                        label: "隐藏",
                        icon: <IconEyeOff size={13} />,
                        hint: "仅界面隐藏",
                        onSelect: () => onHide(rowMenu.asset),
                      },
                ] satisfies MenuItem[],
              }
            : null
        }
        onClose={() => setRowMenu(null)}
      />
    </div>
  );
}

function AssetRow({
  asset,
  level,
  hidden,
  onDelete,
  onEdit,
  onMore,
}: {
  asset: Asset;
  level: number;
  hidden?: boolean;
  onDelete: () => void;
  onEdit: () => void;
  onMore: (x: number, y: number) => void;
}) {
  const Icon = assetIcon(asset.kind);
  const label = asset.name;
  const target = asset.host ? `${asset.host}${asset.port ? `:${asset.port}` : ""}` : "";
  const tip = [
    asset.name,
    target || (asset.builtin ? "本机" : ""),
    asset.builtin ? "内置资产 · 不可删除" : "",
    hidden ? "已隐藏" : "",
    "双击连接 · 可拖入分组",
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <div
      role="treeitem"
      aria-level={level}
      tabIndex={0}
      className={`nx-row group focus-within:[&_.nx-row-actions]:flex ${hidden ? "opacity-55" : ""}`}
      draggable
      onDragStart={(e) => {
        e.dataTransfer.setData(DRAG_ASSET, asset.id);
        e.dataTransfer.effectAllowed = "move";
      }}
      onDoubleClick={() => void connectAsset(asset)}
      onContextMenu={(e) => {
        e.preventDefault();
        onMore(e.clientX, e.clientY);
      }}
      onKeyDown={(e) => {
        if (e.target !== e.currentTarget) return;
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          void connectAsset(asset);
          return;
        }
        treeRowKeyDown(e);
      }}
      title={tip}
    >
      <Icon size={14} className="shrink-0 text-neutral-500" />
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {hidden ? (
        <span className="nx-badge">已隐藏</span>
      ) : (
        target && <span className="shrink-0 text-[11px] text-neutral-500">{target}</span>
      )}
      {asset.builtin && <span className="nx-badge nx-badge-blue">本机</span>}
      <span className="nx-row-actions [@media(pointer:coarse)]:flex">
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
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="编辑"
          aria-label={`编辑 ${asset.name}`}
          onClick={(e) => {
            e.stopPropagation();
            onEdit();
          }}
        >
          <IconEdit size={12} />
        </button>
        {!asset.builtin && (
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
          className="nx-icon-btn nx-icon-btn-sm"
          title="更多操作"
          aria-label={`更多操作 ${asset.name}`}
          onClick={(e) => {
            e.stopPropagation();
            const r = e.currentTarget.getBoundingClientRect();
            onMore(r.left, r.bottom);
          }}
        >
          ⋯
        </button>
      </span>
    </div>
  );
}

function GroupNode({
  group,
  assets,
  hiddenIds,
  onDelete,
  onEdit,
  onMore,
  onMoveAsset,
  onCreateIn,
  busy,
  error,
  onRename,
  onDeleteGroup,
  onRetry,
  onDismissError,
}: {
  group: AssetGroup;
  assets: Asset[];
  hiddenIds: string[];
  onDelete: (a: Asset) => void;
  onEdit: (a: Asset) => void;
  onMore: (a: Asset, x: number, y: number) => void;
  onMoveAsset: (assetId: string, groupId: string | null) => void;
  onCreateIn: () => void;
  busy: boolean;
  error: string | null;
  onRename: () => void;
  onDeleteGroup: () => void;
  onRetry: () => void;
  onDismissError: () => void;
}) {
  const [open, setOpen] = useState(true);
  const [over, setOver] = useState(false);
  const isAssetDrag = (e: React.DragEvent) =>
    e.dataTransfer.types.includes(DRAG_ASSET);
  return (
    <div
      role="treeitem"
      aria-expanded={open}
      aria-level={1}
      tabIndex={0}
      className={`mb-0.5 rounded focus-within:[&_.nx-row-actions]:flex ${over ? "bg-sky-500/10 ring-1 ring-inset ring-sky-500/40" : ""}`}
      onKeyDown={(e) => {
        if (e.target !== e.currentTarget) return;
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          setOpen((v) => !v);
          return;
        }
        if (e.key === "ArrowRight") {
          if (!open) {
            e.preventDefault();
            setOpen(true);
          }
          return;
        }
        if (e.key === "ArrowLeft") {
          if (open) {
            e.preventDefault();
            setOpen(false);
          }
          return;
        }
        treeRowKeyDown(e);
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
      <div className="nx-row group w-full text-neutral-400">
        <button
          className="flex min-w-0 flex-1 items-center gap-1 text-left"
          onClick={() => setOpen((v) => !v)}
        >
          {open ? (
            <IconChevronDown size={12} className="shrink-0" />
          ) : (
            <IconChevronRight size={12} className="shrink-0" />
          )}
          <IconFolder size={13} className="shrink-0 text-neutral-500" />
          <span className="min-w-0 flex-1 truncate text-xs font-medium">{group.name}</span>
        </button>
        <span className="nx-count">{assets.length}</span>
        <span className="nx-row-actions [@media(pointer:coarse)]:flex">
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
      {open && (
        <div role="group" className="ml-3.5">
          {assets.map((a) => (
            <AssetRow
              key={a.id}
              asset={a}
              level={2}
              hidden={hiddenIds.includes(a.id)}
              onDelete={() => onDelete(a)}
              onEdit={() => onEdit(a)}
              onMore={(x, y) => onMore(a, x, y)}
            />
          ))}
          {assets.length === 0 && (
            <div className="px-2 py-1.5 text-[11px] text-neutral-600">（空分组 · 可拖资产进来）</div>
          )}
        </div>
      )}
    </div>
  );
}

function GroupRenameDialog({
  group,
  onClose,
  onRenamed,
}: {
  group: AssetGroup;
  onClose: () => void;
  onRenamed: () => void;
}) {
  const pushToast = useUi((s) => s.pushToast);
  const [name, setName] = useState(group.name);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const save = async () => {
    if (saving) return;
    const next = name.trim();
    if (!next) {
      setError("分组名不能为空");
      return;
    }
    if (next === group.name) {
      onClose();
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await assetApi.groupUpdate(group.id, next);
      pushToast("success", "已重命名");
      onRenamed();
    } catch (e) {
      setError(describeError(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="nx-overlay" onClick={onClose}>
      <div className="nx-modal flex max-w-[340px] flex-col" onClick={(e) => e.stopPropagation()}>
        <div className="nx-modal-header shrink-0">
          <span className="text-[13px] font-semibold text-neutral-100">重命名分组</span>
        </div>
        <div className="nx-modal-body min-h-0 flex-1 overflow-y-auto">
          <div className="nx-form-row">
            <label className="nx-label">名称</label>
            <input
              className="nx-input"
              value={name}
              autoFocus
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !isImeKeyEvent(e)) void save();
              }}
            />
          </div>
          {error && (
            <div className="mb-2 flex items-center gap-1.5 text-[12px] text-red-400">
              <IconXCircle size={13} /> {error}
            </div>
          )}
        </div>
        <div className="nx-modal-footer shrink-0">
          <button className="nx-btn nx-btn-ghost" onClick={onClose} disabled={saving}>
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
  passphrase: "私钥口令",
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
  onSaved: () => void;
}) {
  const [name, setName] = useState(initial?.name ?? "");
  const [groupKind, setGroupKind] = useState<
    "ssh" | "winrm" | "local" | "docker" | "mysql" | "redis"
  >((initial?.kind as "ssh") ?? "ssh");
  const [groupId, setGroupId] = useState<string | null>(initial?.groupId ?? presetGroupId ?? null);
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
  const groups = useQuery({
    queryKey: ["groups"],
    queryFn: () => assetApi.groupList(),
    refetchOnWindowFocus: false,
  });

  const boundCred = credentials.data?.find((c) => c.id === initial?.credId);
  const boundIsVaultKey = boundCred?.kind === "private_key";
  const boundIsRefKey = boundIsVaultKey && boundCred?.source === "file";
  const privateKeys = (credentials.data ?? []).filter((c) => c.kind === "private_key");

  const [keyOrigin, setKeyOrigin] = useState<"ref" | "vault">(
    boundIsRefKey ? "ref" : boundIsVaultKey ? "vault" : "ref",
  );
  const [vaultMode, setVaultMode] = useState<"new" | "existing">(
    boundIsVaultKey && !boundIsRefKey ? "existing" : "new",
  );
  const [keyContentMode, setKeyContentMode] = useState<"file" | "paste">("file");
  const [inlineKeyContent, setInlineKeyContent] = useState<string | null>(null);
  const [pastedKey, setPastedKey] = useState("");
  const [passphrase, setPassphrase] = useState("");
  const [vaultCredId, setVaultCredId] = useState(boundIsVaultKey ? (initial?.credId ?? "") : "");

  useEffect(() => {
    if (!keyPath && boundIsRefKey && boundCred?.refPath) setKeyPath(boundCred.refPath);
  }, [boundIsRefKey, boundCred?.refPath]);

  const isDb = groupKind === "mysql" || groupKind === "redis";
  const keyAuth = authKind === "key";
  const usesCred = isDb || authKind === "password";
  const credKind = "password";
  const hasBoundPassphrase = boundCred?.kind === "passphrase";

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
    } catch (e) {
      pushToast("error", describeError(e));
    }
  };
  const changeKeyOrigin = (next: "ref" | "vault") => {
    inlinePicker.invalidate();
    if (next === "ref" && inlineKeyContent !== null) {
      setKeyPath("");
      setInlineKeyContent(null);
    }
    setKeyOrigin(next);
  };
  const changeInlinePath = (next: string) => {
    inlinePicker.invalidate();
    setKeyPath(next);
    setInlineKeyContent(null);
  };

  const save = async () => {
    if (savingRef.current) return;
    savingRef.current = true;
    setSaving(true);
    setSaveError(null);
    try {
      if (kind === "group") {
        await assetApi.groupCreate(name);
        onSaved();
        return;
      }
      let finalKeyPath: string | null = null;
      let credId: string | null = null;
      const reuseCredId = boundIsVaultKey ? (initial?.credId ?? undefined) : undefined;
      const credLabel = credName.trim() || name.trim();
      if (keyAuth) {
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
          const res = await vaultApi.setCredential(credLabel, "private_key", content, {
            id: reuseCredId,
            source: "inline",
            ...(passphrase ? { passphrase } : {}),
          });
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
          credId = res.id;
        }
      } else if (credChoice !== "none") {
        credId = credChoice;
      }
      const options =
        groupKind === "local"
          ? {
              ...(shell.trim() ? { shell: shell.trim() } : {}),
              ...(cwd.trim() ? { cwd: cwd.trim() } : {}),
            }
          : undefined;
      const fields = {
        kind: groupKind,
        name,
        groupId,
        host: groupKind === "local" ? null : host,
        port: groupKind === "local" ? null : Number(port),
        username: groupKind === "local" ? null : username,
        authKind: groupKind === "local" ? "none" : authKind,
        keyPath: keyAuth ? finalKeyPath : null,
        credId,
        options,
      };
      if (initial) {
        await assetApi.update({ id: initial.id, ...fields });
      } else {
        await assetApi.create(fields);
      }
      onSaved();
    } catch (e) {
      setSaveError(describeError(e));
    } finally {
      savingRef.current = false;
      setSaving(false);
    }
  };

  return (
    <div className="nx-overlay" onClick={onClose}>
      <div className="nx-modal flex max-w-[380px] flex-col" onClick={(e) => e.stopPropagation()}>
        <div className="nx-modal-header shrink-0">
          <span className="text-[13px] font-semibold text-neutral-100">
            {kind === "group" ? "新建分组" : initial ? "编辑资产" : "新建资产"}
          </span>
        </div>
        <div className="nx-modal-body min-h-0 flex-1 overflow-y-auto">
          {kind === "asset" && (
            <div className="nx-form-row">
              <label className="nx-label">类型</label>
              <select
                className="nx-select"
                value={groupKind}
                disabled={initial?.builtin}
                onChange={(e) => {
                  const v = e.target.value as typeof groupKind;
                  setGroupKind(v);
                  if (v === "winrm") setPort(5985);
                  else if (v === "mysql") setPort(3306);
                  else if (v === "redis") setPort(6379);
                  else setPort(22);
                }}
              >
                {Object.entries(KIND_LABEL).map(([k, label]) => (
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

          <div className="nx-form-row">
            <label className="nx-label">名称</label>
            <input
              className="nx-input"
              value={name}
              onChange={(e) => (kind === "group" ? setName(e.target.value) : syncCredName(e.target.value))}
              placeholder="web-01"
            />
          </div>

          {kind === "asset" && (
            <div className="nx-form-row">
              <label className="nx-label">分组</label>
              <select
                className="nx-select"
                value={groupId ?? ""}
                onChange={(e) => setGroupId(e.target.value || null)}
              >
                <option value="">（不分组）</option>
                {(groups.data ?? []).map((g) => (
                  <option key={g.id} value={g.id}>
                    {g.name}
                  </option>
                ))}
              </select>
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
                  placeholder="留空用系统默认（$SHELL / pwsh）"
                />
              </div>
              <div className="nx-form-row">
                <label className="nx-label">起始目录</label>
                <input
                  className="nx-input font-mono text-[12px]"
                  value={cwd}
                  onChange={(e) => setCwd(e.target.value)}
                  placeholder="留空用家目录"
                />
              </div>
              <div className="nx-hint mb-3">
                ↳ 只对「新开」的终端生效，已经开着的标签不会跟着变。
                本机资产双击即连（无需凭据），终端 / 文件树 / 容器面板都落在本机。
              </div>
            </>
          )}

          {kind === "asset" && groupKind !== "local" && (
            <>
              <div className="mb-3 flex gap-2">
                <div className="min-w-0 flex-1">
                  <label className="nx-label">主机</label>
                  <input
                    className="nx-input"
                    value={host}
                    onChange={(e) => setHost(e.target.value)}
                    placeholder="1.2.3.4"
                  />
                </div>
                <div className="w-[86px] shrink-0">
                  <label className="nx-label">端口</label>
                  <input
                    type="number"
                    className="nx-input"
                    value={port}
                    onChange={(e) => setPort(Number(e.target.value))}
                  />
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
                  测试连接
                </button>
                {probe.status === "ok" && (
                  <span className="text-[11.5px] text-emerald-400">端口可达</span>
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
                    <option value="key">私钥文件</option>
                    <option value="agent">SSH Agent</option>
                  </select>
                </div>
              )}

              {keyAuth && !isDb && (
                <div className="nx-form-row">
                  <label className="nx-label">私钥来源</label>
                  <div className="nx-segment mb-2">
                    <button
                      type="button"
                      className={`nx-segment-item ${keyOrigin === "ref" ? "is-active" : ""}`}
                      onClick={() => changeKeyOrigin("ref")}
                    >
                      引用本地文件
                    </button>
                    <button
                      type="button"
                      className={`nx-segment-item ${keyOrigin === "vault" ? "is-active" : ""}`}
                      onClick={() => changeKeyOrigin("vault")}
                    >
                      存入凭据库
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
                        <button
                          type="button"
                          className="nx-btn nx-btn-outline shrink-0"
                          onClick={() =>
                            void pickKeyFile().then((p) => {
                              if (p) setKeyPath(p);
                            })
                          }
                        >
                          浏览…
                        </button>
                      </div>
                      <div className="nx-hint mt-1.5">
                        ↳ 只记路径，私钥正文不复制进库 —— 跟系统 ssh 用同一份文件。
                        文件被挪走后连不上，回来改这里即可。
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
                              onClick={() => setKeyContentMode("file")}
                            >
                              私钥文件
                            </button>
                            <button
                              type="button"
                              className={`nx-segment-item ${keyContentMode === "paste" ? "is-active" : ""}`}
                              onClick={() => setKeyContentMode("paste")}
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
                            ↳ 保存时读取内容并加密入库，之后不再依赖原文件。
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
                            <div className="nx-hint mt-1.5">↳ 共用凭据 · 改一处，全部生效</div>
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
                          ? "口令跟着所选凭据，改它请去凭据库"
                          : hasBoundPassphrase
                            ? "已设置口令 · 留空保持不变"
                            : "没有就留空"
                      }
                    />
                    <div className="nx-hint mt-1.5">
                      ↳ 选填。口令跟私钥存在同一条凭据里，不会单独占一条。
                      {keyOrigin === "ref" && !passphrase && !hasBoundPassphrase
                        ? " 引用模式不填口令时，凭据库里不会留任何东西。"
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
                      />
                      <div className="nx-hint mt-1">
                        ↳ 保存后进入「凭据」库，可改名与复用
                      </div>
                    </>
                  ) : credChoice !== "none" ? (
                    <div className="nx-hint mt-1">↳ 共用凭据 · 改一处，全部生效</div>
                  ) : null}
                </div>
              )}
            </>
          )}
          {saveError && (
            <div className="mb-2 flex items-center gap-1.5 text-[12px] text-red-400">
              <IconXCircle size={13} /> {saveError}
            </div>
          )}
        </div>
        <div className="nx-modal-footer shrink-0">
          <button className="nx-btn nx-btn-ghost" onClick={onClose} disabled={saving}>
            取消
          </button>
          <button
            className="nx-btn nx-btn-primary"
            disabled={
              saving ||
              !name.trim() ||
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
  );
}
