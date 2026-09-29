// 凭据面板（PLAN-credentials-rework §2.2）：左列表 + 右详情。
//
// 设计要点：
// · 列表每条带「被 N 个资产使用」——凭据不再是存进去就消失的黑洞；
// · 值默认打码，「显示」明文 15 秒后自动打回（复制不打码，复制的是真值）；
// · 锁定态：列表可见（名称/类型/引用数），值一律不可见，解锁在顶部；
// · 删除被引用的凭据：确认框里列出在用的资产，确认后引用清空（FK 已是 SET NULL）。
import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { assetApi, vaultApi, type Credential } from "../../ipc/commands";
import { ask, pickKeyFile, promptText } from "../../ui/dialogs";
import { connectAsset, useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import {
  assetIcon,
  IconCopy,
  IconEye,
  IconEyeOff,
  IconKey,
  IconLock,
  IconPlus,
  IconSearch,
  IconUnlock,
} from "../../ui/icons";

const KIND_LABEL: Record<string, string> = {
  password: "密码",
  passphrase: "私钥口令",
  private_key: "私钥",
  api_key: "API Key",
};

/** 明文回显的自动打回时间（§3 交互评审拍板：15 秒）。 */
const REVEAL_SECONDS = 15;

export function CredentialsPanel() {
  const qc = useQueryClient();
  const { pushToast } = useUi();
  const [search, setSearch] = useState("");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const status = useQuery({
    queryKey: ["vault-status"],
    queryFn: () => vaultApi.status(),
  });
  const credentials = useQuery({
    queryKey: ["credentials"],
    queryFn: () => vaultApi.listCredentials(),
  });

  const st = status.data;
  const locked = !!st?.initialized && !st.unlocked;
  const protectionOn = st?.mode === "master";

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["credentials"] });
    void qc.invalidateQueries({ queryKey: ["assets"] });
    void qc.invalidateQueries({ queryKey: ["vault-status"] });
  };

  /** 解锁（锁定横幅与连接前流程共用这一个入口）。 */
  const unlock = async () => {
    const pwd = await promptText("输入保护密码解锁凭据库：", "", { secret: true });
    if (pwd === null) return;
    try {
      await vaultApi.unlock(pwd);
      refresh();
      pushToast("success", "已解锁");
    } catch (e) {
      pushToast("error", describeError(e));
    }
  };

  const list = (credentials.data ?? []).filter((c) => {
    const q = search.trim().toLowerCase();
    if (!q) return true;
    return c.name.toLowerCase().includes(q) || (KIND_LABEL[c.kind] ?? c.kind).includes(q);
  });
  const selected = list.find((c) => c.id === selectedId) ?? null;

  return (
    <div className="flex h-full flex-col bg-neutral-900">
      {/* 工具条 */}
      <div className="flex h-[38px] shrink-0 items-center gap-2 border-b border-neutral-800/60 px-3">
        <span className="flex items-center gap-1.5 text-[13px] font-semibold text-neutral-100">
          <IconKey size={14} className="text-neutral-400" />
          凭据
        </span>
        {st &&
          (protectionOn ? (
            locked ? (
              <span className="nx-badge nx-badge-amber">
                <IconLock size={11} /> 已锁定
              </span>
            ) : (
              <span className="nx-badge nx-badge-green">
                <IconUnlock size={11} /> 已解锁
              </span>
            )
          ) : (
            <span className="nx-badge">未启用密码保护</span>
          ))}
        <div className="nx-spacer" />
        {protectionOn &&
          !locked && (
            <button
              className="nx-btn nx-btn-outline nx-btn-sm"
              title="锁定后需解锁才能使用凭据"
              onClick={() =>
                void vaultApi
                  .lock()
                  .then(refresh)
                  .catch((e) => pushToast("error", describeError(e)))
              }
            >
              <IconLock size={12} />
              立即锁定
            </button>
          )}
        <div className="nx-field w-[170px]">
          <span className="nx-field-icon">
            <IconSearch size={13} />
          </span>
          <input
            className="nx-input nx-input-sm"
            placeholder="搜索凭据"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
        <button className="nx-btn nx-btn-primary nx-btn-sm" onClick={() => setCreating(true)}>
          <IconPlus size={12} />
          新建凭据
        </button>
      </div>

      {/* 锁定横幅：列表照常可见，值一律打码 */}
      {locked && (
        <div className="flex shrink-0 items-center gap-2 border-b border-amber-500/20 bg-amber-500/10 px-3 py-1.5 text-[12px] text-amber-300/90">
          <IconLock size={13} />
          <span>凭据库已锁定 · 解锁后可查看与使用</span>
          <div className="nx-spacer" />
          <button className="nx-btn nx-btn-sm" onClick={() => void unlock()}>
            解锁
          </button>
        </div>
      )}

      <div className="flex min-h-0 flex-1">
        {/* 列表 */}
        <div className="w-[248px] shrink-0 overflow-y-auto border-r border-neutral-800/60 p-1.5">
          {list.map((c) => (
            <button
              key={c.id}
              className={`nx-row w-full text-left ${c.id === selectedId ? "bg-blue-500/12 ring-1 ring-inset ring-blue-500/40" : ""}`}
              onClick={() => setSelectedId(c.id)}
            >
              <IconKey size={13} className="shrink-0 text-neutral-500" />
              <span className="min-w-0 flex-1">
                <span className="block truncate text-[12.5px] text-neutral-200">{c.name}</span>
                <span className="block truncate text-[11px] text-neutral-500">
                  {KIND_LABEL[c.kind] ?? c.kind} ·{" "}
                  {c.usedBy.length > 0 ? (
                    <>被 {c.usedBy.length} 个资产使用</>
                  ) : (
                    <span className="text-amber-500/90">未使用</span>
                  )}
                </span>
              </span>
            </button>
          ))}
          {list.length === 0 && (
            <div className="nx-hint px-2 py-8 text-center">
              {search ? "没有匹配的凭据" : "暂无凭据 · 新建资产时自动录入"}
            </div>
          )}
        </div>

        {/* 详情 */}
        {selected ? (
          <CredentialDetail
            key={selected.id}
            cred={selected}
            locked={locked}
            onChanged={refresh}
            onDeleted={(id) => {
              setSelectedId((cur) => (cur === id ? null : cur));
              refresh();
            }}
          />
        ) : (
          <div className="flex flex-1 flex-col items-center justify-center gap-2 text-neutral-600">
            <IconKey size={22} />
            <span className="text-[12.5px]">选择凭据查看详情</span>
            <span className="text-[11.5px] text-neutral-700">
              改名 · 改值 · 引用关系 · 显示 / 复制
            </span>
          </div>
        )}
      </div>

      {creating && (
        <NewCredentialModal
          onClose={() => setCreating(false)}
          onSaved={(id) => {
            setCreating(false);
            setSelectedId(id);
            refresh();
          }}
        />
      )}
    </div>
  );
}

