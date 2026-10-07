import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { assetApi, syncApi, transcriptApi, type TranscriptChunk } from "../../ipc/commands";
import type { SyncCollectAsset, SyncCollectCredential, SyncCollectTombstone, SyncKindOptIn, SyncReport } from "../../ipc/types";
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
import {
  aiProfilePayload,
  collectAssetPayload,
  credentialPayload,
  groupPayload,
  knownHostPayload,
  marshalSyncPayload,
  snippetPayload,
  tombstonePayload,
  transcriptPayload,
} from "../auth/syncPayload";
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

// ---------- 桌面端:账号登录与同步 ----------
// 登录只保存凭据完成同步配置,不传输数据;会话在 sync_now 时惰性建立。

function DesktopLinkCard() {
  const { pushToast } = useUi();
  const qc = useQueryClient();

  const [link, setLink] = useState<AccountLink | null>(null);
  const [status, setStatus] = useState<SyncStatusView | null>(null);
  const [draft, setDraft] = useState({ url: "", username: "", password: "", insecure: false });
  const [draftEdited, setDraftEdited] = useState(false);
  const [busy, setBusy] = useState<null | "login" | "sync">(null);
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

  const login = async () => {
    setBusy("login");
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
      pushToast("success", "登录信息已保存 · 同步配置完成,点「立即同步」开始传输数据");
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
        输入服务端地址、账号和密码,登录一次即完成同步配置;数据只在你点「立即同步」时加密传输,
        登录本身不上传也不下载。数据在你的设备上加密,服务端只存密文;不登录账号也完全可以继续本地使用。
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
            <span className="nx-hint block">勾选后不再验证服务端身份,可能被中间人冒充;只有自签证书的内网/自建地址才需要勾。</span>
          </span>
        </label>
      </div>

      <div className="mt-3 flex flex-wrap items-center gap-2">
        <button
          className="nx-btn nx-btn-primary nx-btn-sm"
          disabled={busy !== null || !draft.url.trim() || !draft.username.trim() || (!draft.password && !link?.hasPassword)}
          onClick={() => void login()}
        >
          {busy === "login" ? <IconRefresh size={12} className="animate-spin" /> : <IconLock size={12} />}
          {busy === "login" ? "登录中…" : "登录"}
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
          会话记录(终端录像)默认不同步;要同步某一条,到「终端历史」里对那条单独打开同步开关。
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
        payloadHash: await objectPayloadHash(utf8Bytes(marshalSyncPayload(payload))),
      });
    }
  }
  return out;
}

interface LocalCollection {
  entities: LocalEntity[];
  warnings: string[];
}

const COLLECT_PAGE_LIMIT = 512;

async function collectAssetsAll(): Promise<SyncCollectAsset[]> {
  const out: SyncCollectAsset[] = [];
  let afterId: string | undefined;
  for (let page = 0; page < 10000; page++) {
    const result = await syncApi.collectAssets(true, afterId, COLLECT_PAGE_LIMIT);
    out.push(...result.assets);
    if (!result.hasMore) return out;
    if (!result.nextAfterId) throw new Error("资产收集分页游标缺失");
    afterId = result.nextAfterId;
  }
  throw new Error("资产收集分页超出上限");
}

async function collectTombstonesAll(): Promise<SyncCollectTombstone[]> {
  const out: SyncCollectTombstone[] = [];
  let afterId: string | undefined;
  for (let page = 0; page < 10000; page++) {
    const result = await syncApi.collectTombstones(afterId, COLLECT_PAGE_LIMIT);
    out.push(...result.tombstones);
    if (!result.hasMore) return out;
    if (!result.nextAfterId) throw new Error("墓碑收集分页游标缺失");
    afterId = result.nextAfterId;
  }
  throw new Error("墓碑收集分页超出上限");
}

async function collectCredentialsAll(): Promise<SyncCollectCredential[]> {
  const out: SyncCollectCredential[] = [];
  let afterId: string | undefined;
  for (let page = 0; page < 10000; page++) {
    const result = await syncApi.collectCredentials(true, afterId, COLLECT_PAGE_LIMIT);
    out.push(...result.credentials);
    if (!result.hasMore) return out;
    if (!result.nextAfterId) throw new Error("凭据收集分页游标缺失");
    afterId = result.nextAfterId;
  }
  throw new Error("凭据收集分页超出上限");
}

