// 资产树（M1-T1）：分组 + 资产 + 搜索 + 双击连接 + 右键菜单。
//
// UI 要点：类型图标来自统一映射表（不再用 emoji）；行内操作只在 hover 时出现；
// 分组用 chevron + 计数徽章；折叠状态由外壳的图标栏控制，这里不再自带展开按钮。
import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ask, pickKeyFile } from "../../ui/dialogs";
import { assetApi, vaultApi, type Asset, type AssetGroup } from "../../ipc/commands";
import { connectAsset, openCredentialsSidebar, useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import { pickInlineKeyFile, resolveInlineKeyContent } from "../credentials/keyStaging";
import {
  assetIcon,
  IconChevronDown,
  IconChevronRight,
  IconClose,
  IconEdit,
  IconFolder,
  IconKey,
  IconPlay,
  IconPlus,
  IconSearch,
} from "../../ui/icons";

/** 资产类型 → 中文名（新建弹窗与提示里用）。 */
const KIND_LABEL: Record<string, string> = {
  ssh: "SSH (Linux)",
  winrm: "WinRM (Windows)",
  local: "本地终端",
  docker: "Docker 主机",
  mysql: "MySQL",
  redis: "Redis",
};

/** 拖拽资产用的 dataTransfer 类型（自定义 MIME，避免普通文本拖拽误触发）。 */
const DRAG_ASSET = "application/x-nexterm-asset";

export function AssetTree() {
  const qc = useQueryClient();
  const { pushToast, leftOpen, leftWidth } = useUi();
  const [query, setQuery] = useState("");
  const [editing, setEditing] = useState<"none" | "asset" | "group">("none");
  const [editingAsset, setEditingAsset] = useState<Asset | null>(null);
  /** 从分组行上的 + 新建时 preset 为该分组；顶栏 + = 不分组。 */
  const [presetGroup, setPresetGroup] = useState<string | null>(null);

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
  // 凭据数量徽章与表单里的「选已有凭据」共用这一份缓存
  const credentials = useQuery({
    queryKey: ["credentials"],
    queryFn: () => vaultApi.listCredentials(),
    refetchOnWindowFocus: false,
  });
  const results = useQuery({
    queryKey: ["asset-search", query],
    queryFn: () => assetApi.search(query),
    enabled: query.trim().length > 0,
  });

  const filtered = query.trim() ? (results.data ?? []) : (assets.data ?? []);

  const byGroup = useMemo(() => {
    const map = new Map<string | null, Asset[]>();
    for (const a of filtered) {
      const list = map.get(a.groupId) ?? [];
      list.push(a);
      map.set(a.groupId, list);
    }
    return map;
  }, [filtered]);

  const onDelete = async (a: Asset) => {
    const ok = await ask(`删除资产「${a.name}」？软删除，可恢复；关联凭据保留。`);
    if (!ok) return;
    await assetApi.delete(a.id);
    void qc.invalidateQueries({ queryKey: ["assets"] });
    void qc.invalidateQueries({ queryKey: ["credentials"] });
    pushToast("info", "已删除");
  };

  /** 拖拽落点：移入分组 / 移回未分组。groupId 传 null = 拖到根区域。 */
  const moveAsset = async (assetId: string, groupId: string | null) => {
    try {
      await assetApi.update({ id: assetId, groupId });
      void qc.invalidateQueries({ queryKey: ["assets"] });
      pushToast("info", groupId ? "已移入分组" : "已移到未分组");
    } catch (e) {
      pushToast("error", `移动失败：${describeError(e)}`);
    }
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
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="新建资产"
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
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
        </div>
      </div>

      {/* 根区域本身是「未分组」的放置目标：拖到空白处即移出分组 */}
      <div
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
            onDelete={() => void onDelete(a)}
            onEdit={() => setEditingAsset(a)}
          />
        ))}
        {(groups.data ?? []).map((g: AssetGroup) => (
          <GroupNode
            key={g.id}
            group={g}
            assets={byGroup.get(g.id) ?? []}
            onDelete={(a) => void onDelete(a)}
            onEdit={(a) => setEditingAsset(a)}
            onMoveAsset={(assetId, groupId) => void moveAsset(assetId, groupId)}
            onCreateIn={() => {
              setPresetGroup(g.id);
              setEditing("asset");
            }}
          />
        ))}
        {filtered.length === 0 && (
          <div className="nx-hint px-2 py-8 text-center">
            {query ? "没有匹配的资产" : "还没有资产 — 点右上角的 + 新建一个"}
          </div>
        )}
      </div>

      {/* 凭据入口：常驻底部。凭据在语义上属于资产，入口放资产区；
          点击后左栏整体切成「凭据」形态（凭据有自己的侧边栏，不再占主区标签） */}
      <button
        className="mx-1.5 mb-1.5 mt-auto flex h-[32px] shrink-0 items-center gap-2 rounded-md border border-transparent border-t-neutral-800/60 px-2.5 text-[12.5px] text-neutral-400 transition-colors hover:bg-white/[.05] hover:text-neutral-100"
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
    </div>
  );
}

