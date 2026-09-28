// 资产树（M1-T1）：分组 + 资产 + 搜索 + 双击连接 + 右键菜单。
//
// UI 要点：类型图标来自统一映射表（不再用 emoji）；行内操作只在 hover 时出现；
// 分组用 chevron + 计数徽章；折叠状态由外壳的图标栏控制，这里不再自带展开按钮。
import { useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ask, pickKeyFile } from "../../ui/dialogs";
import { assetApi, vaultApi, type Asset, type AssetGroup } from "../../ipc/commands";
import { connectAsset, openCredentialsTab, useUi } from "../../app/store";
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

export function AssetTree() {
  const qc = useQueryClient();
  const { pushToast, leftOpen, leftWidth } = useUi();
  const [query, setQuery] = useState("");
  const [editing, setEditing] = useState<"none" | "asset" | "group">("none");
  const [editingAsset, setEditingAsset] = useState<Asset | null>(null);

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
          />
        ))}
        {filtered.length === 0 && (
          <div className="nx-hint px-2 py-8 text-center">
            {query ? "没有匹配的资产" : "还没有资产 — 点右上角的 + 新建一个"}
          </div>
        )}
      </div>

      {/* 凭据入口：常驻底部。凭据在语义上属于资产，入口放资产区 */}
      <button
        className="mx-1.5 mb-1.5 mt-auto flex h-[30px] shrink-0 items-center gap-2 border-t border-neutral-800/60 px-2.5 text-[12.5px] text-neutral-400 hover:bg-white/[.04] hover:text-neutral-200"
        title="凭据库"
        onClick={openCredentialsTab}
      >
        <IconKey size={14} className="shrink-0 text-neutral-500" />
        <span className="flex-1 text-left">凭据</span>
        <span className="nx-count">{credentials.data?.length ?? "…"}</span>
      </button>

      {editing !== "none" && (
        <AssetEditor
          kind={editing}
          onClose={() => setEditing("none")}
          onSaved={() => {
            setEditing("none");
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
          className="nx-icon-btn nx-icon-btn-sm"
          title="编辑"
          onClick={(e) => {
            e.stopPropagation();
            onEdit();
          }}
        >
          <IconEdit size={12} />
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
  onEdit,
}: {
  group: AssetGroup;
  assets: Asset[];
  onDelete: (a: Asset) => void;
  onEdit: (a: Asset) => void;
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
            <AssetRow key={a.id} asset={a} onDelete={() => onDelete(a)} onEdit={() => onEdit(a)} />
          ))}
          {assets.length === 0 && (
            <div className="px-2 py-1.5 text-[11px] text-neutral-600">（空分组）</div>
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
  onClose,
  onSaved,
}: {
  kind: "asset" | "group";
  /** 编辑已有资产（P1）。不传 = 新建。 */
  initial?: Asset;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [name, setName] = useState(initial?.name ?? "");
  const [groupKind, setGroupKind] = useState<"ssh" | "winrm" | "local" | "mysql" | "redis">(
    (initial?.kind as "ssh") ?? "ssh",
  );
  const [host, setHost] = useState(initial?.host ?? "");
  const [port, setPort] = useState(initial?.port ?? 22);
  const [username, setUsername] = useState(initial?.username ?? "root");
  const [authKind, setAuthKind] = useState(initial?.authKind ?? "password");
  const [keyPath, setKeyPath] = useState(initial?.keyPath ?? "");
  const [password, setPassword] = useState("");
  const pushToast = useUi((s) => s.pushToast);

  // 私钥独立交互（§私钥添加逻辑）：来源默认选文件，也可粘贴内容（保存时落成本地文件）
  const [keySource, setKeySource] = useState<"file" | "paste">("file");
  const [pastedKey, setPastedKey] = useState("");
  /** 私钥口令：与私钥同组输入，留空 = 保持已有绑定不变。 */
  const [passphrase, setPassphrase] = useState("");

  // 凭据绑定：默认跟随已有绑定；没有就「新建凭据」
  const [credChoice, setCredChoice] = useState<CredChoice>(initial?.credId ?? "new");
  const [credName, setCredName] = useState(initial?.name ?? "");
  const credNameTouched = useRef(false);
  const credentials = useQuery({
    queryKey: ["credentials"],
    queryFn: () => vaultApi.listCredentials(),
    refetchOnWindowFocus: false,
  });

  const isDb = groupKind === "mysql" || groupKind === "redis";
  /** 私钥模式走自己的一组输入（文件/粘贴 + 口令），不走凭据下拉。 */
  const keyAuth = authKind === "key";
  /** 密码类（密码认证 / 数据库）走凭据下拉；agent / 私钥 / 无认证不显示。 */
  const usesCred = isDb || authKind === "password";
  const credKind = "password";
  /** 编辑时已绑定的口令凭据：口令框占位提示「留空保持不变」。 */
  const boundCred = credentials.data?.find((c) => c.id === initial?.credId);
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
      /* 私钥模式：路径（粘贴的落成文件）+ 口令（留空保持已有绑定） */
      let finalKeyPath: string | null = null;
      let credId: string | null = null;
      if (keyAuth) {
        if (keySource === "paste") {
          if (!pastedKey.trim()) {
            pushToast("error", "请粘贴私钥内容");
            return;
          }
          const saved = await assetApi.saveKeyFile(pastedKey);
          finalKeyPath = saved.path;
        } else {
          finalKeyPath = keyPath.trim() || null;
        }
        credId = initial?.credId ?? null;
        if (passphrase) {
          // 有绑定就原位更新（同一条口令凭据改值），没有就新建
          const res = await vaultApi.setCredential(
            credName.trim() || name.trim(),
            "passphrase",
            passphrase,
            credId ?? undefined,
          );
          credId = res.id;
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
      const fields = {
        kind: groupKind,
        name,
        host: groupKind === "local" ? null : host,
        port: groupKind === "local" ? null : Number(port),
        username: groupKind === "local" ? null : username,
        authKind: groupKind === "local" ? "none" : authKind,
        keyPath: keyAuth ? finalKeyPath : null,
        credId,
      };
      if (initial) {
        await assetApi.update({ id: initial.id, ...fields });
      } else {
        await assetApi.create(fields);
      }
      onSaved();
    } catch (e) {
      pushToast("error", String((e as Error).message ?? e));
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
              onChange={(e) => (kind === "group" ? setName(e.target.value) : syncCredName(e.target.value))}
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
                  <label className="nx-label">私钥</label>
                  <div className="mb-1.5 flex gap-1.5">
                    <button
                      type="button"
                      className={`nx-btn nx-btn-sm ${keySource === "file" ? "nx-btn-primary" : "nx-btn-ghost"}`}
                      onClick={() => setKeySource("file")}
                    >
                      私钥文件
                    </button>
                    <button
                      type="button"
                      className={`nx-btn nx-btn-sm ${keySource === "paste" ? "nx-btn-primary" : "nx-btn-ghost"}`}
                      onClick={() => setKeySource("paste")}
                    >
                      粘贴内容
                    </button>
                  </div>
                  {keySource === "file" ? (
                    <div className="flex gap-1.5">
                      <input
                        className="nx-input"
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
                  ) : (
                    <textarea
                      className="nx-textarea font-mono text-[11.5px]"
                      rows={4}
                      value={pastedKey}
                      onChange={(e) => setPastedKey(e.target.value)}
                      placeholder={"-----BEGIN OPENSSH PRIVATE KEY-----\n…\n-----END OPENSSH PRIVATE KEY-----"}
                    />
                  )}
                  <input
                    type="password"
                    className="nx-input mt-1.5"
                    autoComplete="off"
                    value={passphrase}
                    onChange={(e) => setPassphrase(e.target.value)}
                    placeholder={
                      hasBoundPassphrase ? "已设置口令 · 留空保持不变" : "私钥口令（没有可留空）"
                    }
                  />
                  <div className="nx-hint mt-1">
                    ↳ 口令与私钥一同存入「凭据」库，可改名复用
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
