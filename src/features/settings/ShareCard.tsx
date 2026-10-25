// Terminal share card (SHARE158): the real share management UI in account mode.
// All data comes from real HTTP contracts (internal/fleet/server/sharing_http.go and http.go):
// - Host shares (POST/GET /share/host-shares + /share/host-shares/{id}/revoke):
//   a registered user shares an enrolled device (a host with the device agent) with another registered user, who opens
//   terminals via the device agent within the TTL, never touching the host password or private keys. The create body takes recipient_username,
//   resolved exactly by the server (case-insensitive); the frontend needs no user directory (/admin/users is superadmin-only),
//   and plain device owners share the same create path as superadmins; all TTL options fall within the server-side 1 minute-30 day
//   bounds.
// - Legacy public links (GET /share/links + /share/links/{id}/revoke): existing session links remain
//   viewable and revocable for one compatibility cycle, but cannot be created; new public access uses
//   the terminal's existing preview-link menu. List responses never carry tokens.
// The server filters the list by owner/superadmin; the desktop proxies the same contracts (remote_share_*) through the
// "account sync" link server session; logged out, a "log in to a server account to use this" guide card
// points to the "account sync" section, and no request is issued.

import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { type FleetDevice } from "../../ipc/fleetApi";
import {
  type HostShareView,
  type ShareLinkView,
  type SharePermission,
} from "../../ipc/sharingApi";
import { useAuth } from "../auth/store";
import { useUi } from "../../app/store";
import { WEB } from "../../ipc/env";
import { remoteHostShareApi } from "../../ipc/remoteApi";
import {
  browserHostShareIdentityScope,
  hostShareIdentityScope,
} from "../../ipc/hostShareApi";
import { fleetClient, sharingClient } from "../fleet/clients";
import { useFleetAccount } from "../fleet/useFleetAccount";
import { HostShareTerminalApp } from "../sharing/HostShareTerminalApp";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { formatTime } from "../../ui/format";
import {
  IconGlobe,
  IconInfo,
  IconPlus,
  IconRefresh,
  IconXCircle,
} from "../../ui/icons";
import { showSettingsSection } from "./settingsNavigation";

// All TTL options fall within the server-side minShareTTL(1 minute)-maxShareTTL(30 days) bounds;
// the 24h default aligns with the server defaultHostShareTTL, and ttl_ms is always sent explicitly on submit.
const SHARE_TTL_OPTIONS = [
  { label: "1 小时", ms: 60 * 60_000 },
  { label: "6 小时", ms: 6 * 60 * 60_000 },
  { label: "24 小时", ms: 24 * 60 * 60_000 },
  { label: "7 天", ms: 7 * 24 * 60 * 60_000 },
  { label: "30 天", ms: 30 * 24 * 60 * 60_000 },
];

function shortId(id: string): string {
  return id.length > 10 ? `${id.slice(0, 8)}…` : id;
}

// permissionBadge displays anything outside the contract as read-only (consistent with publicShareApi's parsing).
function permissionBadge(permission: SharePermission): { text: string; tone: string } {
  return permission === "read_write" ? { text: "读写", tone: "nx-badge-amber" } : { text: "只读", tone: "" };
}

function shareState(expiresAt: number, revokedAt: number | undefined, now: number): { text: string; tone: string } {
  if (revokedAt) return { text: "已吊销", tone: "nx-badge-red" };
  if (now >= expiresAt) return { text: "已过期", tone: "nx-badge-amber" };
  return { text: "有效", tone: "nx-badge-green" };
}

function ShareUnsupported() {
  return (
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <IconGlobe size={15} className="text-neutral-400" />
        <span className="nx-card-title">分享</span>
        <span className="nx-badge nx-badge-amber">登录服务端账号后可用</span>
      </div>
      <p className="nx-hint">
        主机分享与公开链接依赖服务端账号体系。在「设置 → 账号同步」登录服务端账号后，即可把接入的设备分享给其他注册用户并管理公开链接。
      </p>
      <div className="mt-3">
        <button
          type="button"
          className="nx-btn nx-btn-outline nx-btn-sm"
          onClick={() => showSettingsSection("settings-account")}
        >
          去设置 → 账号同步登录
        </button>
      </div>
    </section>
  );
}

