import { useCallback, useEffect, useRef, useState } from "react";
import { syncApi } from "../../ipc/commands";
import type { SyncTokenDto } from "../../ipc/types";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import {
  IconChevronDown,
  IconChevronRight,
  IconCopy,
  IconInfo,
  IconKey,
  IconRefresh,
  IconShield,
  IconXCircle,
} from "../../ui/icons";

const DAY_MS = 24 * 60 * 60 * 1000;

const TTL_OPTIONS = [
  { label: "永不过期", ms: 0 },
  { label: "7 天", ms: 7 * DAY_MS },
  { label: "30 天", ms: 30 * DAY_MS },
  { label: "90 天", ms: 90 * DAY_MS },
];

const PURPOSE_PATTERN = /^[a-z][a-z0-9_-]{0,31}$/;

function formatTime(ms: number): string {
  return new Date(ms).toLocaleString();
}

function tokenStatus(t: SyncTokenDto, now: number): { text: string; tone: string } {
  if (t.revokedAt !== null) return { text: "已吊销", tone: "nx-badge-red" };
  if (t.expiresAt > 0 && now > t.expiresAt) return { text: "已过期", tone: "nx-badge-amber" };
  return { text: "生效中", tone: "nx-badge-green" };
}

function OneTimeSecret({ secret, label, onDone }: { secret: string; label: string; onDone: () => void }) {
  const { pushToast } = useUi();
  const [revealed, setRevealed] = useState(false);

  return (
    <div className="nx-alert nx-alert-danger mt-2 flex flex-col gap-1.5">
      <div className="flex items-start gap-2">
        <IconShield size={14} className="mt-0.5 shrink-0" />
        <div className="min-w-0 flex-1">
          <b>新令牌（{label}）只显示这一次</b>：服务端只保存散列，关闭后无法再查看。请立即复制并填到对应客户端。
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <code className="nx-code min-w-0 flex-1 truncate font-mono text-[12px]">
          {revealed ? secret : "•".repeat(32)}
        </code>
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => setRevealed((v) => !v)}>
          {revealed ? "隐藏" : "显示"}
        </button>
        <button
          className="nx-btn nx-btn-outline nx-btn-sm"
          onClick={() => {
            void navigator.clipboard
              ?.writeText(secret)
              .then(() => pushToast("success", "令牌已复制"))
              .catch(() => pushToast("error", "复制失败，请手动选中复制"));
          }}
        >
          <IconCopy size={12} />
          复制
        </button>
        <button className="nx-btn nx-btn-outline nx-btn-sm" onClick={onDone}>
          完成
        </button>
      </div>
    </div>
  );
}

