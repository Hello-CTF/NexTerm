// 资产树（M1-T1）：分组 + 资产 + 搜索 + 双击连接 + 右键菜单。
import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ask } from "../../ui/dialogs";
import { assetApi, vaultApi, type Asset, type AssetGroup } from "../../ipc/commands";
import { connectAsset, useUi } from "../../app/store";

const KIND_ICON: Record<string, string> = {
  ssh: "🖥",
  winrm: "🪟",
  local: "💻",
  docker: "🐳",
  mysql: "🐬",
  redis: "🟥",
};

export function AssetTree() {
  const qc = useQueryClient();
  const { pushToast, leftOpen, setLeftOpen } = useUi();
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

  if (!leftOpen) {
    return (
      <button
        className="flex h-full w-8 items-center justify-center rounded text-neutral-400 hover:bg-neutral-800"
        onClick={() => setLeftOpen(true)}
        title="展开资产树"
      >
        ▸
      </button>
    );
  }

  return (
    <div className="relative flex h-full w-56 flex-col border-r border-neutral-800 bg-neutral-950/60">
      <div className="flex items-center gap-1 p-2">
        <input
          className="min-w-0 flex-1 rounded bg-neutral-800 px-2 py-1 text-xs text-neutral-200 outline-none focus:ring-1 focus:ring-blue-500"
          placeholder="搜索资产 (Ctrl+K)"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <button
          className="rounded bg-neutral-800 px-1.5 py-1 text-xs hover:bg-neutral-700"
          title="新建资产"
          onClick={() => setEditing("asset")}
        >
          +
        </button>
        <button
          className="rounded bg-neutral-800 px-1.5 py-1 text-xs hover:bg-neutral-700"
          title="新建分组"
          onClick={() => setEditing("group")}
        >
          📁+
        </button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-1 pb-2 text-sm">
        {byGroup.get(null)?.map((a) => (
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
          <div className="px-2 py-6 text-center text-xs text-neutral-500">
            {query ? "无匹配资产" : "还没有资产，点 + 新建"}
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
  const label = `${asset.name}${asset.host ? ` (${asset.host}${asset.port ? `:${asset.port}` : ""})` : ""}`;
  return (
    <div
      className="group flex cursor-pointer items-center gap-1.5 rounded px-2 py-1 hover:bg-neutral-800"
      onDoubleClick={() => void connectAsset(asset)}
      title="双击连接"
    >
      <span>{KIND_ICON[asset.kind] ?? "•"}</span>
      <span className="flex-1 truncate text-neutral-300">{label}</span>
      <button
        className="hidden rounded px-1 text-[10px] text-green-400 hover:bg-green-500/10 group-hover:block"
        onClick={(e) => {
          e.stopPropagation();
          void connectAsset(asset);
        }}
        title="连接"
      >
        连接
      </button>
      <button
        className="hidden rounded px-1 text-xs text-neutral-500 hover:text-red-400 group-hover:block"
        onClick={(e) => {
          e.stopPropagation();
          onDelete();
        }}
        title="删除"
      >
        ✕
      </button>
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
    <div className="mb-1">
      <button
        className="flex w-full items-center gap-1 rounded px-2 py-1 text-left text-xs font-medium text-neutral-400 hover:bg-neutral-800"
        onClick={() => setOpen((v) => !v)}
      >
        <span>{open ? "▾" : "▸"}</span>
        <span>📁 {group.name}</span>
        <span className="ml-auto text-[10px] text-neutral-600">{assets.length}</span>
      </button>
      {open && (
        <div className="ml-3">
          {assets.map((a) => (
            <AssetRow key={a.id} asset={a} onDelete={() => onDelete(a)} />
          ))}
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

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50" onClick={onClose}>
      <div
        className="w-80 rounded-lg border border-neutral-700 bg-neutral-900 p-4 text-sm shadow-xl"
        onClick={(e) => e.stopPropagation()}
      >
        <h3 className="mb-3 font-medium text-neutral-200">
          {kind === "group" ? "新建分组" : "新建资产"}
        </h3>
        {kind === "asset" && (
          <>
            <label className="mb-1 block text-xs text-neutral-400">类型</label>
            <select
              className="mb-2 w-full rounded bg-neutral-800 px-2 py-1 outline-none"
              value={groupKind}
              onChange={(e) => {
                setGroupKind(e.target.value as "ssh" | "winrm" | "local" | "mysql" | "redis");
                if (e.target.value === "winrm") setPort(5985);
                else if (e.target.value === "ssh") setPort(22);
              }}
            >
              <option value="ssh">SSH (Linux)</option>
              <option value="winrm">WinRM (Windows)</option>
              <option value="local">本地终端</option>
              <option value="mysql">MySQL</option>
              <option value="redis">Redis</option>
            </select>
          </>
        )}
        <label className="mb-1 block text-xs text-neutral-400">名称</label>
        <input
          className="mb-2 w-full rounded bg-neutral-800 px-2 py-1 outline-none"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="web-01"
        />
        {kind === "asset" && groupKind !== "local" && (
          <>
            <div className="mb-2 flex gap-2">
              <div className="flex-1">
                <label className="mb-1 block text-xs text-neutral-400">主机</label>
                <input
                  className="w-full rounded bg-neutral-800 px-2 py-1 outline-none"
                  value={host}
                  onChange={(e) => setHost(e.target.value)}
                  placeholder="1.2.3.4"
                />
              </div>
              <div className="w-20">
                <label className="mb-1 block text-xs text-neutral-400">端口</label>
                <input
                  type="number"
                  className="w-full rounded bg-neutral-800 px-2 py-1 outline-none"
                  value={port}
                  onChange={(e) => setPort(Number(e.target.value))}
                />
              </div>
            </div>
            <label className="mb-1 block text-xs text-neutral-400">用户名</label>
            <input
              className="mb-2 w-full rounded bg-neutral-800 px-2 py-1 outline-none"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
            {groupKind !== "mysql" && groupKind !== "redis" && (
              <>
                <label className="mb-1 block text-xs text-neutral-400">认证方式</label>
                <select
                  className="mb-2 w-full rounded bg-neutral-800 px-2 py-1 outline-none"
                  value={authKind}
                  onChange={(e) => setAuthKind(e.target.value)}
                >
                  <option value="password">密码</option>
                  <option value="key">私钥文件</option>
                  <option value="agent">SSH Agent</option>
                </select>
                {authKind === "key" ? (
                  <>
                    <label className="mb-1 block text-xs text-neutral-400">私钥路径</label>
                    <input
                      className="mb-2 w-full rounded bg-neutral-800 px-2 py-1 outline-none"
                      value={keyPath}
                      onChange={(e) => setKeyPath(e.target.value)}
                      placeholder="C:\Users\you\.ssh\id_ed25519"
                    />
                    <label className="mb-1 block text-xs text-neutral-400">私钥口令（可选，加密保存）</label>
                    <input
                      type="password"
                      className="mb-2 w-full rounded bg-neutral-800 px-2 py-1 outline-none"
                      value={password}
                      onChange={(e) => setPassword(e.target.value)}
                    />
                  </>
                ) : (
                  <>
                    <label className="mb-1 block text-xs text-neutral-400">密码（加密保存）</label>
                    <input
                      type="password"
                      className="mb-2 w-full rounded bg-neutral-800 px-2 py-1 outline-none"
                      value={password}
                      onChange={(e) => setPassword(e.target.value)}
                    />
                  </>
                )}
              </>
            )}
          </>
        )}
        <div className="mt-3 flex justify-end gap-2">
          <button className="rounded px-3 py-1 text-neutral-400 hover:bg-neutral-800" onClick={onClose}>
            取消
          </button>
          <button
            className="rounded bg-blue-600 px-3 py-1 text-white hover:bg-blue-500 disabled:opacity-50"
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