function ShareLoginHint() {
  // Logged out, no second login button here (the only login entry is the "account" card); this section only states what it needs.
  return (
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <IconGlobe size={15} className="text-neutral-400" />
        <span className="nx-card-title">分享</span>
      </div>
      <p className="nx-hint">登录后可以把你接入的设备分享给其他注册用户，并管理已创建的公开链接。</p>
    </section>
  );
}

function ShareAuthOff() {
  return (
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <IconGlobe size={15} className="text-neutral-400" />
        <span className="nx-card-title">分享</span>
        <span className="nx-badge nx-badge-amber">账号功能已关闭</span>
      </div>
      <p className="nx-hint">
        这台服务器关闭了账号功能（--auth=off），终端分享不可用；主机分享与公开链接都依赖账号体系。
      </p>
    </section>
  );
}

export function ShareCard() {
  const authStatus = useAuth((s) => s.status);
  const account = useFleetAccount();
  const identityLink = useQuery({
    queryKey: ["sync-link", "host-share-identity"],
    queryFn: remoteHostShareApi.identity,
    enabled: !WEB && Boolean(account.userId),
    staleTime: 30_000,
    retry: false,
  });
  const needsDesktopIdentity = !WEB && Boolean(account.userId);
  // auth=off is only detectable on WEB (desktop login itself relies on the account system; logging in implies auth=on).
  if (WEB && authStatus?.auth === "off") return <ShareAuthOff />;
  const identityMismatch = needsDesktopIdentity && identityLink.isSuccess && identityLink.data.userId !== account.userId;
  const initialIdentityFailure = needsDesktopIdentity && identityLink.isError && identityLink.data === undefined;
  if (needsDesktopIdentity && (initialIdentityFailure || identityMismatch)) {
    return (
      <section className="nx-card">
        <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
          <IconGlobe size={15} className="text-neutral-400" />
          <span className="nx-card-title">分享</span>
          <span className="nx-hint">账号身份信息不可用，请重新登录。</span>
        </div>
      </section>
    );
  }
  if (account.loading || (needsDesktopIdentity && identityLink.isPending)) {
    return (
      <section className="nx-card">
        <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
          <IconGlobe size={15} className="text-neutral-400" />
          <span className="nx-card-title">分享</span>
          <span className="nx-hint">账号状态读取中…</span>
        </div>
      </section>
    );
  }
  if (!account.userId) return WEB ? <ShareLoginHint /> : <ShareUnsupported />;
  const scopeKey = WEB
    ? browserHostShareIdentityScope(account.userId)
    : hostShareIdentityScope(identityLink.data!.serverUrl, identityLink.data!.userId, identityLink.data!.generation);
  return <ShareManagement userId={account.userId} scopeKey={scopeKey} isAdmin={account.isAdmin} />;
}