function AssetRow({
  asset,
  onDelete,
  onEdit,
}: {
  asset: Asset;
  onDelete: () => void;
  onEdit: () => void;
}) {
  const Icon = assetIcon(asset.kind);
  const label = asset.name;
  const target = asset.host ? `${asset.host}${asset.port ? `:${asset.port}` : ""}` : "";
  const tip = [
    asset.name,
    target || (asset.builtin ? "本机" : ""),
    asset.builtin ? "内置资产 · 不可删除" : "",
    "双击连接 · 可拖入分组",
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <div
      className="nx-row group"
      draggable
      onDragStart={(e) => {
        e.dataTransfer.setData(DRAG_ASSET, asset.id);
        e.dataTransfer.effectAllowed = "move";
      }}
      onDoubleClick={() => void connectAsset(asset)}
      title={tip}
    >
      <Icon size={14} className="shrink-0 text-neutral-500" />
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {target && <span className="shrink-0 text-[11px] text-neutral-500">{target}</span>}
      {/* 内置的「当前设备」：标出来，并且不给删除按钮 ——
          它是「本机」这个概念的锚点，删掉之后终端 / 容器 / 文件树都没了落脚点。
          内核同样拒绝删除（命令层能被脚本直接调），这里只是不给点。 */}
      {asset.builtin && <span className="nx-badge nx-badge-blue">本机</span>}
      <span className="nx-row-actions">
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="连接"
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
            onClick={(e) => {
              e.stopPropagation();
              onDelete();
            }}
          >
            <IconClose size={12} />
          </button>
        )}
      </span>
    </div>
  );
}

