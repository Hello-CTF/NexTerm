import { useCallback, useEffect, useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { assetApi, syncApi, transcriptApi, type Asset, type AssetGroup, type TranscriptChunk, type TranscriptSummary } from "../../ipc/commands";
import type { SyncReport, SnippetDto } from "../../ipc/types";
import { AuthApiError, syncV2Api } from "../../ipc/authApi";
import { useAuth } from "../auth/store";
import {
  base64ToBytes,
  bytesToBase64,
  objectPayloadHash,
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
  const [draftEdited, setDraftEdited] = useState(false);
  const [busy, setBusy] = useState<null | "save" | "sync">(null);
  const [report, setReport] = useState<SyncReport | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);

  const updateDraft = (patch: Partial<{ url: string; username: string; password: string; insecure: boolean }>) => {
    setDraftEdited(true);
    setDraft((d) => ({ ...d, ...patch }));
  };

  const load = useCallback(() => {
    setLoadError(null);
    return Promise.all([
      syncApi.linkGet().then((l) => {
        const next = l as unknown as AccountLink;
        setLink(next);
        // 初次加载用已保存链接回填 URL/用户名/insecure;密码保持空白;不覆盖未保存编辑
        setDraft((d) => (draftEdited ? d : { ...d, url: next.url, username: next.username, insecure: next.insecure }));
      }),
      syncApi.status().then((s) => setStatus(s as unknown as SyncStatusView)),
    ]).catch((e: unknown) => setLoadError(describeError(e)));
  }, [draftEdited]);

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
            onChange={(e) => updateDraft({ url: e.target.value })}
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
            onChange={(e) => updateDraft({ username: e.target.value })}
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
            onChange={(e) => updateDraft({ password: e.target.value })}
          />
        </div>
        <label className="flex items-start gap-2">
          <input
            type="checkbox"
            className="mt-0.5 h-4 w-4 shrink-0"
            checked={draft.insecure}
            onChange={(e) => updateDraft({ insecure: e.target.checked })}
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
  payloadHash: string;
}

interface RemoteObject {
  id: string;
  kind: SyncObjectKind;
  name: string;
  updatedAt: number;
  deletedAt: number | null;
  seq: number;
  payloadHash: string;
  plaintext: string;
}