async function collectKnownHostsAll(): Promise<import("../../ipc/types").SyncCollectKnownHost[]> {
  const out: import("../../ipc/types").SyncCollectKnownHost[] = [];
  let afterId: string | undefined;
  for (let page = 0; page < 10000; page++) {
    const result = await syncApi.collectKnownHosts(afterId, COLLECT_PAGE_LIMIT);
    out.push(...result.knownHosts);
    if (!result.hasMore) return out;
    if (!result.nextAfterId) throw new Error("已知主机收集分页游标缺失");
    afterId = result.nextAfterId;
  }
  throw new Error("已知主机收集分页超出上限");
}

async function collectAIProfilesAll(): Promise<import("../../ipc/types").SyncCollectAIProfile[]> {
  const out: import("../../ipc/types").SyncCollectAIProfile[] = [];
  let afterId: string | undefined;
  for (let page = 0; page < 10000; page++) {
    const result = await syncApi.collectAIProfiles(true, afterId, COLLECT_PAGE_LIMIT);
    out.push(...result.profiles);
    if (!result.hasMore) return out;
    if (!result.nextAfterId) throw new Error("AI 模型档案收集分页游标缺失");
    afterId = result.nextAfterId;
  }
  throw new Error("AI 模型档案收集分页超出上限");
}

function credentialStateLabel(state: string): string {
  switch (state) {
    case "locked":
      return "凭据库未解锁";
    case "unavailable":
      return "凭据库不可用";
    case "error":
      return "解密失败";
    case "withheld":
      return "未请求明文";
    default:
      return state;
  }
}

async function localEntity(
  id: string,
  kind: SyncObjectKind,
  name: string,
  updatedAt: number,
  deletedAt: number | null,
  payload: unknown,
): Promise<LocalEntity> {
  return { id, kind, name, updatedAt, deletedAt, payload, payloadHash: await objectPayloadHash(utf8Bytes(marshalSyncPayload(payload))) };
}

// loadLocalEntities 经 M141 collect RPC 收集完整本地副本: 软删资产、两类墓碑与凭据都进入对比,
// 与 internal/sync collectLocalObjects 同序同键(同 ID 后写覆盖先写,墓碑压过存活对象,存活 transcript 最后)。
// 读不到明文的凭据(locked/unavailable/error)明确记入 warnings,不静默遗漏。
// known_host/AI 模型档案按账号用户 opt-in 收集(默认关): 开启才进入对比与推送;
// 档案 apiKey 被扣留(locked/unavailable/error)同样只记 warning; 无密钥档案(apiKeySet=false)不受凭据库锁定影响。
async function loadLocalEntities(optIn: SyncKindOptIn): Promise<LocalCollection> {
  const warnings: string[] = [];
  const [groups, snippets, transcripts, assets, tombstones, credentials, knownHosts, aiProfiles] = await Promise.all([
    assetApi.groupList(),
    assetApi.snippetList(),
    loadOptedInTranscripts(),
    collectAssetsAll(),
    collectTombstonesAll(),
    collectCredentialsAll(),
    optIn.knownHost ? collectKnownHostsAll() : Promise.resolve([]),
    optIn.aiProfile ? collectAIProfilesAll() : Promise.resolve([]),
  ]);
  const byId = new Map<string, LocalEntity>();
  for (const g of groups) {
    byId.set(g.id, await localEntity(g.id, "group", g.name, g.updatedAt, null, groupPayload(g)));
  }
  for (const c of credentials) {
    if (c.secretState !== "revealed" || c.secret === undefined) {
      warnings.push(`凭据「${c.name}」${credentialStateLabel(c.secretState)},未进入本次对比与推送`);
      continue;
    }
    byId.set(c.id, await localEntity(c.id, "credential", c.name, c.updatedAt, null, credentialPayload(c)));
  }
  for (const s of snippets) {
    byId.set(s.id, await localEntity(s.id, "snippet", s.name, s.updatedAt, null, snippetPayload(s)));
  }
  for (const a of assets) {
    byId.set(a.id, await localEntity(a.id, "asset", a.name, a.updatedAt, a.deletedAt ?? null, collectAssetPayload(a)));
  }
  for (const k of knownHosts) {
    byId.set(k.id, await localEntity(k.id, "known_host", `${k.host}:${k.port}`, k.addedAt, null, knownHostPayload(k)));
  }
  for (const p of aiProfiles) {
    if (p.apiKeySet && (p.apiKeyState !== "revealed" || p.apiKey === undefined)) {
      warnings.push(`AI 档案「${p.name}」${credentialStateLabel(p.apiKeyState)},未进入本次对比与推送`);
      continue;
    }
    byId.set(p.id, await localEntity(p.id, "ai_profile", p.name, p.updatedAt, null, aiProfilePayload(p)));
  }
  for (const t of tombstones) {
    // 已禁用种类的墓碑不参与对比与推送: 禁用同步不得删除远端对象
    if (t.targetKind === "known_host" && !optIn.knownHost) continue;
    if (t.targetKind === "ai_profile" && !optIn.aiProfile) continue;
    const label = KIND_LABELS[t.targetKind] ?? t.targetKind;
    byId.set(t.id, await localEntity(t.id, "tombstone", `${label} ${t.id}`, t.deletedAt, t.deletedAt, tombstonePayload(t)));
  }
  for (const tr of transcripts) {
    byId.set(tr.id, tr);
  }
  return { entities: [...byId.values()], warnings };
}