function GroupNode({
  group,
  assets,
  onDelete,
  onEdit,
  onMoveAsset,
  onCreateIn,
}: {
  group: AssetGroup;
  assets: Asset[];
  onDelete: (a: Asset) => void;
  onEdit: (a: Asset) => void;
  onMoveAsset: (assetId: string, groupId: string | null) => void;
  onCreateIn: () => void;
}) {
  const [open, setOpen] = useState(true);
  const [over, setOver] = useState(false);
  const isAssetDrag = (e: React.DragEvent) =>
    e.dataTransfer.types.includes(DRAG_ASSET);
  return (
    <div
      className={`mb-0.5 rounded ${over ? "bg-sky-500/10 ring-1 ring-inset ring-sky-500/40" : ""}`}
      onDragOver={(e) => {
        if (isAssetDrag(e)) {
          e.preventDefault();
          setOver(true);
        }
      }}
      onDragLeave={(e) => {
        // 子元素之间移动也会触发 dragleave，只有真正离开分组块才熄高亮
        if (!e.currentTarget.contains(e.relatedTarget as Node)) setOver(false);
      }}
      onDrop={(e) => {
        if (!isAssetDrag(e)) return;
        e.preventDefault();
        e.stopPropagation(); // 别冒泡给根区域（= 未分组）
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
        <span className="nx-row-actions">
          <button
            className="nx-icon-btn nx-icon-btn-sm"
            title="在分组内新建资产"
            onClick={(e) => {
              e.stopPropagation();
              onCreateIn();
            }}
          >
            <IconPlus size={12} />
          </button>
        </span>
      </div>
      {open && (
        <div className="ml-3.5">
          {assets.map((a) => (
            <AssetRow key={a.id} asset={a} onDelete={() => onDelete(a)} onEdit={() => onEdit(a)} />
          ))}
          {assets.length === 0 && (
            <div className="px-2 py-1.5 text-[11px] text-neutral-600">（空分组 · 可拖资产进来）</div>
          )}
        </div>
      )}
    </div>
  );
}

/** 凭据选择：new = 新建一条；none = 不绑定；其余值 = 已有凭据的 id。 */
type CredChoice = "new" | "none" | string;

const CRED_KIND_LABEL: Record<string, string> = {
  password: "密码",
  passphrase: "私钥口令",
  private_key: "私钥",
  api_key: "API Key",
};

function AssetEditor({
  kind,
  initial,
  presetGroupId,
  onClose,
  onSaved,
}: {
  kind: "asset" | "group";
  /** 编辑已有资产（P1）。不传 = 新建。 */
  initial?: Asset;
  /** 新建时的目标分组（分组行上的 + 进入）。 */
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
  /** 本机资产的两个启动选项（存进 options，连接时由内核读取）。空 = 用系统默认。 */
  const [shell, setShell] = useState(
    typeof initial?.options?.shell === "string" ? (initial.options.shell as string) : "",
  );
  const [cwd, setCwd] = useState(
    typeof initial?.options?.cwd === "string" ? (initial.options.cwd as string) : "",
  );
  const pushToast = useUi((s) => s.pushToast);

  // 凭据绑定：默认跟随已有绑定；没有就「新建凭据」
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

  /** 编辑时已绑定的凭据：它的 kind 与来源决定私钥区块怎么回显。 */
  const boundCred = credentials.data?.find((c) => c.id === initial?.credId);
  const boundIsVaultKey = boundCred?.kind === "private_key";
  /** 已绑定的是「引用型」私钥凭据（库里只有路径，没有正文）。 */
  const boundIsRefKey = boundIsVaultKey && boundCred?.source === "file";
  /** 可选的私钥凭据（入库模式的「选已有凭据」用）。 */
  const privateKeys = (credentials.data ?? []).filter((c) => c.kind === "private_key");

  /**
   * 私钥的两个正交选择：
   *   来源 = 「引用本地文件」（只记路径）或「存入凭据库」（读内容加密保存）；
   *   口令 = 私钥的一个属性，两种来源都能填，跟私钥存进同一条凭据。
   * 编辑已有资产时按已绑定凭据的形态回显（引用型 → 引用；入库型 → 入库+选已有）。
   */
  const [keyOrigin, setKeyOrigin] = useState<"ref" | "vault">(
    boundIsRefKey ? "ref" : boundIsVaultKey ? "vault" : "ref",
  );
  /** 入库模式下：新建一条凭据，还是共选已有。 */
  const [vaultMode, setVaultMode] = useState<"new" | "existing">(
    boundIsVaultKey && !boundIsRefKey ? "existing" : "new",
  );
  /** 入库模式下私钥内容的获取方式。 */
  const [keyContentMode, setKeyContentMode] = useState<"file" | "paste">("file");
  const [inlineKeyContent, setInlineKeyContent] = useState<string | null>(null);
  const [pastedKey, setPastedKey] = useState("");
  /** 私钥口令：留空 = 不改动已有口令（同一条凭据上的字段）。 */
  const [passphrase, setPassphrase] = useState("");
  /** 入库模式选中的 private_key 凭据。 */
  const [vaultCredId, setVaultCredId] = useState(boundIsVaultKey ? (initial?.credId ?? "") : "");

  // 引用型凭据的路径存在凭据里（不在 asset.key_path），查询回来后才补进输入框。
  // 只补一次：用户改过之后不再覆盖。
  useEffect(() => {
    if (!keyPath && boundIsRefKey && boundCred?.refPath) setKeyPath(boundCred.refPath);
    // 依赖只看这两个：keyPath 变化不该重跑（否则会把用户正在输入的内容覆盖回去）
  }, [boundIsRefKey, boundCred?.refPath]);

  const isDb = groupKind === "mysql" || groupKind === "redis";
  /** 私钥模式走自己的一组输入（文件/粘贴/凭据库 + 口令），不走凭据下拉。 */
  const keyAuth = authKind === "key";
  /** 密码类（密码认证 / 数据库）走凭据下拉；agent / 私钥 / 无认证不显示。 */
  const usesCred = isDb || authKind === "password";
  const credKind = "password";
  /** 编辑时已绑定的口令凭据：口令框占位提示「留空保持不变」。 */
  const hasBoundPassphrase = boundCred?.kind === "passphrase";

  /** 凭据名默认取资产名，跟着资产名输入走；一旦手改过就不再跟着变。 */
  const syncCredName = (assetName: string) => {
    setName(assetName);
    if (!credNameTouched.current) setCredName(assetName);
  };

  const save = async () => {
    try {
      if (kind === "group") {
        await assetApi.groupCreate(name);
        onSaved();
        return;
      }
      /*
       * 私钥：来源二选一（引用本地文件 / 存入凭据库），口令跟私钥同一条凭据。
       *
       * 「已绑定私钥凭据」时原位更新同一条（改一次、所有共用它的资产一起生效，
       * 与凭据共用的语义一致）；否则新建。
       */
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
            // 有口令（或原本就是引用型凭据）→ 路径与口令一起存成一条凭据：
            // 口令必须跟着私钥走，不能在库里单飞。
            const res = await vaultApi.setCredential(credLabel, "private_key", path, {
              id: reuseCredId,
              source: "file",
              // 留空 = 沿用原口令（后端会解出原载荷只换该字段）
              ...(passphrase ? { passphrase } : {}),
            });
            credId = res.id;
            finalKeyPath = null;
          } else {
            // 纯引用：资产直接记路径，凭据库里不留东西
            finalKeyPath = path || null;
            // 老数据可能把口令挂在一条独立凭据上，别因为"这次没填口令"就把它解绑
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
        // 密码类：新建凭据。填了值才创建；不填 = 不绑定
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
      // 本机资产的两个启动选项。空值**不写进** options —— 于是「清空输入框」
      // 就等于「回到系统默认（$SHELL / 家目录）」，而不是留一个空 shell 路径
      // 让内核去启一个不存在的程序。
      //
      // 非本机一律不传 options（undefined）：`AssetUpdateArgs.options` 是
      // `Option<Value>`，传了就是**整列替换** —— 顺手把 encoding / 初始命令
      // 这些别的字段抹掉，是很容易犯的错。
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
      pushToast("error", describeError(e));
    }
  };

  return (
    <div className="nx-overlay" onClick={onClose}>
      <div className="nx-modal max-w-[380px]" onClick={(e) => e.stopPropagation()}>
        <div className="nx-modal-header">
          <span className="text-[13px] font-semibold text-neutral-100">
            {kind === "group" ? "新建分组" : initial ? "编辑资产" : "新建资产"}
          </span>
        </div>
        <div className="nx-modal-body">
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
                      onClick={() => {
                        if (inlineKeyContent !== null) {
                          setKeyPath("");
                          setInlineKeyContent(null);
                        }
                        setKeyOrigin("ref");
                      }}
                    >
                      引用本地文件
                    </button>
                    <button
                      type="button"
                      className={`nx-segment-item ${keyOrigin === "vault" ? "is-active" : ""}`}
                      onClick={() => setKeyOrigin("vault")}
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
                                onChange={(e) => {
                                  setKeyPath(e.target.value);
                                  setInlineKeyContent(null);
                                }}
                                placeholder="选择私钥文件"
                              />
                              <button
                                type="button"
                                className="nx-btn nx-btn-outline shrink-0"
                                onClick={() =>
                                  void pickInlineKeyFile().then((selection) => {
                                    if (!selection) return;
                                    setKeyPath(selection.path);
                                    setInlineKeyContent(selection.content);
                                  })
                                }
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
                            <div className="nx-hint mt-1.5">↳ 共用这条凭据 · 改一处所有使用者生效</div>
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
                    <div className="nx-hint mt-1">↳ 共用凭据 · 修改一处全部生效</div>
                  ) : null}
                </div>
              )}
            </>
          )}
        </div>
        <div className="nx-modal-footer">
          <button className="nx-btn nx-btn-ghost" onClick={onClose}>
            取消
          </button>
          <button
            className="nx-btn nx-btn-primary"
            disabled={!name.trim()}
            onClick={() => void save()}
          >
            保存
          </button>
        </div>
      </div>
    </div>
  );
}