export function SyncTokenPanel() {
  const { pushToast } = useUi();
  const [open, setOpen] = useState(false);
  const [tokens, setTokens] = useState<SyncTokenDto[] | null>(null);
  const [listError, setListError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [draft, setDraft] = useState({ clientId: "", purpose: "sync", ttlMs: 0 });
  const [formError, setFormError] = useState<string | null>(null);
  const [secret, setSecret] = useState<{ value: string; label: string } | null>(null);

  const load = useCallback(() => {
    setListError(null);
    return syncApi
      .tokenList()
      .then(setTokens)
      .catch((e: unknown) => setListError(describeError(e)));
  }, []);

  const autoLoadAttempted = useRef(false);
  useEffect(() => {
    if (open && !autoLoadAttempted.current) {
      autoLoadAttempted.current = true;
      void load();
    }
  }, [open, load]);

  const issue = async () => {
    const clientId = draft.clientId.trim();
    const purpose = draft.purpose.trim();
    if (!clientId || clientId.length > 64) {
      setFormError("客户端标识需为 1-64 个字符");
      return;
    }
    if (purpose && !PURPOSE_PATTERN.test(purpose)) {
      setFormError("用途需以小写字母开头，可含小写字母、数字、- 与 _，最长 32 字符");
      return;
    }
    setBusy(true);
    setFormError(null);
    try {
      const result = await syncApi.tokenIssue(clientId, purpose || undefined, draft.ttlMs || undefined);
      setSecret({ value: result.secret, label: `${clientId}（${result.token.purpose}）` });
      setDraft({ clientId: "", purpose: "sync", ttlMs: 0 });
      await load();
      pushToast("success", `已为 ${clientId} 签发令牌`);
    } catch (e) {
      setFormError(describeError(e));
    } finally {
      setBusy(false);
    }
  };

  const rotate = async (t: SyncTokenDto) => {
    const ok = await ask(
      `轮换 ${t.clientId}（${t.purpose}）的令牌？\n\n旧令牌立即失效，没有宽限期；客户端需要改填新令牌。`,
      { title: "轮换同步令牌", kind: "warning" },
    );
    if (!ok) return;
    try {
      const next = await syncApi.rotateToken(t.id);
      if (next) setSecret({ value: next, label: `${t.clientId}（${t.purpose}）` });
      await load();
      pushToast("success", "已轮换，旧令牌立即失效");
    } catch (e) {
      pushToast("error", describeError(e));
    }
  };

  const revoke = async (t: SyncTokenDto) => {
    const ok = await ask(
      `吊销 ${t.clientId}（${t.purpose}）的令牌？\n\n吊销后该客户端立即无法同步，且不可恢复，只能重新签发。`,
      { title: "吊销同步令牌", kind: "warning" },
    );
    if (!ok) return;
    try {
      await syncApi.tokenRevoke(t.id);
      await load();
      pushToast("success", `已吊销 ${t.clientId} 的令牌`);
    } catch (e) {
      pushToast("error", describeError(e));
    }
  };

  const now = Date.now();

  return (
    <div className="mt-3 border-t border-neutral-800/60 pt-3">
      <button
        className="nx-btn nx-btn-ghost nx-btn-sm"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        {open ? <IconChevronDown size={12} /> : <IconChevronRight size={12} />}
        客户端令牌（按客户端分发 · 轮换 · 吊销）
      </button>

      {open && (
        <div className="mt-2 flex flex-col gap-2">
          <div className="nx-alert nx-alert-info flex items-start gap-2">
            <IconInfo size={14} className="mt-0.5 shrink-0" />
            <div>
              给每台桌面设备签发独立令牌：按客户端与用途区分，可独立轮换与吊销，互不影响。
              令牌内容只在签发或轮换时显示一次；日志与审计只记录客户端标识与用途，
              <b>不记录令牌本身</b>。客户端令牌丢失或怀疑泄露时，用「轮换」立即换发（旧令牌即刻失效），
              或「吊销」直接禁用。
            </div>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <label className="w-[76px] shrink-0 text-[12px] text-neutral-400" htmlFor="sync-token-client">
              客户端标识
            </label>
            <input
              id="sync-token-client"
              className="nx-input min-w-0 flex-1"
              placeholder="例如 macbook-pro"
              value={draft.clientId}
              autoComplete="off"
              onChange={(e) => setDraft((d) => ({ ...d, clientId: e.target.value }))}
            />
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <label className="w-[76px] shrink-0 text-[12px] text-neutral-400" htmlFor="sync-token-purpose">
              用途
            </label>
            <input
              id="sync-token-purpose"
              className="nx-input min-w-0 font-mono min-[560px]:w-[180px]"
              value={draft.purpose}
              autoComplete="off"
              onChange={(e) => setDraft((d) => ({ ...d, purpose: e.target.value }))}
            />
            <span className="nx-hint">小写字母开头，可含数字 - _；sync 用于资产同步</span>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <label className="w-[76px] shrink-0 text-[12px] text-neutral-400" htmlFor="sync-token-ttl">
              有效期
            </label>
            <select
              id="sync-token-ttl"
              className="nx-input min-w-0 min-[560px]:w-[140px]"
              value={draft.ttlMs}
              onChange={(e) => setDraft((d) => ({ ...d, ttlMs: Number(e.target.value) }))}
            >
              {TTL_OPTIONS.map((o) => (
                <option key={o.ms} value={o.ms}>
                  {o.label}
                </option>
              ))}
            </select>
            <div className="nx-spacer" />
            <button
              className="nx-btn nx-btn-primary nx-btn-sm"
              disabled={busy || !draft.clientId.trim()}
              onClick={() => void issue()}
            >
              {busy ? <IconRefresh size={12} className="animate-spin" /> : <IconKey size={12} />}
              {busy ? "签发中…" : "签发令牌"}
            </button>
          </div>

          {formError && (
            <div className="nx-alert nx-alert-danger flex items-start gap-2">
              <IconXCircle size={13} className="mt-0.5 shrink-0" />
              <span className="min-w-0 flex-1 break-words">{formError}</span>
            </div>
          )}

          {secret && <OneTimeSecret secret={secret.value} label={secret.label} onDone={() => setSecret(null)} />}

          {listError && (
            <div className="nx-alert nx-alert-danger flex items-start gap-2">
              <IconXCircle size={13} className="mt-0.5 shrink-0" />
              <span className="min-w-0 flex-1 break-words">令牌列表读取失败 · {listError}</span>
              <button className="nx-btn nx-btn-ghost nx-btn-sm shrink-0" onClick={() => void load()}>
                <IconRefresh size={12} />
                重试
              </button>
            </div>
          )}

          {tokens && tokens.length === 0 && (
            <div className="nx-hint py-1 text-[12px]">还没有签发过客户端令牌。</div>
          )}

          {tokens && tokens.length > 0 && (
            <div className="max-h-[260px] overflow-y-auto rounded border border-neutral-800/60">
              {tokens.map((t) => {
                const st = tokenStatus(t, now);
                const isAdmin = t.id === "admin";
                const revoked = t.revokedAt !== null;
                return (
                  <div
                    key={t.id}
                    className="flex flex-wrap items-center gap-x-2 gap-y-1 border-b border-neutral-800/40 px-2.5 py-1.5 last:border-b-0"
                  >
                    <span className="min-w-0 flex-1 truncate text-[12.5px] text-neutral-200" title={t.clientId}>
                      {t.clientId}
                    </span>
                    {isAdmin && <span className="nx-badge shrink-0 nx-badge-blue">管理员</span>}
                    <span className="nx-badge shrink-0" title="令牌用途">
                      {t.purpose}
                    </span>
                    <span className={`nx-badge shrink-0 ${st.tone}`}>{st.text}</span>
                    <span className="nx-hint shrink-0 text-[11px]">
                      签发 {formatTime(t.createdAt)}
                    </span>
                    <span className="nx-hint shrink-0 text-[11px]">
                      {t.expiresAt > 0 ? `过期 ${formatTime(t.expiresAt)}` : "永不过期"}
                    </span>
                    <span className="nx-hint shrink-0 text-[11px]">
                      {t.lastUsedAt ? `最近使用 ${formatTime(t.lastUsedAt)}` : "从未使用"}
                    </span>
                    <button
                      className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
                      disabled={revoked && !isAdmin}
                      title={revoked && !isAdmin ? "已吊销的令牌不能轮换" : "轮换后立即换发新令牌，旧令牌即刻失效"}
                      onClick={() => void rotate(t)}
                    >
                      轮换
                    </button>
                    <button
                      className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
                      disabled={isAdmin || revoked}
                      title={isAdmin ? "管理员令牌不可吊销，请改用轮换" : revoked ? "已吊销" : "吊销后不可恢复"}
                      onClick={() => void revoke(t)}
                    >
                      吊销
                    </button>
                  </div>
                );
              })}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
