import { useCallback, useEffect, useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { assetApi, syncApi, type Asset, type AssetGroup } from "../../ipc/commands";
import type { SyncReport, SnippetDto } from "../../ipc/types";
import { AuthApiError, syncV2Api } from "../../ipc/authApi";
import { useAuth } from "../auth/store";
import {
  base64ToBytes,
  bytesToBase64,
  sealSyncObject,
  tryOpenSyncObject,
  utf8Bytes,
  type SyncObjectKind,
} from "../auth/crypto";
import { useUi } from "../../app/store";
import { DEMO, WEB } from "../../demo";
import { describeError } from "../../ui/errorText";
import { AuthCard } from "./AuthCard";
import { AccountCard } from "./AccountCard";
import { SyncReportView } from "./SyncCardReport";
import {
  IconCheckCircle,
  IconDownload,
  IconInfo,
  IconLock,
  IconRefresh,
  IconServer,
  IconUpload,
  IconXCircle,
} from "../../ui/icons";

// Go internal/sync.Link 的线上形状(src/ipc/types.ts 的 SyncLink 是 v1 残留,以这里为准)。
interface AccountLink {
  url: string;
  username: string;
  insecure: boolean;
  hasPassword: boolean;
  verifiedAt: number;
  lastError: string | null;
}

interface SyncStatusView {
  configured: boolean;
  loggedIn: boolean;
  username?: string;
  userId?: string;
  head?: string;
  seq: number;
  verifiedAt: number;
  lastError: string;
}

export function SyncCard() {
  return (
    <>
      <AuthCard />
      <AccountCard />
      <SyncBody />
    </>
  );
}

function SyncBody() {
  if (DEMO) return <DemoSyncConsole />;
  if (WEB) return <WebSyncConsole />;
  return <DesktopLinkCard />;
}

// ---------- 桌面端:账号链接同步 ----------

function DesktopLinkCard() {
  const { pushToast } = useUi();
  const qc = useQueryClient();

  const [link, setLink] = useState<AccountLink | null>(null);
  const [status, setStatus] = useState<SyncStatusView | null>(null);
  const [draft, setDraft] = useState({ url: "", username: "", password: "", insecure: false });
  const [busy, setBusy] = useState<null | "save" | "sync">(null);
  const [report, setReport] = useState<SyncReport | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);

  const load = useCallback(() => {
    setLoadError(null);
    return Promise.all([
      syncApi.linkGet().then((l) => setLink(l as unknown as AccountLink)),
      syncApi.status().then((s) => setStatus(s as unknown as SyncStatusView)),
    ]).catch((e: unknown) => setLoadError(describeError(e)));
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const save = async () => {
    setBusy("save");
    setError(null);
    setReport(null);
    try {
      await syncApi.linkSet({
        url: draft.url.trim(),
        username: draft.username.trim(),
        ...(draft.password ? { password: draft.password } : {}),
        insecure: draft.insecure,
      } as never);
      setDraft((d) => ({ ...d, password: "" }));
      void qc.invalidateQueries({ queryKey: ["sync-link"] });
      await load();
      pushToast("success", "同步链接已保存");
    } catch (e) {
      setError(describeError(e));
    } finally {
      setBusy(null);
    }
  };

  const syncNow = async () => {
    setBusy("sync");
    setError(null);
    setReport(null);
    try {
      const r = (await syncApi.syncNow()) as unknown as SyncReport;
      setReport(r);
      void qc.invalidateQueries();
      await load();
      const moved = r.applied + r.pushed;
      pushToast(moved > 0 ? "success" : "info", moved > 0 ? `同步完成:应用 ${r.applied} · 推送 ${r.pushed}` : "两边已经一致");
    } catch (e) {
      setError(describeError(e));
      await load();
    } finally {
      setBusy(null);
    }
  };

  const connected = !!status?.loggedIn && !status.lastError;

  return (
    <section className="nx-card">
      <div className="mb-1 flex items-center gap-2">
        <IconServer size={15} className="text-neutral-400" />
        <span className="nx-card-title">账号同步</span>
        {connected ? (
          <span className="nx-badge nx-badge-green">已登录 {status?.username}</span>
        ) : status?.configured ? (
          <span className="nx-badge nx-badge-amber">待同步</span>
        ) : (
          <span className="nx-badge">未配置</span>
        )}
      </div>

      <p className="nx-hint mb-3.5">
        把这台桌面设备的资产、分组、片段加密同步到你的账号。数据在你的设备上加密,
        服务端只存密文;不登录账号也完全可以继续本地使用。
      </p>

      {loadError && (
        <div className="nx-alert nx-alert-danger mb-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">同步配置读取失败 · {loadError}</span>
          <button className="nx-btn nx-btn-ghost nx-btn-sm shrink-0" onClick={() => void load()}>
            <IconRefresh size={12} />
            重试
          </button>
        </div>
      )}

      <div className="flex flex-col gap-2">
        <div className="flex items-center gap-2">
          <label className="w-[76px] shrink-0 text-[12px] text-neutral-400" htmlFor="sync-url">
            服务端
          </label>
          <input
            id="sync-url"
            className="nx-input min-w-0 flex-1 font-mono"
            placeholder="https://nexterm.example.com"
            value={draft.url}
            autoComplete="url"
            onChange={(e) => setDraft((d) => ({ ...d, url: e.target.value }))}
          />
        </div>
        <div className="flex items-center gap-2">
          <label className="w-[76px] shrink-0 text-[12px] text-neutral-400" htmlFor="sync-user">
            账号
          </label>
          <input
            id="sync-user"
            className="nx-input min-w-0 flex-1"
            placeholder="用户名"
            value={draft.username}
            autoComplete="username"
            onChange={(e) => setDraft((d) => ({ ...d, username: e.target.value }))}
          />
        </div>
        <div className="flex items-center gap-2">
          <label className="w-[76px] shrink-0 text-[12px] text-neutral-400" htmlFor="sync-pass">
            密码
          </label>
          <input
            id="sync-pass"
            type="password"
            className="nx-input min-w-0 flex-1"
            placeholder={link?.hasPassword ? "留空 = 不修改已保存的密码" : "账号密码(用于解锁数据密钥)"}
            value={draft.password}
            autoComplete="new-password"
            onChange={(e) => setDraft((d) => ({ ...d, password: e.target.value }))}
          />
        </div>
        <label className="flex items-start gap-2">
          <input
            type="checkbox"
            className="mt-0.5 h-4 w-4 shrink-0"
            checked={draft.insecure}
            onChange={(e) => setDraft((d) => ({ ...d, insecure: e.target.checked }))}
          />
          <span className="text-[12px] text-neutral-300">
            跳过证书校验
            <span className="nx-hint block">只有自签证书的内网/自建地址才需要勾。</span>
          </span>
        </label>
      </div>

      <div className="mt-3 flex flex-wrap items-center gap-2">
        <button
          className="nx-btn nx-btn-primary nx-btn-sm"
          disabled={busy !== null || !draft.url.trim() || !draft.username.trim() || (!draft.password && !link?.hasPassword)}
          onClick={() => void save()}
        >
          {busy === "save" ? <IconRefresh size={12} className="animate-spin" /> : <IconCheckCircle size={12} />}
          {busy === "save" ? "保存中…" : "保存链接"}
        </button>
        <button
          className="nx-btn nx-btn-outline nx-btn-sm"
          disabled={busy !== null || !status?.configured}
          onClick={() => void syncNow()}
        >
          {busy === "sync" ? <IconRefresh size={12} className="animate-spin" /> : <IconUpload size={12} />}
          {busy === "sync" ? "同步中…" : "立即同步"}
        </button>
        {status?.lastError && <span className="nx-hint text-amber-300">上次失败:{status.lastError}</span>}
      </div>

      {error && (
        <div className="nx-alert nx-alert-danger mt-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">{error}</span>
        </div>
      )}

      {report && <SyncReportView data={report} />}

      <div className="nx-alert nx-alert-info mt-3 flex items-start gap-2">
        <IconInfo size={14} className="mt-0.5 shrink-0" />
        <div>
          会话记录(终端录像)默认不同步;要同步某一条,到「会话记录」里对那条单独打开同步开关。
          同步是端到端加密的,服务端看不到资产名、主机、用户名和内容。
        </div>
      </div>
    </section>
  );
}

// ---------- Web 端:账号同步台 ----------

interface LocalEntity {
  id: string;
  kind: SyncObjectKind;
  name: string;
  updatedAt: number;
  deletedAt: number | null;
  payload: unknown;
}

interface RemoteObject {
  id: string;
  kind: SyncObjectKind;
  name: string;
  updatedAt: number;
  deletedAt: number | null;
  seq: number;
}

function groupPayload(g: AssetGroup): unknown {
  const o: Record<string, unknown> = { id: g.id };
  if (g.parentId !== null) o.parentId = g.parentId;
  o.name = g.name;
  o.sort = g.sort;
  o.createdAt = g.createdAt;
  o.updatedAt = g.updatedAt;
  return o;
}

function assetPayload(a: Asset): unknown {
  const o: Record<string, unknown> = { id: a.id };
  if (a.groupId !== null) o.groupId = a.groupId;
  o.kind = a.kind;
  o.name = a.name;
  if (a.host !== null) o.host = a.host;
  if (a.port !== null) o.port = a.port;
  if (a.username !== null) o.username = a.username;
  if (a.authKind !== null) o.authKind = a.authKind;
  if (a.keyPath !== null) o.keyPath = a.keyPath;
  if (a.credId !== null) o.credId = a.credId;
  o.optionsJson = JSON.stringify(a.options ?? {});
  o.tags = a.tags;
  o.note = a.note;
  o.sort = a.sort;
  o.createdAt = a.createdAt;
  o.updatedAt = a.updatedAt;
  if (a.deletedAt !== null) o.deletedAt = a.deletedAt;
  return o;
}

function snippetPayload(s: SnippetDto): unknown {
  const o: Record<string, unknown> = { id: s.id };
  if (s.groupId !== null) o.groupId = s.groupId;
  o.name = s.name;
  o.body = s.body;
  o.sort = s.sort;
  o.createdAt = s.createdAt;
  o.updatedAt = s.updatedAt;
  return o;
}

async function loadLocalEntities(): Promise<LocalEntity[]> {
  const [groups, assets, snippets] = await Promise.all([
    assetApi.groupList(),
    assetApi.list(),
    assetApi.snippetList(),
  ]);
  const out: LocalEntity[] = [];
  for (const g of groups) {
    out.push({ id: g.id, kind: "group", name: g.name, updatedAt: g.updatedAt, deletedAt: null, payload: groupPayload(g) });
  }
  for (const a of assets) {
    if (a.builtin) continue;
    out.push({ id: a.id, kind: "asset", name: a.name, updatedAt: a.updatedAt, deletedAt: a.deletedAt, payload: assetPayload(a) });
  }
  for (const s of snippets) {
    out.push({ id: s.id, kind: "snippet", name: s.name, updatedAt: s.updatedAt, deletedAt: null, payload: snippetPayload(s) });
  }
  return out;
}

function revisionOf(updatedAt: number, deletedAt: number | null): number {
  return deletedAt !== null && deletedAt > updatedAt ? deletedAt : updatedAt;
}

async function loadRemoteObjects(dek: Uint8Array): Promise<RemoteObject[]> {
  const ids = await syncV2Api.ids();
  const out: RemoteObject[] = [];
  for (const entry of ids.entries) {
    const page = await syncV2Api.pull(0, [entry.id]);
    const wire = page.objects[0];
    if (!wire) continue;
    const { kind, plaintext } = await tryOpenSyncObject(dek, base64ToBytes(wire.blob), entry.id);
    const text = new TextDecoder().decode(plaintext);
    if (kind === "tombstone") {
      const t = JSON.parse(text) as { targetKind?: string; deletedAt?: number };
      out.push({ id: entry.id, kind: "tombstone", name: entry.id, updatedAt: t.deletedAt ?? 0, deletedAt: t.deletedAt ?? 0, seq: entry.seq });
      continue;
    }
    const p = JSON.parse(text) as { name?: string; updatedAt?: number; deletedAt?: number };
    out.push({
      id: entry.id,
      kind,
      name: p.name ?? entry.id,
      updatedAt: p.updatedAt ?? 0,
      deletedAt: p.deletedAt ?? null,
      seq: entry.seq,
    });
  }
  return out;
}

function cursorKey(userId: string): string {
  return `sync.cursor.${userId}`;
}

function loadCursor(userId: string): { head: string; seq: number } {
  try {
    const raw = window.localStorage.getItem(cursorKey(userId));
    if (raw) {
      const parsed = JSON.parse(raw) as { head?: string; seq?: number };
      return { head: parsed.head ?? "", seq: parsed.seq ?? 0 };
    }
  } catch {
    // 忽略损坏的游标
  }
  return { head: "", seq: 0 };
}

function saveCursor(userId: string, cursor: { head: string; seq: number }): void {
  try {
    window.localStorage.setItem(cursorKey(userId), JSON.stringify(cursor));
  } catch {
    // 忽略存储失败
  }
}

type RowState = "local-only" | "remote-only" | "same" | "local-newer" | "remote-newer" | "local-deleted" | "remote-deleted";

interface Row {
  id: string;
  name: string;
  kind: SyncObjectKind;
  state: RowState;
  local?: LocalEntity;
  remote?: RemoteObject;
}

function buildRows(local: LocalEntity[], remote: RemoteObject[]): Row[] {
  const remoteById = new Map(remote.map((r) => [r.id, r]));
  const rows: Row[] = [];
  for (const l of local) {
    const r = remoteById.get(l.id);
    if (!r) {
      rows.push({ id: l.id, name: l.name, kind: l.kind, state: l.deletedAt !== null ? "local-deleted" : "local-only", local: l });
      continue;
    }
    remoteById.delete(l.id);
    const lr = revisionOf(l.updatedAt, l.deletedAt);
    const rr = revisionOf(r.updatedAt, r.deletedAt);
    let state: RowState;
    if (r.kind === "tombstone" || r.deletedAt !== null) {
      state = lr > rr ? "local-newer" : "remote-deleted";
    } else if (l.deletedAt !== null) {
      state = lr >= rr ? "local-deleted" : "remote-newer";
    } else if (lr === rr) {
      state = "same";
    } else {
      state = lr > rr ? "local-newer" : "remote-newer";
    }
    rows.push({ id: l.id, name: l.name, kind: l.kind, state, local: l, remote: r });
  }
  for (const r of remoteById.values()) {
    rows.push({ id: r.id, name: r.name, kind: r.kind, state: r.kind === "tombstone" || r.deletedAt !== null ? "remote-deleted" : "remote-only", remote: r });
  }
  const rank = (s: RowState) => (s === "same" ? 1 : 0);
  return rows.sort((a, b) => rank(a.state) - rank(b.state) || a.name.localeCompare(b.name));
}

function rowLabel(r: Row): { text: string; tone: string; hint: string } {
  switch (r.state) {
    case "local-only":
      return { text: "仅本机", tone: "", hint: "云端还没有这条。推送会新建。" };
    case "remote-only":
      return { text: "仅云端", tone: "", hint: "本机还没有这条。其他设备推送的。" };
    case "same":
      return { text: "已一致", tone: "nx-badge-green", hint: "两边修订号相同。" };
    case "local-newer":
      return { text: "本机较新", tone: "nx-badge-amber", hint: "本机这份修订号更新,推送会覆盖云端。" };
    case "remote-newer":
      return { text: "云端较新", tone: "nx-badge-amber", hint: "云端这份修订号更新。" };
    case "local-deleted":
      return { text: "本机已删", tone: "nx-badge-amber", hint: "这条在本机已删除。" };
    case "remote-deleted":
      return { text: "云端已删", tone: "nx-badge-amber", hint: "这条在云端已删除(墓碑)。" };
  }
}

const KIND_LABELS: Record<string, string> = {
  group: "分组",
  asset: "资产",
  snippet: "片段",
  tombstone: "墓碑",
};

function WebSyncConsole() {
  const user = useAuth((s) => s.user);
  const dek = useAuth((s) => s.dek);

  if (!user) {
    return (
      <section className="nx-card">
        <div className="mb-1 flex items-center gap-2">
          <IconServer size={15} className="text-neutral-400" />
          <span className="nx-card-title">账号同步</span>
        </div>
        <p className="nx-hint mb-3">
          登录后,这台服务器上的资产、分组、片段会端到端加密同步到你的账号,在其他设备上可用。
          不登录也能继续本地使用。
        </p>
        <button className="nx-btn nx-btn-primary nx-btn-sm" onClick={() => useAuth.setState({ gate: "login" })}>
          <IconLock size={12} />
          登录以启用同步
        </button>
      </section>
    );
  }
  if (!dek) return <UnlockDEKCard />;
  return <CompareConsole />;
}

function UnlockDEKCard() {
  const unlockDEK = useAuth((s) => s.unlockDEK);
  const error = useAuth((s) => s.error);
  const clearError = useAuth((s) => s.clearError);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  const submit = async () => {
    setFormError(null);
    clearError();
    setBusy(true);
    try {
      await unlockDEK(password);
      setPassword("");
    } catch (e) {
      setFormError(describeError(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="nx-card">
      <div className="mb-1 flex items-center gap-2">
        <IconLock size={15} className="text-neutral-400" />
        <span className="nx-card-title">解锁数据密钥</span>
      </div>
      <p className="nx-hint mb-3">
        同步内容用你的账号密码加密。输入密码在本机解锁数据密钥(约需几秒,密钥不会离开本机)。
      </p>
      <div className="flex flex-wrap items-center gap-2">
        <input
          className="nx-input min-w-0 flex-1 max-w-[280px]"
          type="password"
          placeholder="账号密码"
          value={password}
          autoComplete="current-password"
          onChange={(e) => setPassword(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && password) void submit();
          }}
        />
        <button className="nx-btn nx-btn-primary nx-btn-sm" disabled={busy || !password} onClick={() => void submit()}>
          {busy ? "解锁中…" : "解锁"}
        </button>
      </div>
      {formError && (
        <div className="nx-alert nx-alert-danger mt-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span>{formError}</span>
        </div>
      )}
      {!formError && error && (
        <div className="nx-alert nx-alert-danger mt-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span>{describeError(error)}</span>
        </div>
      )}
    </section>
  );
}

function CompareConsole() {
  const { pushToast } = useUi();
  const user = useAuth((s) => s.user)!;
  const dek = useAuth((s) => s.dek)!;

  const [local, setLocal] = useState<LocalEntity[] | null>(null);
  const [remote, setRemote] = useState<RemoteObject[] | null>(null);
  const [cursor, setCursor] = useState<{ head: string; seq: number }>(() => loadCursor(user.id));
  const [busy, setBusy] = useState<null | "refresh" | "push">(null);
  const [error, setError] = useState<string | null>(null);
  const [pushInfo, setPushInfo] = useState<string | null>(null);

  const rows = useMemo(() => (local && remote ? buildRows(local, remote) : null), [local, remote]);

  const load = useCallback(() => {
    setError(null);
    return Promise.all([loadLocalEntities(), loadRemoteObjects(dek)])
      .then(([l, r]) => {
        setLocal(l);
        setRemote(r);
      })
      .catch((e: unknown) => setError(describeError(e)));
  }, [dek]);

  useEffect(() => {
    void load();
  }, [load]);

  const pushable = useMemo(() => (local ?? []).filter((e) => e.deletedAt === null), [local]);

  const push = async () => {
    setBusy("push");
    setError(null);
    setPushInfo(null);
    try {
      const rank = (k: SyncObjectKind) => (k === "group" ? 0 : k === "snippet" ? 1 : 2);
      const ordered = [...pushable].sort((a, b) => rank(a.kind) - rank(b.kind) || a.id.localeCompare(b.id));
      const objects: { id: string; blob: string }[] = [];
      for (const e of ordered) {
        const blob = await sealSyncObject(dek, utf8Bytes(JSON.stringify(e.payload)), e.id, e.kind);
        objects.push({ id: e.id, blob: bytesToBase64(blob) });
      }
      let head = cursor.head;
      let resp = await syncV2Api.push(head, objects);
      let retried = false;
      while (resp.applied === 0 && resp.skipped === 0 && objects.length > 0 && !retried) {
        // 空推且对象非空:可能是 head 分叉被服务端跳过,刷新游标后重试一次
        const ids = await syncV2Api.ids();
        if (ids.head === head) break;
        head = ids.head;
        resp = await syncV2Api.push(head, objects);
        retried = true;
      }
      const next = { head: resp.head, seq: resp.max_seq };
      saveCursor(user.id, next);
      setCursor(next);
      setPushInfo(`已推送 ${resp.applied} 个对象(跳过 ${resp.skipped})`);
      pushToast("success", `已推送 ${resp.applied} 个对象`);
      await load();
    } catch (e) {
      if (e instanceof AuthApiError && e.status === 409) {
        setError("云端已被其他设备更新,请刷新后再推送");
      } else {
        setError(describeError(e));
      }
    } finally {
      setBusy(null);
    }
  };

  const pushCount = pushable.filter((e) => {
    const r = remote?.find((x) => x.id === e.id);
    if (!r) return true;
    return revisionOf(e.updatedAt, e.deletedAt) > revisionOf(r.updatedAt, r.deletedAt);
  }).length;

  return (
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <IconServer size={15} className="text-neutral-400" />
        <span className="nx-card-title">账号同步</span>
        <span className="nx-badge nx-badge-green">已解锁</span>
        <span className="nx-hint">
          游标 seq {cursor.seq || "—"} · 云端 {remote?.length ?? "…"} 个对象
        </span>
        <div className="nx-spacer" />
        <button className="nx-btn nx-btn-ghost nx-btn-sm" disabled={busy !== null} onClick={() => void load()}>
          <IconRefresh size={12} className={busy === "refresh" ? "animate-spin" : ""} />
          刷新对比
        </button>
        <button
          className="nx-btn nx-btn-primary nx-btn-sm"
          disabled={busy !== null || pushable.length === 0}
          onClick={() => void push()}
        >
          {busy === "push" ? <IconRefresh size={12} className="animate-spin" /> : <IconUpload size={12} />}
          {busy === "push" ? "推送中…" : `推送到云端 (${pushCount})`}
        </button>
      </div>

      <p className="nx-hint mb-3">
        本机数据与云端密文副本的对比(在本机解密后比较,服务端看不到内容)。
        推送会把本机较新的内容加密送上云端,其他设备随后拉取合并;
        「云端较新」的行暂不能在本机应用,等服务端应用通道就绪后可拉回。
      </p>

      {error && (
        <div className="nx-alert nx-alert-danger mb-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">{error}</span>
          <button className="nx-btn nx-btn-ghost nx-btn-sm shrink-0" onClick={() => void load()}>
            <IconRefresh size={12} />
            重试
          </button>
        </div>
      )}

      {pushInfo && (
        <div className="nx-alert nx-alert-info mb-3 flex items-start gap-2">
          <IconCheckCircle size={13} className="mt-0.5 shrink-0" />
          <span>{pushInfo}</span>
        </div>
      )}

      {rows && (
        <div className="max-h-[320px] overflow-y-auto rounded border border-neutral-800/60">
          {rows.length === 0 && <div className="nx-hint px-2.5 py-3 text-[12px]">本机与云端都还没有可同步的内容。</div>}
          {rows.map((r) => {
            const label = rowLabel(r);
            return (
              <div
                key={r.id}
                className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-neutral-800/40 px-2.5 py-1.5 last:border-b-0"
                title={label.hint}
              >
                <span className="min-w-0 flex-1 truncate text-[12.5px] text-neutral-200" title={r.name}>
                  {r.name}
                </span>
                <span className="nx-hint shrink-0 text-[11px]">{KIND_LABELS[r.kind] ?? r.kind}</span>
                <span className={`nx-badge shrink-0 ${label.tone}`}>{label.text}</span>
              </div>
            );
          })}
        </div>
      )}

      {!rows && !error && <div className="nx-hint py-3 text-[12px]">正在读本机与云端数据…</div>}

      <div className="nx-alert nx-alert-info mt-3 flex items-start gap-2">
        <IconInfo size={14} className="mt-0.5 shrink-0" />
        <div>
          会话记录(终端录像)默认不同步;要同步某一条,到「会话记录」里对那条单独打开同步开关。
          冲突按修订号(最后修改时间)裁决,协议假设各设备时钟已经 <b>NTP 同步</b>,
          不检测也不校正时钟偏移;时钟不准时「较新」判定可能不符合预期。
        </div>
      </div>
    </section>
  );
}

// ---------- DEMO ----------

function DemoSyncConsole() {
  return (
    <section className="nx-card">
      <div className="mb-1 flex items-center gap-2">
        <IconServer size={15} className="text-neutral-400" />
        <span className="nx-card-title">账号同步</span>
        <span className="nx-badge nx-badge-amber">演示</span>
      </div>
      <p className="nx-hint mb-3">
        演示模式下同步数据是假的。真实环境里,登录后这里会显示本机与云端密文副本的对比,
        并能一键推送;数据在你的设备上加密,服务端只存密文。
      </p>
      <div className="max-h-[200px] overflow-y-auto rounded border border-neutral-800/60">
        {[
          { name: "web-01", kind: "资产", state: "已一致", tone: "nx-badge-green" },
          { name: "db-02", kind: "资产", state: "本机较新", tone: "nx-badge-amber" },
          { name: "生产环境", kind: "分组", state: "已一致", tone: "nx-badge-green" },
          { name: "常用命令", kind: "片段", state: "仅本机", tone: "" },
          { name: "old-laptop", kind: "资产", state: "云端已删", tone: "nx-badge-amber" },
        ].map((r) => (
          <div
            key={r.name}
            className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-neutral-800/40 px-2.5 py-1.5 last:border-b-0"
          >
            <span className="min-w-0 flex-1 truncate text-[12.5px] text-neutral-200">{r.name}</span>
            <span className="nx-hint shrink-0 text-[11px]">{r.kind}</span>
            <span className={`nx-badge shrink-0 ${r.tone}`}>{r.state}</span>
          </div>
        ))}
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <button className="nx-btn nx-btn-primary nx-btn-sm" disabled>
          <IconUpload size={12} />
          推送到云端 (2)
        </button>
        <button className="nx-btn nx-btn-outline nx-btn-sm" disabled>
          <IconDownload size={12} />
          从云端拉取
        </button>
      </div>
    </section>
  );
}
