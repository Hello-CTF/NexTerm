import { useEffect, useRef, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { vaultApi, type Credential, type CredentialSource, type RevealedCredential } from "../../ipc/commands";
import { ask } from "../../ui/dialogs";
import { connectAsset, openCredentialsSidebar, useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import { NewCredentialModal } from "./NewCredentialModal";
import { GenerateKeyModal } from "./GenerateKeyModal";
import { kindMeta, formatTime } from "./meta";
import { resolveCredentialCopy, type CredentialCopyField } from "./credentialCopy";
import { useRefreshCredentials, useVaultUnlock } from "./useVaultUnlock";
import { useVaultInitGate } from "./useVaultInitGate";
import {
  assetIcon,
  IconCopy,
  IconEye,
  IconEyeOff,
  IconFile,
  IconKey,
  IconLock,
  IconPlus,
  IconRefresh,
  IconXCircle,
} from "../../ui/icons";

const REVEAL_SECONDS = 15;

export function CredentialsPanel({ credId }: { credId?: string }) {
  const refresh = useRefreshCredentials();
  const unlock = useVaultUnlock();
  const { ensureVaultInit, vaultInitGate } = useVaultInitGate();
  const [creating, setCreating] = useState(false);
  const [generating, setGenerating] = useState(false);

  const startCreate = () =>
    void ensureVaultInit().then((ok) => {
      if (ok) setCreating(true);
    });

  const status = useQuery({ queryKey: ["vault-status"], queryFn: () => vaultApi.status() });
  const credentials = useQuery({ queryKey: ["credentials"], queryFn: () => vaultApi.listCredentials() });

  const st = status.data;
  const locked = !!st?.initialized && !st.unlocked;
  const protectionOn = st?.mode === "master" && !st.passwordless;
  const list = credentials.data ?? [];
  const selected = list.find((c) => c.id === credId) ?? null;

  return (
    <div className="flex h-full flex-col bg-neutral-900">
      <div className="flex min-h-[38px] shrink-0 flex-wrap items-center gap-x-2 gap-y-1 border-b border-neutral-800/60 px-3 py-1">
        <IconKey size={14} className="shrink-0 text-neutral-400" />
        <span className="shrink-0 whitespace-nowrap text-[13px] font-semibold text-neutral-100">凭据</span>
        {st &&
          (protectionOn ? (
            locked ? (
              <span className="nx-badge nx-badge-amber hidden sm:inline-flex">
                <IconLock size={11} /> 已锁定
              </span>
            ) : (
              <span className="nx-badge nx-badge-green hidden sm:inline-flex">已解锁</span>
            )
          ) : (
            <span className="nx-badge hidden sm:inline-flex">未启用密码保护</span>
          ))}
        <div className="nx-spacer" />
        <button
          className="nx-btn nx-btn-outline nx-btn-sm shrink-0"
          onClick={() =>
            void ensureVaultInit().then((ok) => {
              if (ok) setGenerating(true);
            })
          }
        >
          <IconKey size={12} />
          生成密钥
        </button>
        <button className="nx-btn nx-btn-primary nx-btn-sm shrink-0" onClick={startCreate}>
          <IconPlus size={12} />
          新建凭据
        </button>
      </div>

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

      {credentials.isLoading ? (
        <div className="nx-empty" role="status">
          凭据加载中…
        </div>
      ) : credentials.isError && !credentials.data ? (
        <div className="nx-empty">
          <div className="nx-empty-icon">
            <IconXCircle size={18} />
          </div>
          <div className="text-[12.5px] text-red-300">
            凭据列表加载失败 · {describeError(credentials.error)}
          </div>
          <button
            className="nx-btn nx-btn-ghost nx-btn-sm"
            onClick={() => void credentials.refetch()}
          >
            <IconRefresh size={12} />
            重试
          </button>
        </div>
      ) : selected ? (
        <CredentialDetail
          key={selected.id}
          cred={selected}
          locked={locked}
          onChanged={refresh}
          onDeleted={refresh}
        />
      ) : (
        <EmptyDetail hasAny={list.length > 0} onNew={startCreate} />
      )}

      {creating && (
        <NewCredentialModal
          onClose={() => setCreating(false)}
          onSaved={() => {
            setCreating(false);
            refresh();
          }}
        />
      )}
      {generating && (
        <GenerateKeyModal
          onClose={() => setGenerating(false)}
          onSaved={() => refresh()}
        />
      )}
      {vaultInitGate}
    </div>
  );
}

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
  const meta = kindMeta(cred.kind);
  const isKey = cred.kind === "private_key";
  const isRef = isKey && cred.source === "file";

  const [name, setName] = useState(cred.name);
  const [newSecret, setNewSecret] = useState("");
  const [newPassphrase, setNewPassphrase] = useState("");
  const [revealed, setRevealed] = useState<RevealedCredential | null>(null);
  const [left, setLeft] = useState(0);
  const [saving, setSaving] = useState(false);
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

  const reveal = async () => {
    if (revealed !== null) {
      maskNow();
      return;
    }
    try {
      const res = await vaultApi.revealCredential(cred.id);
      setRevealed(res);
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

  const copy = async (field: CredentialCopyField, label: string) => {
    try {
      const value = await resolveCredentialCopy(cred, field, revealed, () =>
        vaultApi.revealCredential(cred.id),
      );
      await navigator.clipboard.writeText(value);
      pushToast("success", label);
    } catch (e) {
      pushToast("error", `复制失败：${describeError(e)}`);
    }
  };

  const dirty = name.trim() !== cred.name || newSecret.length > 0 || newPassphrase.length > 0;

  const save = async () => {
    setSaving(true);
    try {
      const patch: {
        name?: string;
        secret?: string;
        source?: CredentialSource;
        passphrase?: string;
      } = {};
      if (name.trim() && name.trim() !== cred.name) patch.name = name.trim();
      if (newSecret) {
        patch.secret = newSecret;
        if (isKey) patch.source = isRef ? "file" : "inline";
      }
      if (newPassphrase) patch.passphrase = newPassphrase;
      if (Object.keys(patch).length === 0) return;

      await vaultApi.updateCredential(cred.id, patch);
      pushToast(
        "success",
        patch.secret && cred.usedBy.length > 0
          ? `已保存 · 使用它的 ${cred.usedBy.length} 个资产（${cred.usedBy
              .map((u) => u.name)
              .join("、")}）下次连接生效`
          : "已保存",
      );
      setNewSecret("");
      setNewPassphrase("");
      maskNow();
      onChanged();
    } catch (e) {
      pushToast("error", describeError(e));
    } finally {
      setSaving(false);
    }
  };

  const clearPassphrase = async () => {
    const ok = await ask("清除这条私钥的口令？\n清除后，需要口令才能解开的私钥将无法连接。", {
      kind: "warning",
    });
    if (!ok) return;
    try {
      await vaultApi.updateCredential(cred.id, { passphrase: "" });
      pushToast("success", "已清除口令");
      maskNow();
      onChanged();
    } catch (e) {
      pushToast("error", describeError(e));
    }
  };

  const del = async () => {
    const ok = await ask(
      cred.usedBy.length > 0
        ? `${cred.usedBy.length} 个资产正在使用：${cred.usedBy
            .map((u) => u.name)
            .join("、")}\n删除后引用置空，资产保留。确认删除？`
        : `未被引用，删除无影响。确认删除「${cred.name}」？`,
      { kind: "warning" },
    );
    if (!ok) return;
    try {
      await vaultApi.deleteCredential(cred.id);
      pushToast(
        "success",
        cred.usedBy.length > 0 ? `已删除 · ${cred.usedBy.length} 个资产引用已置空` : "已删除",
      );
      onDeleted(cred.id);
    } catch (e) {
      pushToast("error", describeError(e));
    }
  };

  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto max-w-[640px] px-6 py-6">
        <div className="mb-6 flex items-start gap-3.5">
          <span
            className={`flex h-11 w-11 shrink-0 items-center justify-center rounded-xl ${meta.bg} ${meta.tone}`}
          >
            <meta.Icon size={20} />
          </span>
          <div className="min-w-0 flex-1">
            <input
              className="w-full border-b border-transparent bg-transparent pb-0.5 text-[15px] font-semibold tracking-tight text-neutral-100 outline-none hover:border-neutral-700 focus:border-blue-500/60 disabled:cursor-not-allowed"
              value={locked ? cred.name : name}
              disabled={locked}
              spellCheck={false}
              aria-label="凭据名称"
              onChange={(e) => setName(e.target.value)}
              title={locked ? "解锁后可改名" : "点击可改名"}
            />
            <div className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11.5px] text-neutral-500">
              <span className="nx-badge">{meta.label}</span>
              {isKey && !locked && cred.source && (
                <span className="nx-badge">
                  {cred.source === "file" ? "引用本地文件" : "已存入凭据库"}
                </span>
              )}
              {isKey && !locked && cred.hasPassphrase && <span className="nx-badge">有口令</span>}
              {cred.usedBy.length > 0 ? (
                <span>
                  被 <span className="text-neutral-300">{cred.usedBy.length}</span> 个资产使用
                </span>
              ) : (
                <span className="text-[var(--nx-fg-warning)]">未被引用 · 可安全删除</span>
              )}
            </div>
          </div>
        </div>

        <Section title={isKey ? (isRef ? "引用的私钥文件" : "私钥内容") : meta.label}>
          {locked ? (
            <div className="flex items-center gap-2.5">
              <code className="font-mono text-[13px] tracking-[0.3em] text-neutral-600">
                ••••••••••
              </code>
              <span className="nx-hint">解锁后可查看与修改</span>
            </div>
          ) : (
            <>
              <div className="flex items-start gap-2">
                {isRef ? (
                  <code className="min-w-0 flex-1 break-all rounded-md border border-neutral-800/70 bg-neutral-900/60 px-2.5 py-1.5 font-mono text-[12px] text-neutral-300">
                    {cred.refPath || "（路径为空）"}
                  </code>
                ) : revealed === null ? (
                  <code className="min-w-0 flex-1 truncate rounded-md border border-neutral-800/70 bg-neutral-900/60 px-2.5 py-1.5 font-mono text-[12.5px] tracking-[0.2em] text-neutral-500">
                    ••••••••••
                  </code>
                ) : (
                  <pre className="nx-mono max-h-[180px] min-w-0 flex-1 overflow-auto whitespace-pre-wrap break-all rounded-md border border-purple-500/25 bg-neutral-900/60 px-2.5 py-1.5 text-[11.5px] leading-[1.5] text-neutral-300">
                    {revealed.value}
                  </pre>
                )}
                {!isRef && (
                  <button className="nx-btn nx-btn-sm shrink-0" onClick={() => void reveal()}>
                    {revealed !== null ? <IconEyeOff size={12} /> : <IconEye size={12} />}
                    {revealed !== null ? "隐藏" : "显示"}
                  </button>
                )}
                <button
                  className="nx-btn nx-btn-sm shrink-0"
                  onClick={() => void copy("value", "已复制")}
                >
                  <IconCopy size={12} />
                  复制
                </button>
              </div>
              {revealed !== null && !isRef && (
                <div className="mt-2 flex items-center gap-1.5 text-[11px] text-[var(--nx-fg-warning)]">
                  <IconEye size={11} />
                  {left} 秒后自动隐藏
                </div>
              )}

              <div className="mt-3 border-t border-neutral-800/60 pt-3">
                {isRef ? (
                  <>
                    <input
                      className="nx-input font-mono text-[12px]"
                      placeholder="新的私钥文件路径（留空不修改）"
                      value={newSecret}
                      spellCheck={false}
                      onChange={(e) => setNewSecret(e.target.value)}
                    />
                    <div className="nx-hint mt-1.5">
                      ↳ 只记路径，库里不保存私钥正文。文件被挪走就失效，来这里改路径。
                    </div>
                  </>
                ) : (
                  <input
                    type="password"
                    className="nx-input font-mono"
                    placeholder={isKey ? "新私钥内容（留空不修改）" : "新值（留空不修改）"}
                    value={newSecret}
                    autoComplete="off"
                    onChange={(e) => setNewSecret(e.target.value)}
                  />
                )}
                {newSecret.length > 0 && cred.usedBy.length > 0 && (
                  <div className="nx-hint mt-1.5">
                    保存后，使用它的 {cred.usedBy.length} 个资产下次连接时生效，不用逐个去改。
                  </div>
                )}
              </div>
            </>
          )}
        </Section>

        {isKey && (
          <Section title="私钥口令">
            {locked ? (
              <span className="nx-hint">解锁后可查看与修改</span>
            ) : (
              <>
                <div className="flex items-center gap-2">
                  <span className="min-w-0 flex-1 truncate rounded-md border border-neutral-800/70 bg-neutral-900/60 px-2.5 py-1.5 font-mono text-[12.5px] text-neutral-300">
                    {!cred.hasPassphrase
                      ? "未设置"
                      : revealed
                        ? revealed.passphrase || "（空）"
                        : "••••••••"}
                  </span>
                  {cred.hasPassphrase && (
                    <>
                      <button className="nx-btn nx-btn-sm shrink-0" onClick={() => void reveal()}>
                        {revealed !== null ? <IconEyeOff size={12} /> : <IconEye size={12} />}
                        {revealed !== null ? "隐藏" : "显示"}
                      </button>
                      <button
                        className="nx-btn nx-btn-sm shrink-0"
                        onClick={() => void copy("passphrase", "已复制口令")}
                      >
                        <IconCopy size={12} />
                        复制
                      </button>
                    </>
                  )}
                </div>
                <input
                  type="password"
                  className="nx-input mt-3 font-mono"
                  placeholder="新口令（留空不修改）"
                  value={newPassphrase}
                  autoComplete="off"
                  onChange={(e) => setNewPassphrase(e.target.value)}
                />
                <div className="mt-1.5 flex items-center gap-2">
                  <span className="nx-hint flex-1">
                    选填。私钥本身带口令才需要填；改口令不用重填私钥。
                  </span>
                  {cred.hasPassphrase && newPassphrase.length === 0 && (
                    <button
                      className="nx-btn nx-btn-ghost nx-btn-sm text-[var(--nx-fg-warning)]"
                      onClick={() => void clearPassphrase()}
                    >
                      清除口令
                    </button>
                  )}
                </div>
              </>
            )}
          </Section>
        )}

        <Section title="使用它的资产" count={cred.usedBy.length}>
          {cred.usedBy.length > 0 ? (
            <div className="flex flex-wrap gap-1.5">
              {cred.usedBy.map((u) => {
                const Icon = assetIcon(u.kind, typeof (u as { options?: Record<string, unknown> }).options?.icon === "string" ? ((u as { options?: Record<string, unknown> }).options?.icon as string) : undefined);
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
            <div className="nx-hint">
              没有资产在用它。
            </div>
          )}
        </Section>

        <div className="mb-5 flex flex-wrap gap-x-5 gap-y-1 text-[11px] text-neutral-600">
          <span>创建于 {formatTime(cred.createdAt)}</span>
          <span>最后修改 {formatTime(cred.updatedAt)}</span>
        </div>

        <div className="flex items-center gap-2 border-t border-neutral-800/60 pt-4">
          <button
            className="nx-btn nx-btn-primary"
            disabled={!dirty || saving || locked}
            onClick={() => void save()}
          >
            {saving ? "保存中…" : "保存"}
          </button>
          {dirty && (
            <button
              className="nx-btn nx-btn-ghost"
              onClick={() => {
                setName(cred.name);
                setNewSecret("");
                setNewPassphrase("");
              }}
            >
              放弃修改
            </button>
          )}
          <div className="nx-spacer" />
          <button className="nx-btn nx-btn-ghost text-red-400" onClick={() => void del()}>
            删除
          </button>
        </div>
      </div>
    </div>
  );
}

function Section({
  title,
  count,
  children,
}: {
  title: string;
  count?: number;
  children: ReactNode;
}) {
  return (
    <div className="mb-4">
      <div className="mb-1.5 flex items-center gap-1.5 px-0.5">
        <span className="text-[11.5px] font-medium tracking-wide text-neutral-400">{title}</span>
        {count !== undefined && <span className="nx-count">{count}</span>}
      </div>
      <div className="rounded-lg border border-neutral-800/70 bg-neutral-950/40 p-3">{children}</div>
    </div>
  );
}

function EmptyDetail({ hasAny, onNew }: { hasAny: boolean; onNew: () => void }) {
  return (
    <div className="nx-empty">
      <div className="nx-empty-icon">
        <IconFile size={18} />
      </div>
      <div className="text-[12.5px] text-neutral-400">
        {hasAny ? "在左侧选择一条凭据" : "还没有任何凭据"}
      </div>
      <div className="text-[11.5px] text-neutral-600">
        {hasAny ? "名称 · 值 · 引用它的资产 · 最后修改时间" : "新建资产时填的密码会自动存进来"}
      </div>
      <div className="mt-1 flex gap-2">
        <button className="nx-btn nx-btn-sm" onClick={openCredentialsSidebar}>
          打开凭据列表
        </button>
        <button className="nx-btn nx-btn-sm nx-btn-primary" onClick={onNew}>
          <IconPlus size={12} />
          新建凭据
        </button>
      </div>
    </div>
  );
}