function revisionOf(updatedAt: number, deletedAt: number | null): number {
  return deletedAt !== null && deletedAt > updatedAt ? deletedAt : updatedAt;
}

// remoteKindVisible 按 opt-in 过滤远端对象: 禁用种类既不应用也不参与对比;
// known_host/ai_profile 的远端墓碑按其 targetKind 同样受开关约束(禁用同步不得删除远端/本地对象)。
function remoteKindVisible(kind: SyncObjectKind, plaintext: string, optIn: SyncKindOptIn): boolean {
  if (kind === "known_host") return optIn.knownHost;
  if (kind === "ai_profile") return optIn.aiProfile;
  if (kind === "tombstone") {
    const target = (JSON.parse(plaintext) as { targetKind?: string }).targetKind;
    if (target === "known_host") return optIn.knownHost;
    if (target === "ai_profile") return optIn.aiProfile;
  }
  return true;
}

async function loadRemoteObjects(dek: Uint8Array, optIn: SyncKindOptIn): Promise<RemoteState> {
  const ids = await syncV2Api.ids();
  const out: RemoteObject[] = [];
  for (const entry of ids.entries) {
    const page = await syncV2Api.pull(0, [entry.id]);
    const wire = page.objects[0];
    if (!wire) continue;
    const { kind, plaintext } = await tryOpenSyncObject(dek, base64ToBytes(wire.blob), entry.id);
    const text = new TextDecoder().decode(plaintext);
    if (!remoteKindVisible(kind, text, optIn)) continue;
    const payloadHash = await objectPayloadHash(plaintext);
    if (kind === "tombstone") {
      const t = JSON.parse(text) as { targetKind?: string; deletedAt?: number };
      out.push({ id: entry.id, kind: "tombstone", name: entry.id, updatedAt: t.deletedAt ?? 0, deletedAt: t.deletedAt ?? 0, seq: entry.seq, payloadHash, plaintext: text });
      continue;
    }
    if (kind === "transcript") {
      // transcript 没有 updatedAt: 修订号即 endedAt(内容结束后不可变),名称取资产名。
      const t = JSON.parse(text) as { assetName?: string; endedAt?: number };
      out.push({
        id: entry.id,
        kind,
        name: t.assetName ? `${t.assetName} 的会话记录` : entry.id,
        updatedAt: t.endedAt ?? 0,
        deletedAt: null,
        seq: entry.seq,
        payloadHash,
        plaintext: text,
      });
      continue;
    }
    if (kind === "known_host") {
      // known_host 没有 name/updatedAt: 修订号即 addedAt, 名称取 host:port。
      const k = JSON.parse(text) as { host?: string; port?: number; addedAt?: number };
      out.push({
        id: entry.id,
        kind,
        name: k.host ? `${k.host}:${k.port}` : entry.id,
        updatedAt: k.addedAt ?? 0,
        deletedAt: null,
        seq: entry.seq,
        payloadHash,
        plaintext: text,
      });
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

const DEFAULT_KIND_OPT_IN: SyncKindOptIn = { knownHost: false, aiProfile: false };

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

// sealAll 按 kind 依赖序把 winner 对象加密成线上形态(与 Go objectKindRank 对齐:
// known_host/ai_profile 排在 tombstone 之前, 墓碑仍最后)。
async function sealAll(dek: Uint8Array, list: LocalEntity[]): Promise<{ id: string; blob: string }[]> {
  const rank = (k: SyncObjectKind) => {
    switch (k) {
      case "group": return 0;
      case "credential": return 1;
      case "snippet": return 2;
      case "asset": return 3;
      case "known_host": return 4;
      case "ai_profile": return 5;
      case "tombstone": return 6;
      case "transcript": return 7;
      default: return 8;
    }
  };
  const ordered = [...list].sort((a, b) => rank(a.kind) - rank(b.kind) || a.id.localeCompare(b.id));
  const objects: { id: string; blob: string }[] = [];
  for (const e of ordered) {
    const blob = await sealSyncObject(dek, utf8Bytes(marshalSyncPayload(e.payload)), e.id, e.kind);
    objects.push({ id: e.id, blob: bytesToBase64(blob) });
  }
  return objects;
}

// localWins 复刻 M117 merge.go 的 LWW 裁决:修订号大者胜,平手按载荷 sha256 字典序决胜。
// 载荷完全一致(同 hash)时本地无需推送(覆盖相同内容)。
function localWins(l: LocalEntity, r: RemoteObject): boolean {
  if (l.payloadHash === r.payloadHash) return false;
  if (l.kind === "transcript") return transcriptLocalWins(l, r);
  const lr = revisionOf(l.updatedAt, l.deletedAt);
  const rr = revisionOf(r.updatedAt, r.deletedAt);
  if (lr !== rr) return lr > rr;
  return l.payloadHash > r.payloadHash;
}

// transcriptLocalWins 按会话记录的 endedAt/不可变与内容补全语义裁决:完整内容胜过省略版
// (本机省略、云端完整时远端胜,走应用补全),否则按 endedAt 比较,平手按载荷 hash 决胜。
function transcriptLocalWins(l: LocalEntity, r: RemoteObject): boolean {
  const lp = l.payload as { contentOmitted?: boolean; endedAt?: number };
  const rp = JSON.parse(r.plaintext) as { contentOmitted?: boolean; endedAt?: number };
  const lFull = lp.contentOmitted !== true;
  const rFull = rp.contentOmitted !== true;
  if (lFull !== rFull) return lFull;
  const le = lp.endedAt ?? 0;
  const re = rp.endedAt ?? 0;
  if (le !== re) return le > re;
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
      return { text: "云端已删", tone: "nx-badge-amber", hint: "这条在云端已被删除。" };
  }
}

const KIND_LABELS: Record<string, string> = {
  group: "分组",
  asset: "资产",
  snippet: "片段",
  credential: "凭据",
  tombstone: "删除标记",
  transcript: "会话记录",
  known_host: "已知主机",
  ai_profile: "AI 档案",
};

function WebSyncConsole() {
  const user = useAuth((s) => s.user);
  const dek = useAuth((s) => s.dek);

  // 未登录时整卡不渲染:设置页只有 AuthCard「账号」一个登录入口,不在同步台重复放登录按钮。
  if (!user) return null;
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
  const [collectWarnings, setCollectWarnings] = useState<string[]>([]);
  const [cursor, setCursor] = useState<{ head: string; seq: number }>(() => loadCursor(user.id));
  const [busy, setBusy] = useState<null | "refresh" | "push" | "optin">(null);
  const [error, setError] = useState<string | null>(null);
  const [pushInfo, setPushInfo] = useState<string | null>(null);
  const [kindOptIn, setKindOptIn] = useState<SyncKindOptIn | null>(null);
  // opt-in 代次: 开关变更即递增, 旧代次的快照与进行中的 load/push/apply 一律作废,
  // 防止「已关闭同步仍推送/应用旧快照」(尤其删除墓碑删除远端副本)。
  const optInEpochRef = useRef(0);
  // 当前 local/remote 快照所属代次; 与 optInEpochRef 不一致时推送/应用保持禁用(reload 完成前)。
  const [snapshotEpoch, setSnapshotEpoch] = useState(-1);

  // opt-in 读取失败(旧服务端没有该命令/网络错误)按默认关处理, 不阻断对比台
  useEffect(() => {
    let cancelled = false;
    Promise.resolve()
      .then(() => syncApi.kindOptInGet())
      .then((view) => {
        if (!cancelled) setKindOptIn(view);
      })
      .catch(() => {
        if (!cancelled) setKindOptIn({ knownHost: false, aiProfile: false });
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const rows = useMemo(() => (local && remote ? buildRows(local, remote) : null), [local, remote]);

  const load = useCallback(
    (optIn: SyncKindOptIn, epoch: number) => {
      setError(null);
      return Promise.all([loadLocalEntities(optIn), loadRemoteObjects(dek, optIn)])
        .then(([l, r]) => {
          if (epoch !== optInEpochRef.current) return; // 乱序/过期 load 的回写丢弃
          setLocal(l.entities);
          setCollectWarnings(l.warnings);
          setRemote(r.objects);
          setSnapshotEpoch(epoch);
          // 游标以服务端最新 head/seq 为准(空 genesis 不得强制空字符串)
          const next = { head: r.head, seq: r.maxSeq };
          saveCursor(user.id, next);
          setCursor(next);
        })
        .catch((e: unknown) => {
          if (epoch !== optInEpochRef.current) return;
          setError(describeError(e));
        });
    },
    [dek, user.id],
  );

  useEffect(() => {
    if (kindOptIn) void load(kindOptIn, optInEpochRef.current);
  }, [load, kindOptIn]);

  const updateKindOptIn = async (patch: { knownHost?: boolean; aiProfile?: boolean }) => {
    optInEpochRef.current += 1; // 立即作废旧快照与进行中的 push/apply
    setBusy("optin");
    setError(null);
    try {
      const next = await syncApi.kindOptInSet(patch);
      setKindOptIn(next); // effect 以新代次触发 reload, 完成前操作保持禁用
    } catch (e) {
      setError(describeError(e));
      // 开关未改成: 按当前 opt-in 重新加载, 恢复同代可用快照
      void load(kindOptIn ?? DEFAULT_KIND_OPT_IN, optInEpochRef.current);
    } finally {
      setBusy(null);
    }
  };

  // winner 集合:仅本机独占或本地胜出(含平修订号按载荷 hash 决胜);计数与上传共用同一集合。
  const winners = useMemo(() => {
    if (!local || !remote) return [];
    return computeWinners(local, remote);
  }, [local, remote]);

  // applySet 集合:远端胜出(仅云端/云端较新/云端已删)需要拉取应用的对象;已一致(同 hash)不重复应用。
  const applySet = useMemo(() => {
    if (!local || !remote) return [];
    return remote.filter((r) => {
      const l = local.find((e) => e.id === r.id);
      if (!l) return true; // remote-only
      if (l.payloadHash === r.payloadHash) return false; // 已一致
      return !localWins(l, r); // remote-newer 或 remote-deleted
    });
  }, [local, remote]);

  const [applyInfo, setApplyInfo] = useState<string | null>(null);

  const applyRemote = async () => {
    const startEpoch = optInEpochRef.current;
    if (snapshotEpoch !== startEpoch) return; // 快照已过期(开关切换后 reload 未完成)
    setBusy("push");
    setError(null);
    setApplyInfo(null);
    try {
      const objects = applySet.map((r) => ({
        id: r.id,
        kind: r.kind,
        payload: JSON.parse(r.plaintext) as import("../../ipc/types").JsonValue,
      }));
      // 分批(单次上限 256); 每批前核对代次, 开关切换后不再发送旧集合
      let applied = 0;
      let identical = 0;
      let skipped = 0;
      const warnings: string[] = [];
      for (let i = 0; i < objects.length; i += 256) {
        if (optInEpochRef.current !== startEpoch) return;
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
      await load(kindOptIn ?? DEFAULT_KIND_OPT_IN, startEpoch);
    } catch (e) {
      setError(describeError(e));
    } finally {
      setBusy(null);
    }
  };

  const push = async () => {
    const startEpoch = optInEpochRef.current;
    if (snapshotEpoch !== startEpoch) return; // 快照已过期(开关切换后 reload 未完成)
    setBusy("push");
    setError(null);
    setPushInfo(null);
    const optIn = kindOptIn ?? DEFAULT_KIND_OPT_IN;
    try {
      // 对比基线(加载时的 head)。若服务端 head 已变,先重新拉取并以同一快照重算 winner,不沿用旧 winner。
      let head = cursor.head;
      let winnersNow = winners;
      const fresh = await syncV2Api.ids();
      if (optInEpochRef.current !== startEpoch) return;
      if (fresh.head !== head) {
        const remoteState = await loadRemoteObjects(dek, optIn);
        if (optInEpochRef.current !== startEpoch) return;
        setRemote(remoteState.objects);
        winnersNow = computeWinners(local ?? [], remoteState.objects);
        const base = { head: remoteState.head, seq: remoteState.maxSeq };
        saveCursor(user.id, base);
        setCursor(base);
        head = remoteState.head;
      }

      // 上传前最后核对: 开关切换后旧 winners 一律不得上传(即使 head 未变)
      if (optInEpochRef.current !== startEpoch) return;
      let resp = await syncV2Api.push(head, await sealAll(dek, winnersNow));

      // 空推(applied+skipped=0)或真 409(他端已更新):重新拉取并以同一快照重算 winner,再重试一次,不得只换 known_head。
      const reconcileAndRetry = async (): Promise<typeof resp | null> => {
        const remoteState = await loadRemoteObjects(dek, optIn);
        if (optInEpochRef.current !== startEpoch) return null;
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
      await load(optIn, startEpoch);
    } catch (e) {
      if (e instanceof AuthApiError && e.status === 409) {
        // 真 409:重新对账并以新 head 重试一次;仍失败才报错。
        try {
          const remoteState = await loadRemoteObjects(dek, optIn);
          if (optInEpochRef.current !== startEpoch) return;
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
          await load(optIn, startEpoch);
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
        <button className="nx-btn nx-btn-ghost nx-btn-sm" disabled={busy !== null} onClick={() => void load(kindOptIn ?? DEFAULT_KIND_OPT_IN, optInEpochRef.current)}>
          <IconRefresh size={12} className={busy === "refresh" ? "animate-spin" : ""} />
          刷新对比
        </button>
        <button
          className="nx-btn nx-btn-outline nx-btn-sm"
          disabled={busy !== null || snapshotEpoch !== optInEpochRef.current || applySet.length === 0}
          onClick={() => void applyRemote()}
        >
          {busy === "push" ? <IconRefresh size={12} className="animate-spin" /> : <IconDownload size={12} />}
          {busy === "push" ? "应用中…" : `拉取并应用 (${applySet.length})`}
        </button>
        <button
          className="nx-btn nx-btn-primary nx-btn-sm"
          disabled={busy !== null || snapshotEpoch !== optInEpochRef.current || pushCount === 0}
          onClick={() => void push()}
        >
          {busy === "push" ? <IconRefresh size={12} className="animate-spin" /> : <IconUpload size={12} />}
          {busy === "push" ? "推送中…" : `推送到云端 (${pushCount})`}
        </button>
      </div>

      <p className="nx-hint mb-3">
        本机数据与云端密文副本的对比(在本机解密后比较,服务端看不到内容)。
        推送会把本机较新的内容加密送上云端;拉取并应用会把云端较新的内容合并到本机(冲突按修订号+载荷 hash 裁决,删除会随删除标记同步)。
      </p>

      {kindOptIn && (
        <div className="mb-3 flex flex-col gap-2">
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              className="mt-0.5 h-4 w-4 shrink-0"
              checked={kindOptIn.knownHost}
              disabled={busy !== null}
              onChange={(e) => void updateKindOptIn({ knownHost: e.target.checked })}
            />
            <span className="text-[12px] text-neutral-300">
              同步已知主机(主机信任)
              <span className="nx-hint block">默认关闭。开启后,已接受的主机密钥会端到端加密同步;本地删除会随同步删除云端副本。</span>
            </span>
          </label>
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              className="mt-0.5 h-4 w-4 shrink-0"
              checked={kindOptIn.aiProfile}
              disabled={busy !== null}
              onChange={(e) => void updateKindOptIn({ aiProfile: e.target.checked })}
            />
            <span className="text-[12px] text-neutral-300">
              同步 AI 模型档案
              <span className="nx-hint block">默认关闭。开启后,档案设置会端到端加密同步;API 密钥只在凭据库已解锁时随档案同步。</span>
            </span>
          </label>
        </div>
      )}

      {error && (
        <div className="nx-alert nx-alert-danger mb-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">{error}</span>
          <button className="nx-btn nx-btn-ghost nx-btn-sm shrink-0" onClick={() => void load(kindOptIn ?? DEFAULT_KIND_OPT_IN, optInEpochRef.current)}>
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

      {collectWarnings.length > 0 && (
        <div className="nx-alert nx-alert-info mb-3 flex items-start gap-2">
          <IconInfo size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words text-amber-300">{collectWarnings.join(";")}</span>
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
          会话记录(终端录像)默认不同步;要同步某一条,到「终端历史」里对那条单独打开同步开关。
          已知主机与 AI 模型档案同样默认不同步,需要时在上方单独开启;关闭开关不会删除云端已有副本。
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
