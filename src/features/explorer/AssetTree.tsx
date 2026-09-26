// 资产树（M1-T1）：分组 + 资产 + 搜索 + 双击连接 + 右键菜单。
//
// UI 要点：类型图标来自统一映射表（不再用 emoji）；行内操作只在 hover 时出现；
// 分组用 chevron + 计数徽章；折叠状态由外壳的图标栏控制，这里不再自带展开按钮。
import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ask } from "../../ui/dialogs";
import { assetApi, vaultApi, type Asset, type AssetGroup } from "../../ipc/commands";
import { connectAsset, useUi } from "../../app/store";
import {
  assetIcon,
  IconChevronDown,
  IconChevronRight,
  IconClose,
  IconFolder,
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

export function AssetTree() {
  const qc = useQueryClient();
  const { pushToast, leftOpen } = useUi();
  const [query, setQuery] = useState("");
  const [editing, setEditing] = useState<"none" | "asset" | "group">("none");

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
    if (!(await ask(`删除资产「${a.name}」？（软删除，可恢复）`))) return;
    await assetApi.delete(a.id);
    void qc.invalidateQueries({ queryKey: ["assets"] });
    pushToast("info", "已删除");
  };

  if (!leftOpen) return null;

  return (
    <div className="flex h-full w-[236px] shrink-0 flex-col border-r border-neutral-800/60 bg-neutral-950">
      <div className="flex h-[34px] shrink-0 items-center gap-1 px-2.5">
        <span className="text-xs font-semibold tracking-wide text-neutral-200">资产</span>
        <div className="nx-spacer" />
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="新建资产"
          onClick={() => setEditing("asset")}
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

      <div className="min-h-0 flex-1 overflow-y-auto px-1.5 pb-2">
        {(byGroup.get(null) ?? []).map((a) => (
          <AssetRow key={a.id} asset={a} onDelete={() => void onDelete(a)} />
        ))}
        {(groups.data ?? []).map((g: AssetGroup) => (
          <GroupNode
            key={g.id}
            group={g}
            assets={byGroup.get(g.id) ?? []}
            onDelete={(a) => void onDelete(a)}
          />
        ))}
        {filtered.length === 0 && (
          <div className="nx-hint px-2 py-8 text-center">
            {query ? "没有匹配的资产" : "还没有资产 — 点右上角的 + 新建一个"}
          </div>
        )}
      </div>

      {editing !== "none" && (
        <AssetEditor
          kind={editing}
          onClose={() => setEditing("none")}
          onSaved={() => {
            setEditing("none");
            void qc.invalidateQueries({ queryKey: ["assets"] });
            void qc.invalidateQueries({ queryKey: ["groups"] });
          }}
        />
      )}
    </div>
  );
}

function AssetRow({ asset, onDelete }: { asset: Asset; onDelete: () => void }) {
  const Icon = assetIcon(asset.kind);
  const label = asset.name;
  const target = asset.host ? `${asset.host}${asset.port ? `:${asset.port}` : ""}` : "";
  return (
    <div
      className="nx-row group"
      onDoubleClick={() => void connectAsset(asset)}
      title={target ? `${asset.name} · ${target} · 双击连接` : `${asset.name} · 双击连接`}
    >
      <Icon size={14} className="shrink-0 text-neutral-500" />
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {target && <span className="shrink-0 text-[11px] text-neutral-500">{target}</span>}
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
          className="nx-icon-btn nx-icon-btn-sm is-danger"
          title="删除"
          onClick={(e) => {
            e.stopPropagation();
            onDelete();
          }}
        >
          <IconClose size={12} />
        </button>
      </span>
    </div>
  );
}

function GroupNode({
  group,
  assets,
  onDelete,
}: {
  group: AssetGroup;
  assets: Asset[];
  onDelete: (a: Asset) => void;
}) {
  const [open, setOpen] = useState(true);
  return (
    <div className="mb-0.5">
      <button
        className="nx-row w-full text-neutral-400"
        onClick={() => setOpen((v) => !v)}
      >
        {open ? (
          <IconChevronDown size={12} className="shrink-0" />
        ) : (
          <IconChevronRight size={12} className="shrink-0" />
        )}
        <IconFolder size={13} className="shrink-0 text-neutral-500" />
        <span className="min-w-0 flex-1 truncate text-left text-xs font-medium">{group.name}</span>
        <span className="nx-count">{assets.length}</span>
      </button>
      {open && (
        <div className="ml-3.5">
          {assets.map((a) => (
            <AssetRow key={a.id} asset={a} onDelete={() => onDelete(a)} />
          ))}
          {assets.length === 0 && (
            <div className="px-2 py-1.5 text-[11px] text-neutral-600">（空分组）</div>
          )}
        </div>
      )}
    </div>
  );
}

