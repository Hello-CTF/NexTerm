// 终端分享卡片 (SHARE158): WEB 账号模式下的真实分享管理界面。
// 数据全部来自真实 HTTP 合同 (internal/fleet/server/sharing_http.go 与 http.go):
// - 主机分享 (POST/GET /share/host-shares + /share/host-shares/{id}/revoke):
//   注册用户把 daemon 主机分享给另一个注册用户, 对方在有效期内经 host agent
//   新建终端, 全程不接触主机密码或私钥。创建体收 recipient_username, 由服务端
//   精确解析 (大小写不敏感), 前端不需要用户目录 (/admin/users 仅超管),
//   普通设备 owner 与超管同一创建路径; 有效期选项全部落在服务端 1 分钟-30 天
//   边界内。
// - 公开链接 (GET /share/links + /share/links/{id}/revoke): 本切片只有列表与
//   吊销, 没有创建入口, 也不展示任何 token (列表合同不含 token, 一次性 token
//   只可能来自真实创建响应)。
// 列表由服务端按 owner/superadmin 过滤; 桌面端与演示模式没有分享后端,
// 一律给显式不可用态, 不发任何请求 (DEMO 也不落 demoAuthRequest 假后端)。

import { useCallback, useEffect, useRef, useState } from "react";
import { fleetApi, type FleetDevice } from "../../ipc/fleetApi";
import {
  sharingApi,
  type HostShareView,
  type ShareLinkView,
  type SharePermission,
} from "../../ipc/sharingApi";
import { useAuth } from "../auth/store";
import { useUi } from "../../app/store";
import { DEMO, WEB } from "../../demo";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import {
  IconGlobe,
  IconInfo,
  IconPlus,
  IconRefresh,
  IconXCircle,
} from "../../ui/icons";

// 有效期选项全部落在服务端 minShareTTL(1 分钟)-maxShareTTL(30 天) 边界内;
// 默认 24 小时与服务端 defaultHostShareTTL 对齐, 提交时总是显式带 ttl_ms。
const SHARE_TTL_OPTIONS = [
  { label: "1 小时", ms: 60 * 60_000 },
  { label: "6 小时", ms: 6 * 60 * 60_000 },
  { label: "24 小时", ms: 24 * 60 * 60_000 },
  { label: "7 天", ms: 7 * 24 * 60 * 60_000 },
  { label: "30 天", ms: 30 * 24 * 60 * 60_000 },
];

function formatTime(ms: number): string {
  if (!ms) return "从未";
  return new Date(ms).toLocaleString();
}

function shortId(id: string): string {
  return id.length > 10 ? `${id.slice(0, 8)}…` : id;
}

// permissionBadge 对合同外的一律按只读展示 (与 publicShareApi 的解析口径一致)。
function permissionBadge(permission: SharePermission): { text: string; tone: string } {
  return permission === "read_write" ? { text: "读写", tone: "nx-badge-amber" } : { text: "只读", tone: "" };
}

function shareState(expiresAt: number, revokedAt: number | undefined, now: number): { text: string; tone: string } {
  if (revokedAt) return { text: "已吊销", tone: "nx-badge-red" };
  if (now >= expiresAt) return { text: "已过期", tone: "nx-badge-amber" };
  return { text: "有效", tone: "nx-badge-green" };
}

function ShareUnsupported() {
  const demo = DEMO;
  return (
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <IconGlobe size={15} className="text-neutral-400" />
        <span className="nx-card-title">分享</span>
        <span className="nx-badge nx-badge-amber">{demo ? "演示模式不可用" : "桌面端不可用"}</span>
      </div>
      <p className="nx-hint">
        {demo
          ? "演示模式不连接真实服务器, 不会伪造分享列表。终端分享走真实账号与 HTTP 合同, 请用浏览器模式连接真实服务器。"
          : "桌面端是本地优先模式, 没有账号体系; 终端分享仅在浏览器模式连接服务器并登录后可用。"}
      </p>
    </section>
  );
}