function ShareManagement({ userId, scopeKey, isAdmin }: { userId: string; scopeKey: string; isAdmin: boolean }) {
  const pushToast = useUi((s) => s.pushToast);

  const [devices, setDevices] = useState<FleetDevice[] | null>(null);
  const [shares, setShares] = useState<HostShareView[] | null>(null);
  const [links, setLinks] = useState<ShareLinkView[] | null>(null);
  const [listError, setListError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [now, setNow] = useState(() => Date.now());

  const [createOpen, setCreateOpen] = useState(false);
  const [deviceId, setDeviceId] = useState("");
  const [recipientUsername, setRecipientUsername] = useState("");
  const [ttlMs, setTtlMs] = useState(SHARE_TTL_OPTIONS[2].ms);
  const [write, setWrite] = useState(false);
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [activeShare, setActiveShare] = useState<{ deviceId: string; deviceName: string; ownerUsername: string } | null>(null);

  const loadSeq = useRef(0);
  // accountEpoch bumps only on account switch (manual refresh leaves it alone), isolating in-flight create/revoke
  // then/catch/finally: late completions from an old account never write into the new account's view.
  const accountEpoch = useRef(0);

  // Account-scope isolation covers normalized server URL, user and login generation. Any scope change resets the
  // list/viewer and invalidates late responses, including a same-user switch to another server.
  const [scopedScopeKey, setScopedScopeKey] = useState(scopeKey);
  if (scopedScopeKey !== scopeKey) {
    setScopedScopeKey(scopeKey);
    loadSeq.current += 1;
    accountEpoch.current += 1;
    setDevices(null);
    setShares(null);
    setLinks(null);
    setListError(null);
    setCreateOpen(false);
    setDeviceId("");
    setRecipientUsername("");
    setTtlMs(SHARE_TTL_OPTIONS[2].ms);
    setWrite(false);
    setCreateError(null);
    setCreating(false);
    setLoading(false);
    setActiveShare(null);
  }

  const load = useCallback(() => {
    const seq = ++loadSeq.current;
    setLoading(true);
    setListError(null);
    const fleet = fleetClient();
    const sharing = sharingClient();
    const tasks = [
      fleet.devices().then(
        (r) => {
          if (seq === loadSeq.current) setDevices(r.devices);
        },
        (e: unknown) => {
          if (seq === loadSeq.current) setListError(describeError(e));
        },
      ),
      sharing.hostShares().then(
        (r) => {
          if (seq === loadSeq.current) setShares(r.shares);
        },
        (e: unknown) => {
          if (seq === loadSeq.current) setListError(describeError(e));
        },
      ),
      sharing.links().then(
        (r) => {
          if (seq === loadSeq.current) setLinks(r.links);
        },
        (e: unknown) => {
          if (seq === loadSeq.current) setListError(describeError(e));
        },
      ),
    ];
    void Promise.all(tasks).finally(() => {
      if (seq === loadSeq.current) setLoading(false);
    });
  }, []);

  useEffect(() => {
    load();
  }, [scopeKey, load]);

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(timer);
  }, []);

  const deviceName = (id: string) => devices?.find((d) => d.id === id)?.name ?? shortId(id);
  // The server requires share targets to be enrolled devices (device_agent row + terminal enabled); revoked devices cannot be shared.
  const shareableDevices = (devices ?? []).filter((d) => d.revoked_at === 0 && d.agent && d.agent.terminal_enabled);

  const submitCreate = async () => {
    const recipient = recipientUsername.trim();
    if (!deviceId || !recipient) return;
    const epoch = accountEpoch.current;
    setCreating(true);
    setCreateError(null);
    try {
      await sharingClient().createHostShare({ deviceId, recipientUsername: recipient, write, ttlMs });
      if (epoch !== accountEpoch.current) return;
      pushToast("success", `已将主机「${deviceName(deviceId)}」分享给 ${recipient}`);
      setCreateOpen(false);
      setDeviceId("");
      setRecipientUsername("");
      setWrite(false);
      load();
    } catch (e) {
      if (epoch !== accountEpoch.current) return;
      setCreateError(describeError(e));
    } finally {
      if (epoch === accountEpoch.current) setCreating(false);
    }
  };

  const revokeHostShare = async (share: HostShareView) => {
    // The epoch must be captured before the confirmation opens: if the account switches while the dialog is open, the old account's row must not be revoked in the new session.
    const epoch = accountEpoch.current;
    const ok = await ask(
      `吊销这条主机分享？\n\n「${share.recipient_username}」将无法再通过此分享在「${deviceName(share.device_id)}」上新建终端，进行中的连接也会尽快断开。`,
      { title: "吊销主机分享", kind: "warning" },
    );
    if (!ok) return;
    if (epoch !== accountEpoch.current) return;
    try {
      await sharingClient().revokeHostShare(share.id);
      if (epoch !== accountEpoch.current) return;
      pushToast("success", `已吊销「${share.recipient_username}」的主机分享`);
      load();
    } catch (e) {
      if (epoch !== accountEpoch.current) return;
      pushToast("error", describeError(e));
    }
  };

  const revokeLink = async (link: ShareLinkView) => {
    // The epoch must be captured before the confirmation opens (same as revokeHostShare).
    const epoch = accountEpoch.current;
    const ok = await ask("吊销这条公开链接？\n\n链接立即失效，持有链接的人将无法再打开对应会话。", {
      title: "吊销公开链接",
      kind: "warning",
    });
    if (!ok) return;
    if (epoch !== accountEpoch.current) return;
    try {
      await sharingClient().revokeLink(link.id);
      if (epoch !== accountEpoch.current) return;
      pushToast("success", "已吊销公开链接");
      load();
    } catch (e) {
      if (epoch !== accountEpoch.current) return;
      pushToast("error", describeError(e));
    }
  };

  return (
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <IconGlobe size={15} className="text-neutral-400" />
        <span className="nx-card-title">分享</span>
        {shares !== null && <span className="nx-badge">{shares.length} 条主机分享</span>}
        {links !== null && <span className="nx-badge">{links.length} 条旧版链接</span>}
        <div className="nx-spacer" />
        <button
          className="nx-btn nx-btn-outline nx-btn-sm"
          onClick={() => setCreateOpen((v) => !v)}
          aria-expanded={createOpen}
        >
          <IconPlus size={12} />
          新建主机分享
        </button>
        <button className="nx-btn nx-btn-ghost nx-btn-sm" disabled={loading} onClick={() => void load()}>
          <IconRefresh size={12} className={loading ? "animate-spin" : ""} />
          刷新
        </button>
      </div>

      <p className="nx-hint mb-3">
        主机分享允许另一个注册用户在有效期内通过设备代理在你的接入设备上新建终端。旧版公开链接只能在这里查看和吊销；新的公开访问请使用终端中的「预览链接」。
      </p>

      {createOpen && (
        <div className="mb-3 flex flex-col gap-2 border-b border-neutral-800/60 pb-3">
          <div className="flex flex-wrap items-center gap-2">
            <select
              className="nx-select min-w-0 flex-1 basis-40"
              value={deviceId}
              aria-label="分享主机"
              onChange={(e) => setDeviceId(e.target.value)}
            >
              <option value="">选择主机</option>
              {shareableDevices.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.name}
                </option>
              ))}
            </select>
            <input
              className="nx-input min-w-0 flex-1 basis-40"
              placeholder="接收者的用户名"
              value={recipientUsername}
              autoComplete="off"
              onChange={(e) => setRecipientUsername(e.target.value)}
            />
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <select
              className="nx-select shrink-0"
              style={{ width: 120 }}
              value={ttlMs}
              aria-label="分享有效期"
              onChange={(e) => setTtlMs(Number(e.target.value))}
            >
              {SHARE_TTL_OPTIONS.map((o) => (
                <option key={o.ms} value={o.ms}>
                  {o.label}
                </option>
              ))}
            </select>
            <label className="flex items-center gap-1.5 text-[12px] text-neutral-300">
              <input type="checkbox" className="h-4 w-4" checked={write} onChange={(e) => setWrite(e.target.checked)} />
              允许读写
            </label>
            <span className="nx-hint text-[11px]">默认只读；勾选后，对方才能在终端中输入。</span>
            <div className="nx-spacer" />
            <button
              className="nx-btn nx-btn-primary"
              disabled={creating || !deviceId || !recipientUsername.trim()}
              onClick={() => void submitCreate()}
            >
              {creating ? "创建中…" : "创建分享"}
            </button>
          </div>
          {devices !== null && shareableDevices.length === 0 && (
            <p className="nx-hint text-[11px]">没有可分享的设备：请先在「设备管理」中接入设备并开启远程终端。</p>
          )}
          {createError && (
            <div className="nx-alert nx-alert-danger flex items-start gap-2">
              <IconXCircle size={13} className="mt-0.5 shrink-0" />
              <span className="min-w-0 flex-1 break-words">{createError}</span>
            </div>
          )}
        </div>
      )}

      {listError && (
        <div className="nx-alert nx-alert-danger mb-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">分享数据加载失败 · {listError}</span>
          <button className="nx-btn nx-btn-ghost nx-btn-sm shrink-0" onClick={() => void load()} disabled={loading}>
            <IconRefresh size={12} />
            重试
          </button>
        </div>
      )}

      <div className="mb-1 text-[12.5px] text-neutral-200">主机分享</div>
      {shares !== null && shares.length > 0 && (
        <div className="max-h-[240px] overflow-y-auto rounded border border-neutral-800/60">
          {shares.map((s) => {
            const state = shareState(s.expires_at, s.revoked_at, now);
            const perm = permissionBadge(s.permission);
            const direction =
              s.owner_id === userId
                ? `分享给 ${s.recipient_username}`
                : s.recipient_id === userId
                  ? `来自 ${s.owner_username}`
                  : `${s.owner_username} 分享给 ${s.recipient_username}`;
            const canRevoke = isAdmin || s.owner_id === userId || (devices?.some((d) => d.id === s.device_id) ?? false);
            const canOpen = s.recipient_id === userId && !s.revoked_at && now < s.expires_at;
            return (
              <div
                key={s.id}
                className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-neutral-800/40 px-2.5 py-1.5 last:border-b-0"
              >
                <span className="min-w-0 flex-1 truncate text-[12.5px] text-neutral-200" title={deviceName(s.device_id)}>
                  {deviceName(s.device_id)}
                </span>
                <span className="nx-hint min-w-0 break-words text-[11px]">{direction}</span>
                <span className={`nx-badge shrink-0 ${perm.tone}`}>{perm.text}</span>
                <span className={`nx-badge shrink-0 ${state.tone}`}>{state.text}</span>
                <span className="nx-hint min-w-0 text-[11px]">有效期至 {formatTime(s.expires_at)}</span>
                {canOpen && (
                  <button
                    className="nx-btn nx-btn-outline nx-btn-sm shrink-0"
                    onClick={() => setActiveShare({
                      deviceId: s.device_id,
                      deviceName: deviceName(s.device_id),
                      ownerUsername: s.owner_username,
                    })}
                  >
                    打开终端
                  </button>
                )}
                {canRevoke && !s.revoked_at && (
                  <button className="nx-btn nx-btn-ghost nx-btn-sm shrink-0" onClick={() => void revokeHostShare(s)}>
                    吊销
                  </button>
                )}
              </div>
            );
          })}
        </div>
      )}
      {shares === null && !listError && <div className="nx-hint py-1.5 text-[11.5px]">分享数据加载中…</div>}
      {shares !== null && shares.length === 0 && !listError && (
        <div className="nx-hint py-1.5 text-[11.5px]">还没有主机分享。</div>
      )}

      <div className="mb-1 mt-3 text-[12.5px] text-neutral-200">旧版链接（只可吊销）</div>
      {links !== null && links.length > 0 && (
        <div className="max-h-[240px] overflow-y-auto rounded border border-neutral-800/60">
          {links.map((l) => {
            const state = shareState(l.expires_at, l.revoked_at, now);
            const perm = permissionBadge(l.permission);
            const canRevoke = isAdmin || l.owner_id === userId || (devices?.some((d) => d.id === l.device_id) ?? false);
            return (
              <div
                key={l.id}
                className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-neutral-800/40 px-2.5 py-1.5 last:border-b-0"
              >
                <span className="min-w-0 flex-1 truncate text-[12.5px] text-neutral-200" title={`${deviceName(l.device_id)} · 会话 ${l.session_id}`}>
                  {deviceName(l.device_id)} · 会话 {shortId(l.session_id)}
                </span>
                <span className={`nx-badge shrink-0 ${perm.tone}`}>{perm.text}</span>
                <span className={`nx-badge shrink-0 ${state.tone}`}>{state.text}</span>
                <span className="nx-hint min-w-0 text-[11px]">有效期至 {formatTime(l.expires_at)}</span>
                {l.last_accessed_at !== undefined && (
                  <span className="nx-hint min-w-0 text-[11px]">最近访问 {formatTime(l.last_accessed_at)}</span>
                )}
                {canRevoke && !l.revoked_at && (
                  <button className="nx-btn nx-btn-ghost nx-btn-sm shrink-0" onClick={() => void revokeLink(l)}>
                    吊销
                  </button>
                )}
              </div>
            );
          })}
        </div>
      )}
      {links === null && !listError && <div className="nx-hint py-1.5 text-[11.5px]">旧版链接加载中…</div>}
      {links !== null && links.length === 0 && !listError && (
        <div className="nx-hint py-1.5 text-[11.5px]">还没有旧版链接。新的公开访问请使用终端中的「预览链接」。</div>
      )}

      <div className="nx-alert nx-alert-info mt-3 flex items-start gap-2">
        <IconInfo size={14} className="mt-0.5 shrink-0" />
        <div>
          列表由服务端按账号过滤：普通用户只能查看自己授予或接收的分享，以及自己创建的公开链接；超级管理员可查看全部。吊销立即生效；主机分享和公开链接都不会向访问者泄露主机密码或私钥。
        </div>
      </div>
      {activeShare && (
        <HostShareTerminalApp
          deviceId={activeShare.deviceId}
          deviceName={activeShare.deviceName}
          ownerUsername={activeShare.ownerUsername}
          onClose={() => setActiveShare(null)}
        />
      )}
    </section>
  );
}