function AssetEditor({
  kind,
  onClose,
  onSaved,
}: {
  kind: "asset" | "group";
  onClose: () => void;
  onSaved: () => void;
}) {
  const [name, setName] = useState("");
  const [groupKind, setGroupKind] = useState<"ssh" | "winrm" | "local" | "mysql" | "redis">("ssh");
  const [host, setHost] = useState("");
  const [port, setPort] = useState(22);
  const [username, setUsername] = useState("root");
  const [authKind, setAuthKind] = useState("password");
  const [password, setPassword] = useState("");
  const [keyPath, setKeyPath] = useState("");
  const pushToast = useUi((s) => s.pushToast);

  const save = async () => {
    try {
      if (kind === "group") {
        await assetApi.groupCreate(name);
        onSaved();
        return;
      }
      let credId: string | null = null;
      if ((authKind === "password" || authKind === "key") && password) {
        const res = await vaultApi.credentialSave(
          `${name}-${authKind}`,
          authKind === "password" ? "password" : "passphrase",
          password,
        );
        credId = res.id;
      }
      await assetApi.create({
        kind: groupKind,
        name,
        host: groupKind === "local" ? null : host,
        port: groupKind === "local" ? null : Number(port),
        username: groupKind === "local" ? null : username,
        authKind: groupKind === "local" ? "none" : authKind,
        keyPath: authKind === "key" ? keyPath : null,
        credId,
      });
      onSaved();
    } catch (e) {
      pushToast("error", String((e as Error).message ?? e));
    }
  };

  const isDb = groupKind === "mysql" || groupKind === "redis";

  return (
    <div className="nx-overlay" onClick={onClose}>
      <div className="nx-modal max-w-[380px]" onClick={(e) => e.stopPropagation()}>
        <div className="nx-modal-header">
          <span className="text-[13px] font-semibold text-neutral-100">
            {kind === "group" ? "新建分组" : "新建资产"}
          </span>
        </div>
        <div className="nx-modal-body">
          {kind === "asset" && (
            <div className="nx-form-row">
              <label className="nx-label">类型</label>
              <select
                className="nx-select"
                value={groupKind}
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
            </div>
          )}

          <div className="nx-form-row">
            <label className="nx-label">名称</label>
            <input
              className="nx-input"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="web-01"
            />
          </div>

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

              {!isDb && (
                <>
                  <div className="nx-form-row">
                    <label className="nx-label">用户名</label>
                    <input
                      className="nx-input"
                      value={username}
                      onChange={(e) => setUsername(e.target.value)}
                    />
                  </div>
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
                </>
              )}

              {isDb && (
                <div className="nx-form-row">
                  <label className="nx-label">用户名</label>
                  <input
                    className="nx-input"
                    value={username}
                    onChange={(e) => setUsername(e.target.value)}
                  />
                </div>
              )}

              {!isDb &&
                (authKind === "key" ? (
                  <>
                    <div className="nx-form-row">
                      <label className="nx-label">私钥路径</label>
                      <input
                        className="nx-input"
                        value={keyPath}
                        onChange={(e) => setKeyPath(e.target.value)}
                        placeholder="C:\Users\you\.ssh\id_ed25519"
                      />
                    </div>
                    <div className="nx-form-row">
                      <label className="nx-label">私钥口令（可选，加密保存）</label>
                      <input
                        type="password"
                        className="nx-input"
                        value={password}
                        onChange={(e) => setPassword(e.target.value)}
                      />
                    </div>
                  </>
                ) : (
                  <div className="nx-form-row">
                    <label className="nx-label">密码（加密保存到本机凭据库）</label>
                    <input
                      type="password"
                      className="nx-input"
                      value={password}
                      onChange={(e) => setPassword(e.target.value)}
                    />
                  </div>
                ))}

              {isDb && (
                <div className="nx-form-row">
                  <label className="nx-label">密码（加密保存到本机凭据库）</label>
                  <input
                    type="password"
                    className="nx-input"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                  />
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