function ShareLoginHint() {
  // 未登录不放第二个登录按钮(唯一登录入口在「账号」卡);只说明这一节需要什么。
  return (
    <section className="nx-card">
      <div className="mb-1 flex items-center gap-2">
        <IconGlobe size={15} className="text-neutral-400" />
        <span className="nx-card-title">分享</span>
      </div>
      <p className="nx-hint">登录后可以把你接入的守护主机分享给其他注册用户, 并管理已创建的公开链接。</p>
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
        这台服务器关闭了账号功能(--auth=off), 终端分享不可用; 主机分享与公开链接都依赖账号体系。
      </p>
    </section>
  );
}

export function ShareCard() {
  const authStatus = useAuth((s) => s.status);
  if (!WEB) return <ShareUnsupported />;
  if (authStatus?.auth === "off") return <ShareAuthOff />;
  return <ShareManagement />;
}

function ShareManagement() {
  const { pushToast } = useUi();
  const user = useAuth((s) => s.user);
  const isAdmin = user?.role === "superadmin";
  const userId = user?.id ?? null;

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

  const loadSeq = useRef(0);
  // accountEpoch 只在账号切换时递增 (手动刷新不动它), 隔离创建/吊销在途的
  // then/catch/finally: 旧账号的晚到完成一律不写新账号视图。
  const accountEpoch = useRef(0);

  // 账号作用域隔离: user.id 变化时在渲染期重置全部账号态 (列表/创建表单),
  // 并递增 loadSeq 使旧账号的晚到响应失效; AuthGate 只是 overlay, 登出再登录不会卸载本卡片。
  const [scopedUserId, setScopedUserId] = useState(userId);
  if (scopedUserId !== userId) {
    setScopedUserId(userId);
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
  }

  const load = useCallback(() => {
    if (!WEB) return;
    const seq = ++loadSeq.current;
    setLoading(true);
    setListError(null);
    const tasks = [
      fleetApi.devices().then(
        (r) => {
          if (seq === loadSeq.current) setDevices(r.devices);
        },
        (e: unknown) => {
          if (seq === loadSeq.current) setListError(describeError(e));
        },
      ),
      sharingApi.hostShares().then(
        (r) => {
          if (seq === loadSeq.current) setShares(r.shares);
        },
        (e: unknown) => {
          if (seq === loadSeq.current) setListError(describeError(e));
        },
      ),
      sharingApi.links().then(
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
    if (!user) return;
    load();
  }, [user, load]);

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(timer);
  }, []);

  if (!user) return <ShareLoginHint />;

  const deviceName = (id: string) => devices?.find((d) => d.id === id)?.name ?? shortId(id);
  // 服务端要求分享目标是 daemon 主机 (device_agent 行 + 终端开启), 已吊销设备不可分享。
  const daemonDevices = (devices ?? []).filter((d) => d.revoked_at === 0 && d.agent && d.agent.terminal_enabled);

  const submitCreate = async () => {
    const recipient = recipientUsername.trim();
    if (!deviceId || !recipient) return;
    const epoch = accountEpoch.current;
    setCreating(true);
    setCreateError(null);
    try {
      await sharingApi.createHostShare({ deviceId, recipientUsername: recipient, write, ttlMs });
      if (epoch !== accountEpoch.current) return;
      pushToast("success", `已把主机「${deviceName(deviceId)}」分享给 ${recipient}`);
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
    // epoch 必须在弹出确认前捕获: 确认框打开期间账号切换时, 旧账号的行不得在新会话被吊销。
    const epoch = accountEpoch.current;
    const ok = await ask(
      `吊销这条主机分享?\n\n「${share.recipient_username}」将立即无法通过分享在「${deviceName(share.device_id)}」上新建终端, 进行中的连接会尽快断开。`,
      { title: "吊销主机分享", kind: "warning" },
    );
    if (!ok) return;
    if (epoch !== accountEpoch.current) return;
    try {
      await sharingApi.revokeHostShare(share.id);
      if (epoch !== accountEpoch.current) return;
      pushToast("success", `已吊销「${share.recipient_username}」的主机分享`);
      load();
    } catch (e) {
      if (epoch !== accountEpoch.current) return;
      pushToast("error", describeError(e));
    }
  };

  const revokeLink = async (link: ShareLinkView) => {
    // epoch 必须在弹出确认前捕获 (同 revokeHostShare)。
    const epoch = accountEpoch.current;
    const ok = await ask("吊销这条公开链接?\n\n链接立即失效, 持有链接的人将无法再打开对应会话。", {
      title: "吊销公开链接",
      kind: "warning",
    });
    if (!ok) return;
    if (epoch !== accountEpoch.current) return;
    try {
      await sharingApi.revokeLink(link.id);
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
        {links !== null && <span className="nx-badge">{links.length} 条公开链接</span>}
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
        主机分享把一台守护主机交给另一个注册用户: 对方在有效期内通过主机 agent 新建终端, 全程不接触你的主机密码或私钥。
        公开链接绑定一个实时会话, 有效期内持有链接的人都能打开; 链接只能在这里查看与吊销。
      </p>

      {createOpen && (
        <div className="mb-3 flex flex-col gap-2 border-b border-neutral-800/60 pb-3">
          <div className="flex flex-wrap items-center gap-2">
            <select
              className="nx-select min-w-0 flex-1"
              value={deviceId}
              aria-label="分享主机"
              onChange={(e) => setDeviceId(e.target.value)}
            >
              <option value="">选择主机</option>
              {daemonDevices.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.name}
                </option>
              ))}
            </select>
            <input
              className="nx-input min-w-0 flex-1"
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
            <span className="nx-hint text-[11px]">默认只读; 勾选后对方才可以在终端里输入</span>
            <div className="nx-spacer" />
            <button
              className="nx-btn nx-btn-primary nx-btn-sm"
              disabled={creating || !deviceId || !recipientUsername.trim()}
              onClick={() => void submitCreate()}
            >
              {creating ? "创建中…" : "创建分享"}
            </button>
          </div>
          {devices !== null && daemonDevices.length === 0 && (
            <p className="nx-hint text-[11px]">没有可分享的守护主机: 需要已接入 agent 且远程终端开启的设备 (见「设备管理」)。</p>
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
                ? `授予给 ${s.recipient_username}`
                : s.recipient_id === userId
                  ? `接收自 ${s.owner_username}`
                  : `${s.owner_username} 授予给 ${s.recipient_username}`;
            const canRevoke = isAdmin || s.owner_id === userId || (devices?.some((d) => d.id === s.device_id) ?? false);
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

      <div className="mb-1 mt-3 text-[12.5px] text-neutral-200">公开链接</div>
      {links !== null && links.length > 0 && (
        <div className="max-h-[240px] overflow-y-auto rounded border border-neutral-800/60">
          {links.map((l) => {
            const state = shareState(l.expires_at, l.revoked_at, now);
            const perm = permissionBadge(l.permission);
            const canRevoke = isAdmin || (devices?.some((d) => d.id === l.device_id) ?? false);
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
      {links === null && !listError && <div className="nx-hint py-1.5 text-[11.5px]">公开链接加载中…</div>}
      {links !== null && links.length === 0 && !listError && (
        <div className="nx-hint py-1.5 text-[11.5px]">还没有公开链接。</div>
      )}

      <div className="nx-alert nx-alert-info mt-3 flex items-start gap-2">
        <IconInfo size={14} className="mt-0.5 shrink-0" />
        <div>
          列表由服务端按账号过滤: 普通用户看到自己授予/接收的分享与自建的公开链接, 超管看全部。吊销立即生效;
          分享与链接都不接触主机密码或私钥, 对方通过主机 agent 新建终端。
        </div>
      </div>
    </section>
  );
}
