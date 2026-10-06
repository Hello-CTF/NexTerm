// 凭据详情页（主区）。配合 CredentialsSidebar：**左栏负责"找"与"切"，这里负责"看"与"改"**。
//
// 几个刻意的选择：
// · 头部就一件事 —— 大图标 + 名称 + 「这是干什么用的」，其余全部下沉到卡片；
// · 私钥多两张卡片：「来源」（引用本地文件 / 已入库）与「口令」——
//   口令是私钥的一个属性，跟私钥同一条凭据，所以显示也在私钥的详情里；
// · 值默认打码，明文 15 秒自动打回（复制不走打码，复制的是真值）；
// · 锁定态列表照旧可达，值一律不可见，解锁入口在顶部与左栏。
import { useEffect, useRef, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { vaultApi, type Credential, type CredentialSource, type RevealedCredential } from "../../ipc/commands";
import { ask } from "../../ui/dialogs";
import { connectAsset, openCredentialsSidebar, openCredentialsViewTab, useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import { NewCredentialModal } from "./NewCredentialModal";
import { kindMeta, formatTime } from "./meta";
import { useRefreshCredentials, useVaultUnlock } from "./useVaultUnlock";
import { WEB } from "../../demo";
import {
  assetIcon,
  IconCode,
  IconCopy,
  IconEye,
  IconEyeOff,
  IconFile,
  IconKey,
  IconLock,
  IconPlus,
} from "../../ui/icons";

/** 明文回显的自动打回时间（交互评审拍板：15 秒）。 */
const REVEAL_SECONDS = 15;

export function CredentialsPanel({ credId }: { credId?: string }) {
  const refresh = useRefreshCredentials();
  const unlock = useVaultUnlock();
  const [creating, setCreating] = useState(false);

  const status = useQuery({ queryKey: ["vault-status"], queryFn: () => vaultApi.status() });
  const credentials = useQuery({ queryKey: ["credentials"], queryFn: () => vaultApi.listCredentials() });

  const st = status.data;
  const locked = !!st?.initialized && !st.unlocked;
  const protectionOn = st?.mode === "master";
  const list = credentials.data ?? [];
  const selected = list.find((c) => c.id === credId) ?? null;

  return (
    <div className="flex h-full flex-col bg-neutral-900">
      {/* 工具条 */}
      <div className="flex h-[38px] shrink-0 items-center gap-2 border-b border-neutral-800/60 px-3">
        <IconKey size={14} className="text-neutral-400" />
        <span className="text-[13px] font-semibold text-neutral-100">凭据</span>
        {st &&
          (protectionOn ? (
            locked ? (
              <span className="nx-badge nx-badge-amber">
                <IconLock size={11} /> 已锁定
              </span>
            ) : (
              <span className="nx-badge nx-badge-green">已解锁</span>
            )
          ) : (
            <span className="nx-badge">未启用密码保护</span>
          ))}
        <div className="nx-spacer" />
        <button
          className="nx-btn nx-btn-outline nx-btn-sm"
          title="以文本 / JSON 查看全部凭据"
          onClick={() => openCredentialsViewTab("text")}
        >
          <IconCode size={12} />
          凭据视图
        </button>
        <button className="nx-btn nx-btn-primary nx-btn-sm" onClick={() => setCreating(true)}>
          <IconPlus size={12} />
          新建凭据
        </button>
      </div>

      {/* 锁定横幅 */}
      {locked && (
        <div className="flex shrink-0 items-center gap-2 border-b border-amber-500/20 bg-amber-500/10 px-3 py-1.5 text-[12px] text-amber-300/90">
          <IconLock size={13} />
          {WEB ? (
            // 服务端：密码由部署方托管，用户无从输入 ⇒ 不给「解锁」按钮，
            // 只说明恢复方式（重启后 bootstrap 会用部署密钥自动解锁）。
            <span>凭据库已锁定 · 重启服务端后自动恢复</span>
          ) : (
            <>
              <span>凭据库已锁定 · 解锁后可查看与使用</span>
              <div className="nx-spacer" />
              <button className="nx-btn nx-btn-sm" onClick={() => void unlock()}>
                解锁
              </button>
            </>
          )}
        </div>
      )}

      {selected ? (
        <CredentialDetail
          // 切凭据时重挂：名称草稿、明文倒数这些局部状态应当跟着换人清空
          key={selected.id}
          cred={selected}
          locked={locked}
          onChanged={refresh}
          onDeleted={refresh}
        />
      ) : (
        <EmptyDetail hasAny={list.length > 0} onNew={() => setCreating(true)} />
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
  const meta = kindMeta(cred.kind);
  const isKey = cred.kind === "private_key";
  /** 引用型私钥：库里只有路径，没有正文 —— 「显示」拿不到内容。 */
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

  /** 显示明文：拉取真值，15 秒倒数后自动打回。口令与值一次拿回。 */
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

  const copy = async (text: string, label: string) => {
    try {
      const value = text || (await vaultApi.revealCredential(cred.id)).value;
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
        // 私钥要带上来源：引用型改的是路径，内容型改的是正文。
        // 不带的话后端会用"原载荷"推断，这里显式给更稳。
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
    const ok = await ask(
      "清除这条私钥的口令？\n清除后，需要口令才能解开的私钥将无法连接。",
    );
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
        {/* 头部：图标 + 可改的名称 + 一句话说明 */}
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
                <span className="text-amber-500/90">未被引用 · 可安全删除</span>
              )}
            </div>
          </div>
        </div>

        {/* 值 */}
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
                  // 引用型没有正文可显示，展示的就是那条路径本身
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
                  onClick={() => void copy(isRef ? cred.refPath ?? "" : revealed?.value ?? "", "已复制")}
                >
                  <IconCopy size={12} />
                  复制
                </button>
              </div>
              {revealed !== null && !isRef && (
                <div className="mt-2 flex items-center gap-1.5 text-[11px] text-amber-400/90">
                  <IconEye size={11} />
                  {left}s 后自动隐藏
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
                      ↳ 引用型只记路径，库里不保存私钥正文 —— 文件被挪走就失效，那时来这里改路径。
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
                    保存后，使用它的 {cred.usedBy.length} 个资产下次连接即生效 —— 不用逐个去改。
                  </div>
                )}
              </div>
            </>
          )}
        </Section>

        {/* 私钥口令：跟私钥同一条凭据，所以显示在私钥详情里 */}
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
                        onClick={() => void copy(revealed?.passphrase ?? "", "已复制口令")}
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
                    选填。口令跟私钥存在同一条凭据里，改这里不必重新提供私钥。
                  </span>
                  {cred.hasPassphrase && newPassphrase.length === 0 && (
                    <button
                      className="nx-btn nx-btn-ghost nx-btn-sm text-amber-400"
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

        {/* 使用它的资产 */}
        <Section title="使用它的资产" count={cred.usedBy.length}>
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
            <div className="nx-hint">
              没有资产在用它。改完值可以在这里确认有没有漏掉的引用。
            </div>
          )}
        </Section>

        {/* 时间 */}
        <div className="mb-5 flex flex-wrap gap-x-5 gap-y-1 text-[11px] text-neutral-600">
          <span>创建于 {formatTime(cred.createdAt)}</span>
          <span>最后修改 {formatTime(cred.updatedAt)}</span>
        </div>

        {/* 操作 */}
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

/** 详情卡片：小标题 + 内容（凭据页的几种信息各成一张）。 */
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

/** 没选中凭据时的空态：左栏被收起时给一条回列表的路。 */
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