/* ── 详情 ─────────────────────────────────────────────────────────────── */

function CredentialDetail({
  cred,
  locked,
  onChanged,
  onDeleted,
}: {
  cred: Credential;
  locked: boolean;
  onChanged: () => void;
  onDeleted: (id: string) => void;
}) {
  const { pushToast } = useUi();
  const [name, setName] = useState(cred.name);
  const [newSecret, setNewSecret] = useState("");
  const [revealed, setRevealed] = useState<string | null>(null);
  const [left, setLeft] = useState(0);
  const timerRef = useRef<number | null>(null);

  useEffect(() => {
    return () => {
      if (timerRef.current) window.clearInterval(timerRef.current);
    };
  }, []);

  const maskNow = () => {
    if (timerRef.current) window.clearInterval(timerRef.current);
    timerRef.current = null;
    setRevealed(null);
    setLeft(0);
  };

  /** 显示明文：拉取真值，15 秒倒数后自动打回。 */
  const reveal = async () => {
    if (revealed !== null) {
      maskNow();
      return;
    }
    try {
      const secret = await vaultApi.revealCredential(cred.id);
      setRevealed(secret);
      setLeft(REVEAL_SECONDS);
      timerRef.current = window.setInterval(() => {
        setLeft((n) => {
          if (n <= 1) {
            maskNow();
            return 0;
          }
          return n - 1;
        });
      }, 1000);
    } catch (e) {
      pushToast("error", describeError(e));
    }
  };

  const copy = async () => {
    try {
      const secret = revealed ?? (await vaultApi.revealCredential(cred.id));
      await navigator.clipboard.writeText(secret);
      pushToast("success", "已复制");
    } catch (e) {
      pushToast("error", `复制失败：${describeError(e)}`);
    }
  };

  const dirty = name !== cred.name || newSecret.length > 0;

  const save = async () => {
    try {
      await vaultApi.updateCredential(cred.id, {
        name: name.trim() || cred.name,
        ...(newSecret ? { secret: newSecret } : {}),
      });
      if (newSecret && cred.usedBy.length > 0) {
        // §3-2：改值要告诉用户"下次连接即生效"，不用逐个资产去改
        pushToast(
          "success",
          `已保存 · 使用它的 ${cred.usedBy.length} 个资产（${cred.usedBy.map((u) => u.name).join("、")}）下次连接生效`,
        );
      } else {
        pushToast("success", "已保存");
      }
      setNewSecret("");
      onChanged();
    } catch (e) {
      pushToast("error", describeError(e));
    }
  };

  const del = async () => {
    const ok = await ask(
      cred.usedBy.length > 0
        ? `${cred.usedBy.length} 个资产正在使用：${cred.usedBy.map((u) => u.name).join("、")}\n删除后引用置空，资产保留。确认删除？`
        : `未被引用，删除无影响。确认删除「${cred.name}」？`,
    );
    if (!ok) return;
    try {
      await vaultApi.deleteCredential(cred.id);
      pushToast(
        "success",
        cred.usedBy.length > 0
          ? `已删除 · ${cred.usedBy.length} 个资产引用已置空`
          : "已删除",
      );
      onDeleted(cred.id);
    } catch (e) {
      pushToast("error", describeError(e));
    }
  };

  return (
    <div className="min-w-0 flex-1 overflow-y-auto p-5">
      <div className="grid max-w-[560px] grid-cols-[88px_1fr] items-center gap-x-4 gap-y-3.5">
        <label className="nx-label mb-0 self-center">名称</label>
        <input
          className="nx-input"
          value={locked ? cred.name : name}
          disabled={locked}
          onChange={(e) => setName(e.target.value)}
        />

        <label className="nx-label mb-0 self-center">类型</label>
        <span className="text-[12.5px] text-neutral-300">{KIND_LABEL[cred.kind] ?? cred.kind}</span>

        <label className="nx-label mb-0 self-center">使用它的资产</label>
        <div>
          {cred.usedBy.length > 0 ? (
            <div className="flex flex-wrap gap-1.5">
              {cred.usedBy.map((u) => {
                const Icon = assetIcon(u.kind);
                return (
                  <button
                    key={u.id}
                    className="nx-chip nx-chip-accent"
                    title="点击连接该资产"
                    onClick={() => void connectAsset({ id: u.id, name: u.name, kind: u.kind })}
                  >
                    <Icon size={12} />
                    {u.name}
                  </button>
                );
              })}
            </div>
          ) : (
            <span className="text-[12px] text-amber-500/90">未被引用 · 可安全删除</span>
          )}
        </div>

        <label className="nx-label mb-0 self-center">{KIND_LABEL[cred.kind] ?? "值"}</label>
        <div>
          {locked ? (
            <div className="flex items-center gap-2">
              <span className="rounded border border-dashed border-neutral-700 px-2.5 py-1 font-mono text-[13px] tracking-widest text-neutral-500">
                ••••••••••
              </span>
              <span className="nx-hint">解锁后可查看与修改</span>
            </div>
          ) : (
            <div className="flex items-center gap-2">
              <span className="min-w-0 flex-1 truncate rounded border border-neutral-800/70 bg-neutral-950/60 px-2.5 py-1 font-mono text-[12.5px] text-neutral-300">
                {revealed ?? "••••••••••"}
              </span>
              <button className="nx-btn nx-btn-sm" onClick={() => void reveal()}>
                {revealed !== null ? <IconEyeOff size={12} /> : <IconEye size={12} />}
                {revealed !== null ? "隐藏" : "显示"}
              </button>
              <button className="nx-btn nx-btn-sm" onClick={() => void copy()}>
                <IconCopy size={12} />
                复制
              </button>
              {revealed !== null && (
                <span className="shrink-0 rounded-full bg-neutral-800/80 px-2 py-0.5 text-[11px] text-neutral-400">
                  {left}s 后自动隐藏
                </span>
              )}
            </div>
          )}
          {!locked && (
            <input
              type="password"
              className="nx-input mt-2 font-mono"
              placeholder="新值（留空不修改）"
              value={newSecret}
              autoComplete="off"
              onChange={(e) => setNewSecret(e.target.value)}
            />
          )}
        </div>
      </div>

      {!locked && (
        <div className="mt-6 flex gap-2">
          <button className="nx-btn nx-btn-primary" disabled={!dirty} onClick={() => void save()}>
            保存
          </button>
          <button className="nx-btn nx-btn-ghost text-red-400" onClick={() => void del()}>
            删除
          </button>
        </div>
      )}
    </div>
  );
}