interface RemoteState {
  objects: RemoteObject[];
  head: string;
  maxSeq: number;
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

// transcriptObject 与 internal/sync/object.go 的 transcriptObject 对齐。
function transcriptPayload(t: TranscriptSummary, content: TranscriptChunk[] | null): unknown {
  const o: Record<string, unknown> = {
    id: t.id,
    assetId: t.assetId,
    assetName: t.assetName,
    assetKind: t.assetKind,
    startedAt: t.startedAt,
    endedAt: t.endedAt ?? 0,
    bytes: t.bytes,
    chunks: t.chunks,
    truncated: t.truncated,
  };
  if (t.sessionId) o.sessionId = t.sessionId;
  if (t.contentOmitted || content === null) {
    o.contentOmitted = true;
    return o;
  }
  o.content = content.map((c) => ({ seq: c.seq, tabId: c.tabId, ts: c.ts, data: c.dataBase64 }));
  return o;
}

const TRANSCRIPT_MAX_CONTENT_BYTES = 64 << 20;

// readTranscriptAll 按 done/nextSeq 分页读完全部内容;读取失败显式抛出,不吞成空数组。
async function readTranscriptAll(id: string): Promise<TranscriptChunk[]> {
  const chunks: TranscriptChunk[] = [];
  let afterSeq = 0;
  for (let page = 0; page < 10000; page++) {
    const read = await transcriptApi.read(id, afterSeq);
    chunks.push(...read.chunks);
    if (read.done) return chunks;
    if (read.nextSeq <= afterSeq) {
      throw new Error(`会话记录 ${id} 分页游标未推进(done=false 且 nextSeq 未前进)`);
    }
    afterSeq = read.nextSeq;
  }
  throw new Error(`会话记录 ${id} 分页读取超出上限`);
}

async function loadOptedInTranscripts(): Promise<LocalEntity[]> {
  const hosts = await transcriptApi.hosts();
  const out: LocalEntity[] = [];
  for (const host of hosts) {
    const summaries = await transcriptApi.list(host.assetId);
    for (const t of summaries) {
      if (!t.syncOptIn || t.active || t.endedAt === null) continue;
      let content: TranscriptChunk[] | null = null;
      // contentOmitted 只按真实超限语义使用(>64MiB 仅元数据);未超限必须分页读全,不得把多页内容标成完整。
      if (!t.contentOmitted && t.bytes > TRANSCRIPT_MAX_CONTENT_BYTES) {
        content = null;
      } else if (!t.contentOmitted) {
        content = await readTranscriptAll(t.id);
      }
      const payload = transcriptPayload(t, content);
      out.push({
        id: t.id,
        kind: "transcript",
        name: `${t.assetName} 的会话记录`,
        updatedAt: t.endedAt ?? t.startedAt,
        deletedAt: null,
        payload,
        payloadHash: await objectPayloadHash(utf8Bytes(JSON.stringify(payload))),
      });
    }
  }
  return out;
}

async function loadLocalEntities(): Promise<LocalEntity[]> {
  const [groups, assets, snippets, transcripts] = await Promise.all([
    assetApi.groupList(),
    assetApi.list(),
    assetApi.snippetList(),
    loadOptedInTranscripts(),
  ]);
  const out: LocalEntity[] = [];
  for (const g of groups) {
    const payload = groupPayload(g);
    out.push({ id: g.id, kind: "group", name: g.name, updatedAt: g.updatedAt, deletedAt: null, payload, payloadHash: await objectPayloadHash(utf8Bytes(JSON.stringify(payload))) });
  }
  for (const a of assets) {
    if (a.builtin) continue;
    const payload = assetPayload(a);
    out.push({ id: a.id, kind: "asset", name: a.name, updatedAt: a.updatedAt, deletedAt: a.deletedAt, payload, payloadHash: await objectPayloadHash(utf8Bytes(JSON.stringify(payload))) });
  }
  for (const s of snippets) {
    const payload = snippetPayload(s);
    out.push({ id: s.id, kind: "snippet", name: s.name, updatedAt: s.updatedAt, deletedAt: null, payload, payloadHash: await objectPayloadHash(utf8Bytes(JSON.stringify(payload))) });
  }
  out.push(...transcripts);
  return out;
}

function revisionOf(updatedAt: number, deletedAt: number | null): number {
  return deletedAt !== null && deletedAt > updatedAt ? deletedAt : updatedAt;
}

async function loadRemoteObjects(dek: Uint8Array): Promise<RemoteState> {
  const ids = await syncV2Api.ids();
  const out: RemoteObject[] = [];
  for (const entry of ids.entries) {
    const page = await syncV2Api.pull(0, [entry.id]);
    const wire = page.objects[0];
    if (!wire) continue;
    const { kind, plaintext } = await tryOpenSyncObject(dek, base64ToBytes(wire.blob), entry.id);
    const payloadHash = await objectPayloadHash(plaintext);
    const text = new TextDecoder().decode(plaintext);
    if (kind === "tombstone") {
      const t = JSON.parse(text) as { targetKind?: string; deletedAt?: number };
      out.push({ id: entry.id, kind: "tombstone", name: entry.id, updatedAt: t.deletedAt ?? 0, deletedAt: t.deletedAt ?? 0, seq: entry.seq, payloadHash, plaintext: text });
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
      payloadHash,
      plaintext: text,
    });
  }
  return { objects: out, head: ids.head, maxSeq: ids.max_seq };
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

// computeWinners 返回需要推送的本机对象集合:仅本机独占或本地胜出(含平修订号按载荷 hash 决胜)。
function computeWinners(local: LocalEntity[], remote: RemoteObject[]): LocalEntity[] {
  const remoteById = new Map(remote.map((r) => [r.id, r]));
  return local.filter((e) => {
    const r = remoteById.get(e.id);
    return r === undefined || localWins(e, r);
  });
}

// sealAll 按 kind 依赖序把 winner 对象加密成线上形态(与 objectKindRank 对齐)。
async function sealAll(dek: Uint8Array, list: LocalEntity[]): Promise<{ id: string; blob: string }[]> {
  const rank = (k: SyncObjectKind) => {
    switch (k) {
      case "group": return 0;
      case "credential": return 1;
      case "snippet": return 2;
      case "asset": return 3;
      case "tombstone": return 4;
      case "transcript": return 5;
      default: return 6;
    }
  };
  const ordered = [...list].sort((a, b) => rank(a.kind) - rank(b.kind) || a.id.localeCompare(b.id));
  const objects: { id: string; blob: string }[] = [];
  for (const e of ordered) {
    const blob = await sealSyncObject(dek, utf8Bytes(JSON.stringify(e.payload)), e.id, e.kind);
    objects.push({ id: e.id, blob: bytesToBase64(blob) });
  }
  return objects;
}

// localWins 复刻 M117 merge.go 的 LWW 裁决:修订号大者胜,平手按载荷 sha256 字典序决胜。
// 载荷完全一致(同 hash)时本地无需推送(覆盖相同内容)。
function localWins(l: LocalEntity, r: RemoteObject): boolean {
  if (l.payloadHash === r.payloadHash) return false;
  const lr = revisionOf(l.updatedAt, l.deletedAt);
  const rr = revisionOf(r.updatedAt, r.deletedAt);
  if (lr !== rr) return lr > rr;
  return l.payloadHash > r.payloadHash;
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
    const wins = localWins(l, r);
    let state: RowState;
    if (l.payloadHash === r.payloadHash) {
      state = "same";
    } else if (r.kind === "tombstone" || r.deletedAt !== null) {
      state = wins ? "local-newer" : "remote-deleted";
    } else if (l.deletedAt !== null) {
      state = wins ? "local-deleted" : "remote-newer";
    } else {
      state = wins ? "local-newer" : "remote-newer";
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
        setRemote(r.objects);
        // 游标以服务端最新 head/seq 为准(空 genesis 不得强制空字符串)
        const next = { head: r.head, seq: r.maxSeq };
        saveCursor(user.id, next);
        setCursor(next);
      })
      .catch((e: unknown) => setError(describeError(e)));
  }, [dek, user.id]);

  useEffect(() => {
    void load();
  }, [load]);

  // winner 集合:仅本机独占或本地胜出(含平修订号按载荷 hash 决胜);计数与上传共用同一集合。
  const winners = useMemo(() => {
    if (!local || !remote) return [];
    return computeWinners(local, remote);
  }, [local, remote]);

  // applySet 集合:远端胜出(仅云端/云端较新/云端已删)需要拉取应用的对象。
  const applySet = useMemo(() => {
    if (!local || !remote) return [];
    return remote.filter((r) => {
      const l = local.find((e) => e.id === r.id);
      if (!l) return true; // remote-only
      return !localWins(l, r); // remote-newer 或 remote-deleted
    });
  }, [local, remote]);

  const [applyInfo, setApplyInfo] = useState<string | null>(null);

  const applyRemote = async () => {
    setBusy("push");
    setError(null);
    setApplyInfo(null);
    try {
      const objects = applySet.map((r) => ({
        id: r.id,
        kind: r.kind,
        payload: JSON.parse(r.plaintext) as import("../../ipc/types").JsonValue,
      }));
      // 分批(单次上限 256)
      let applied = 0;
      let identical = 0;
      let skipped = 0;
      const warnings: string[] = [];
      for (let i = 0; i < objects.length; i += 256) {
        const result = await syncApi.applyObjects(objects.slice(i, i + 256));
        applied += result.applied;
        identical += result.identical;
        skipped += result.skipped;
        for (const o of result.objects) {
          if (o.warning) warnings.push(`${o.id}: ${o.warning}`);
        }
      }
      // 警告(如凭据库未解锁)并入结果展示,不随 load() 清除
      setApplyInfo(
        `已应用 ${applied} · 一致 ${identical} · 跳过 ${skipped}` +
          (warnings.length > 0 ? ` · 警告:${warnings.join("; ")}` : ""),
      );
      pushToast("success", `已应用 ${applied} 个对象`);
      await load();
    } catch (e) {
      setError(describeError(e));
    } finally {
      setBusy(null);
    }
  };

  const push = async () => {
    setBusy("push");
    setError(null);
    setPushInfo(null);
    try {
      // 对比基线(加载时的 head)。若服务端 head 已变,先重新拉取并以同一快照重算 winner,不沿用旧 winner。
      let head = cursor.head;
      let winnersNow = winners;
      const fresh = await syncV2Api.ids();
      if (fresh.head !== head) {
        const remoteState = await loadRemoteObjects(dek);
        setRemote(remoteState.objects);
        winnersNow = computeWinners(local ?? [], remoteState.objects);
        const base = { head: remoteState.head, seq: remoteState.maxSeq };
        saveCursor(user.id, base);
        setCursor(base);
        head = remoteState.head;
      }

      let resp = await syncV2Api.push(head, await sealAll(dek, winnersNow));

      // 空推(applied+skipped=0)或真 409(他端已更新):重新拉取并以同一快照重算 winner,再重试一次,不得只换 known_head。
      const reconcileAndRetry = async (): Promise<typeof resp | null> => {
        const remoteState = await loadRemoteObjects(dek);
        const recomputed = computeWinners(local ?? [], remoteState.objects);
        if (remoteState.head === head && recomputed.length === winnersNow.length && !recomputed.some((e, i) => e.id !== winnersNow[i]?.id)) {
          return null;
        }
        setRemote(remoteState.objects);
        winnersNow = recomputed;
        const base = { head: remoteState.head, seq: remoteState.maxSeq };
        saveCursor(user.id, base);
        setCursor(base);
        return syncV2Api.push(remoteState.head, await sealAll(dek, recomputed));
      };

      if (resp.applied === 0 && resp.skipped === 0 && winnersNow.length > 0) {
        const retried = await reconcileAndRetry();
        if (retried) resp = retried;
      }

      const next = { head: resp.head, seq: resp.max_seq };
      saveCursor(user.id, next);
      setCursor(next);
      setPushInfo(`已推送 ${resp.applied} 个对象(跳过 ${resp.skipped})`);
      pushToast("success", `已推送 ${resp.applied} 个对象`);
      await load();
    } catch (e) {
      if (e instanceof AuthApiError && e.status === 409) {
        // 真 409:重新对账并以新 head 重试一次;仍失败才报错。
        try {
          const remoteState = await loadRemoteObjects(dek);
          const recomputed = computeWinners(local ?? [], remoteState.objects);
          setRemote(remoteState.objects);
          const base = { head: remoteState.head, seq: remoteState.maxSeq };
          saveCursor(user.id, base);
          setCursor(base);
          const sealed = await sealAll(dek, recomputed);
          const resp = await syncV2Api.push(remoteState.head, sealed);
          const next = { head: resp.head, seq: resp.max_seq };
          saveCursor(user.id, next);
          setCursor(next);
          setPushInfo(`已推送 ${resp.applied} 个对象(跳过 ${resp.skipped})`);
          pushToast("success", `已推送 ${resp.applied} 个对象`);
          await load();
          return;
        } catch (retryError) {
          setError(describeError(retryError));
          return;
        }
      }
      setError(describeError(e));
    } finally {
      setBusy(null);
    }
  };

  const pushCount = winners.length;

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
          className="nx-btn nx-btn-outline nx-btn-sm"
          disabled={busy !== null || applySet.length === 0}
          onClick={() => void applyRemote()}
        >
          {busy === "push" ? <IconRefresh size={12} className="animate-spin" /> : <IconDownload size={12} />}
          {busy === "push" ? "应用中…" : `拉取并应用 (${applySet.length})`}
        </button>
        <button
          className="nx-btn nx-btn-primary nx-btn-sm"
          disabled={busy !== null || pushCount === 0}
          onClick={() => void push()}
        >
          {busy === "push" ? <IconRefresh size={12} className="animate-spin" /> : <IconUpload size={12} />}
          {busy === "push" ? "推送中…" : `推送到云端 (${pushCount})`}
        </button>
      </div>

      <p className="nx-hint mb-3">
        本机数据与云端密文副本的对比(在本机解密后比较,服务端看不到内容)。
        推送会把本机较新的内容加密送上云端;拉取并应用会把云端较新的内容合并到本机(冲突按修订号+载荷 hash 裁决,删除以墓碑清理)。
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

      {applyInfo && (
        <div className="nx-alert nx-alert-info mb-3 flex items-start gap-2">
          <IconCheckCircle size={13} className="mt-0.5 shrink-0" />
          <span>{applyInfo}</span>
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