/* ── 新建凭据 ─────────────────────────────────────────────────────────── */

function NewCredentialModal({
  onClose,
  onSaved,
}: {
  onClose: () => void;
  onSaved: (id: string) => void;
}) {
  const { pushToast } = useUi();
  const [name, setName] = useState("");
  const [kind, setKind] = useState("password");
  const [secret, setSecret] = useState("");
  // 私钥类型走与资产表单同一套特殊交互：文件（默认）/ 粘贴
  const [keySource, setKeySource] = useState<"file" | "paste">("file");
  const [keyFilePath, setKeyFilePath] = useState("");
  const [pastedKey, setPastedKey] = useState("");

  const isKey = kind === "private_key";
  const keyReady = keySource === "file" ? keyFilePath.trim().length > 0 : pastedKey.trim().length > 0;
  const ready = name.trim().length > 0 && (isKey ? keyReady : secret.length > 0);

  const save = async () => {
    try {
      // 文件模式在保存时才读内容：选完到保存之间文件被改动的窗口最小
      const value = isKey
        ? keySource === "paste"
          ? pastedKey.trim()
          : await assetApi.readKeyFile(keyFilePath.trim())
        : secret;
      const { id } = await vaultApi.setCredential(name.trim(), kind, value);
      pushToast("success", `已创建凭据「${name.trim()}」`);
      onSaved(id);
    } catch (e) {
      pushToast("error", describeError(e));
    }
  };

  return (
    <div className="nx-overlay" onClick={onClose}>
      <div className="nx-modal max-w-[400px]" onClick={(e) => e.stopPropagation()}>
        <div className="nx-modal-header">
          <span className="text-[13px] font-semibold text-neutral-100">新建凭据</span>
        </div>
        <div className="nx-modal-body">
          <div className="nx-form-row">
            <label className="nx-label">名称</label>
            <input
              className="nx-input"
              value={name}
              placeholder="例如：db-prod"
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div className="nx-form-row">
            <label className="nx-label">类型</label>
            <select className="nx-select" value={kind} onChange={(e) => setKind(e.target.value)}>
              {Object.entries(KIND_LABEL).map(([k, label]) => (
                <option key={k} value={k}>
                  {label}
                </option>
              ))}
            </select>
          </div>
          {isKey ? (
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
                    value={keyFilePath}
                    onChange={(e) => setKeyFilePath(e.target.value)}
                    placeholder="选择私钥文件"
                  />
                  <button
                    type="button"
                    className="nx-btn nx-btn-outline shrink-0"
                    onClick={() =>
                      void pickKeyFile().then((p) => {
                        if (p) setKeyFilePath(p);
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
            </div>
          ) : (
            <div className="nx-form-row">
              <label className="nx-label">值</label>
              <input
                type="password"
                className="nx-input"
                value={secret}
                autoComplete="off"
                onChange={(e) => setSecret(e.target.value)}
              />
            </div>
          )}
        </div>
        <div className="nx-modal-footer">
          <button className="nx-btn nx-btn-ghost" onClick={onClose}>
            取消
          </button>
          <button className="nx-btn nx-btn-primary" disabled={!ready} onClick={() => void save()}>
            保存
          </button>
        </div>
      </div>
    </div>
  );
}
