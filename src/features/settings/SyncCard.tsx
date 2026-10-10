import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { assetApi, syncApi, transcriptApi, type TranscriptChunk } from "../../ipc/commands";
import type { AccountLink, SyncCollectAsset, SyncCollectCredential, SyncCollectTombstone, SyncKindOptIn, SyncStatus } from "../../ipc/types";
import { AuthApiError, syncV2Api } from "../../ipc/authApi";
import { useAuth } from "../auth/store";
import {
  base64ToBytes,
  bytesToBase64,
  CryptoError,
  objectPayloadHash,
  sealSyncObject,
  tryOpenSyncObject,
  utf8Bytes,
  type SyncObjectKind,
} from "../auth/crypto";
import {
  aiPermissionPayload,
  aiProfilePayload,
  collectAssetPayload,
  credentialPayload,
  groupPayload,
  knownHostPayload,
  marshalSyncPayload,
  preferencePayload,
  snippetPayload,
  tombstonePayload,
  transcriptPayload,
} from "../auth/syncPayload";
import { useUi } from "../../app/store";
import { WEB } from "../../ipc/env";
import { describeError } from "../../ui/errorText";
import { AuthCard } from "./AuthCard";
import { AccountCard } from "./AccountCard";
import {
  IconChevronDown,
  IconChevronRight,
  IconInfo,
  IconLock,
  IconRefresh,
  IconServer,
  IconXCircle,
} from "../../ui/icons";

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
  if (WEB) return <WebSyncConsole />;
  return <DesktopLinkCard />;
}

// ---------- 桌面端:账号登录与同步 ----------

function DesktopLinkCard() {
  const { pushToast } = useUi();
  const qc = useQueryClient();

  const [link, setLink] = useState<AccountLink | null>(null);
  const [status, setStatus] = useState<SyncStatus | null>(null);
  const [draft, setDraft] = useState({ url: "", username: "", password: "", insecure: false });
  const [draftEdited, setDraftEdited] = useState(false);
  const [busy, setBusy] = useState<null | "login">(null);
  const [error, setError] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const insecurePanelId = useId();
  const [insecureOpen, setInsecureOpen] = useState(false);
  const savedInsecure = link?.insecure ?? false;
  useEffect(() => {
    if (savedInsecure) setInsecureOpen(true);
  }, [savedInsecure]);

  const updateDraft = (patch: Partial<{ url: string; username: string; password: string; insecure: boolean }>) => {
    setDraftEdited(true);
    setDraft((d) => ({ ...d, ...patch }));
  };

  const load = useCallback(() => {
    setLoadError(null);
    return Promise.all([
      syncApi.linkGet().then((l) => {
        setLink(l);
        // 初次加载用已保存链接回填 URL/用户名/insecure;密码保持空白;不覆盖未保存编辑
        setDraft((d) => (draftEdited ? d : { ...d, url: l.url, username: l.username, insecure: l.insecure }));
      }),
      syncApi.status().then((s) => setStatus(s)),
    ]).catch((e: unknown) => setLoadError(describeError(e)));
  }, [draftEdited]);

  useEffect(() => {
    void load();
  }, [load]);

  const login = async () => {
    setBusy("login");
    setError(null);
    try {
      await syncApi.linkSet({
        url: draft.url.trim(),
        username: draft.username.trim(),
        ...(draft.password ? { password: draft.password } : {}),
        insecure: draft.insecure,
      });
      setDraft((d) => ({ ...d, password: "" }));
      void qc.invalidateQueries({ queryKey: ["sync-link"] });
      await load();
      pushToast("success", "登录信息已保存，自动同步已开启");
    } catch (e) {
      setError(describeError(e));
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
        登录后自动同步。首轮先拉取并合并云端内容，再推送本机变更；数据在本机加密后传输，服务端只保存密文。不登录也能继续本地使用。
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
            placeholder={link?.hasPassword ? "留空则不修改已保存的密码" : "账号密码（用于解锁数据密钥）"}
            value={draft.password}
            autoComplete="new-password"
            onChange={(e) => updateDraft({ password: e.target.value })}
          />
        </div>
        <div className="mt-1 border-t border-neutral-800/60 pt-2">
          <button
            type="button"
            className="flex items-center gap-1.5 text-[12px] text-neutral-400 transition-colors hover:text-neutral-100"
            aria-expanded={insecureOpen}
            aria-controls={insecurePanelId}
            onClick={() => setInsecureOpen((v) => !v)}
          >
            {insecureOpen ? (
              <IconChevronDown size={12} className="shrink-0" />
            ) : (
              <IconChevronRight size={12} className="shrink-0" />
            )}
            高级选项
          </button>
          {insecureOpen && (
            <div id={insecurePanelId} className="mt-2">
              <label className="flex items-start gap-2">
                <input
                  type="checkbox"
                  className="mt-0.5 h-4 w-4 shrink-0"
                  checked={draft.insecure}
                  onChange={(e) => updateDraft({ insecure: e.target.checked })}
                />
                <span className="text-[12px] text-neutral-300">
                  跳过证书校验
                  <span className="nx-hint block">勾选后不再验证服务端身份，可能被中间人冒充；仅在自签名证书的内网或自建服务中使用。</span>
                </span>
              </label>
            </div>
          )}
        </div>
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
        {status?.lastError && <span className="nx-hint text-amber-300">上次失败：{status.lastError}</span>}
      </div>

      {error && (
        <div className="nx-alert nx-alert-danger mt-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">{error}</span>
        </div>
      )}

      <div className="nx-alert nx-alert-info mt-3 flex items-start gap-2">
        <IconInfo size={14} className="mt-0.5 shrink-0" />
        <div>
          会话记录（终端录像）默认不同步；如需同步，请在「终端历史」中为对应记录开启同步。同步采用端到端加密，服务端无法查看资产名、主机、用户名和内容。
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
  blobHash: string;
  plaintext: string;
}

interface RemoteState {
  objects: RemoteObject[];
  warnings: string[];
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
// 与 internal/sync collectLocalObjects 同序同键(同 ID 后写覆盖先写,较新存活对象压过旧墓碑,存活 transcript 最后)。
// 读不到明文的凭据(locked/unavailable/error)明确记入 warnings,不静默遗漏。
// 可选同步种类按账号用户开关收集(默认开启): 开启才进入对比与推送;
// 档案 apiKey 被扣留(locked/unavailable/error)同样只记 warning; 无密钥档案(apiKeySet=false)不受凭据库锁定影响。
async function loadLocalEntities(optIn: SyncKindOptIn): Promise<LocalCollection> {
  const warnings: string[] = [];
  const [groups, snippets, transcripts, assets, tombstones, credentials, knownHosts, aiProfiles, aiPermission, preferences] = await Promise.all([
    assetApi.groupList(),
    assetApi.snippetList(),
    loadOptedInTranscripts(),
    collectAssetsAll(),
    collectTombstonesAll(),
    collectCredentialsAll(),
    optIn.knownHost ? collectKnownHostsAll() : Promise.resolve([]),
    optIn.aiProfile ? collectAIProfilesAll() : Promise.resolve([]),
    optIn.aiPermission ? syncApi.collectAIPermission() : Promise.resolve({ id: "", mode: "", dangerRules: [], updatedAt: 0, found: false }),
    optIn.preferences ? syncApi.collectPreferences() : Promise.resolve([]),
  ]);
  const byId = new Map<string, LocalEntity>();
  for (const g of groups) {
    byId.set(g.id, await localEntity(g.id, "group", g.name, g.updatedAt, null, groupPayload(g)));
  }
  for (const c of credentials) {
    if (c.secretState !== "revealed" || c.secret === undefined) {
      warnings.push(`凭据「${c.name}」${credentialStateLabel(c.secretState)}，未进入本次对比与推送`);
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
      warnings.push(`AI 模型档案「${p.name}」${credentialStateLabel(p.apiKeyState)}，未进入本次对比与推送`);
      continue;
    }
    byId.set(p.id, await localEntity(p.id, "ai_profile", p.name, p.updatedAt, null, aiProfilePayload(p)));
  }
  if (aiPermission.found) {
    byId.set(aiPermission.id, await localEntity(aiPermission.id, "ai_permission", "全局 AI 权限", aiPermission.updatedAt, null, aiPermissionPayload(aiPermission)));
  }
  for (const p of preferences) {
    byId.set(p.id, await localEntity(p.id, "preference", p.key, p.updatedAt, null, preferencePayload(p)));
  }
  for (const t of tombstones) {
    // 已禁用种类的墓碑不参与对比与推送: 禁用同步不得删除远端对象
    if (t.targetKind === "known_host" && !optIn.knownHost) continue;
    if (t.targetKind === "ai_profile" && !optIn.aiProfile) continue;
    if (t.targetKind === "ai_permission" && !optIn.aiPermission) continue;
    if (t.targetKind === "preference" && !optIn.preferences) continue;
    const live = byId.get(t.id);
    if (live && revisionOf(live.updatedAt, live.deletedAt) > t.deletedAt) continue;
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
// 可选种类的远端墓碑按其 targetKind 同样受开关约束(禁用同步不得删除远端/本地对象)。
function remoteKindVisible(kind: SyncObjectKind, plaintext: string, optIn: SyncKindOptIn): boolean {
  if (kind === "known_host") return optIn.knownHost;
  if (kind === "ai_profile") return optIn.aiProfile;
  if (kind === "ai_permission") return !!optIn.aiPermission;
  if (kind === "preference") return !!optIn.preferences;
  if (kind === "tombstone") {
    const target = (JSON.parse(plaintext) as { targetKind?: string }).targetKind;
    if (target === "known_host") return optIn.knownHost;
    if (target === "ai_profile") return optIn.aiProfile;
    if (target === "ai_permission") return !!optIn.aiPermission;
    if (target === "preference") return !!optIn.preferences;
  }
  return true;
}

interface RemoteCache {
  head: string;
  objects: Map<string, RemoteObject>;
}

const remoteCaches = new WeakMap<Uint8Array, RemoteCache>();

async function decodeRemoteObject(entry: { id: string; seq: number; blob_hash: string }, blob: string, dek: Uint8Array): Promise<RemoteObject> {
  const { kind, plaintext } = await tryOpenSyncObject(dek, base64ToBytes(blob), entry.id);
  const text = new TextDecoder().decode(plaintext);
  const payloadHash = await objectPayloadHash(plaintext);
  const base = {
    id: entry.id,
    kind,
    updatedAt: 0,
    deletedAt: null,
    seq: entry.seq,
    payloadHash,
    blobHash: entry.blob_hash,
    plaintext: text,
  };
  if (kind === "tombstone") {
    const t = JSON.parse(text) as { targetKind?: string; deletedAt?: number };
    return { ...base, name: entry.id, updatedAt: t.deletedAt ?? 0, deletedAt: t.deletedAt ?? 0 };
  }
  if (kind === "transcript") {
    const t = JSON.parse(text) as { assetName?: string; endedAt?: number };
    return { ...base, name: t.assetName ? `${t.assetName} 的会话记录` : entry.id, updatedAt: t.endedAt ?? 0 };
  }
  if (kind === "known_host") {
    const k = JSON.parse(text) as { host?: string; port?: number; addedAt?: number };
    return { ...base, name: k.host ? `${k.host}:${k.port}` : entry.id, updatedAt: k.addedAt ?? 0 };
  }
  const p = JSON.parse(text) as { name?: string; updatedAt?: number; deletedAt?: number };
  return { ...base, name: p.name ?? entry.id, updatedAt: p.updatedAt ?? 0, deletedAt: p.deletedAt ?? null };
}

type SyncIds = Awaited<ReturnType<typeof syncV2Api.ids>>;

async function loadRemoteObjects(dek: Uint8Array, optIn: SyncKindOptIn, idsOverride?: SyncIds): Promise<RemoteState> {
  const cache = remoteCaches.get(dek);
  const previous = cache?.objects ?? new Map<string, RemoteObject>();
  const ids = idsOverride ?? await syncV2Api.ids(cache?.head || undefined);
  if (ids.unchanged && cache) {
    const objects = [...cache.objects.values()].filter((object) => remoteKindVisible(object.kind, object.plaintext, optIn));
    return { objects, warnings: [], head: ids.head, maxSeq: ids.max_seq };
  }

  const next = new Map<string, RemoteObject>();
  const pending: { id: string; seq: number; blob_hash: string }[] = [];
  for (const entry of ids.entries ?? []) {
    const existing = previous.get(entry.id);
    if (existing && existing.blobHash === entry.blob_hash) {
      next.set(entry.id, existing);
    } else {
      pending.push(entry);
    }
  }

  const warnings: string[] = [];
  for (let offset = 0; offset < pending.length; offset += 256) {
    const batch = pending.slice(offset, offset + 256);
    const page = await syncV2Api.pull(0, batch.map((entry) => entry.id));
    for (const entry of batch) {
      const wire = page.objects.find((object) => object.id === entry.id);
      if (!wire) continue;
      try {
        const object = await decodeRemoteObject(entry, wire.blob, dek);
        next.set(entry.id, object);
      } catch (e) {
        if (e instanceof CryptoError && e.code !== "decrypt") throw e;
        warnings.push(`云端对象 ${entry.id} 解密失败，已跳过；本机同名对象可推送覆盖`);
      }
    }
  }
  remoteCaches.set(dek, { head: ids.head, objects: next });
  const objects = [...next.values()].filter((object) => remoteKindVisible(object.kind, object.plaintext, optIn));
  return { objects, warnings, head: ids.head, maxSeq: ids.max_seq };
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

// sealAll 按 kind 依赖序把 winner 对象加密成线上形态(与 Go objectKindRank 对齐:
// AI 权限与偏好排在普通墓碑之前, 会话记录仍最后)。
async function sealAll(dek: Uint8Array, list: LocalEntity[]): Promise<{ id: string; blob: string }[]> {
  const rank = (k: SyncObjectKind) => {
    switch (k) {
      case "group": return 0;
      case "credential": return 1;
      case "snippet": return 2;
      case "asset": return 3;
      case "known_host": return 4;
      case "ai_profile": return 5;
      case "ai_permission": return 6;
      case "preference": return 7;
      case "tombstone": return 8;
      case "transcript": return 9;
      default: return 10;
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

export const WEB_SYNC_REQUEST_EVENT = "nexterm:web-sync-request";
export const WEB_SYNC_RESULT_EVENT = "nexterm:web-sync-result";
export const WEB_SYNC_ERROR_EVENT = "nexterm:web-sync-error";

interface Snapshot {
  local: LocalEntity[];
  remote: RemoteObject[];
  warnings: string[];
  head: string;
  maxSeq: number;
}

export interface WebSyncResult extends Snapshot {
  applied: number;
  pushed: number;
}

async function loadSnapshot(dek: Uint8Array, optIn: SyncKindOptIn, idsOverride?: SyncIds): Promise<Snapshot> {
  const [local, remote] = await Promise.all([loadLocalEntities(optIn), loadRemoteObjects(dek, optIn, idsOverride)]);
  return {
    local: local.entities,
    remote: remote.objects,
    warnings: [...local.warnings, ...remote.warnings],
    head: remote.head,
    maxSeq: remote.maxSeq,
  };
}

function computeApplySet(local: LocalEntity[], remote: RemoteObject[]): RemoteObject[] {
  return remote.filter((r) => {
    const l = local.find((entity) => entity.id === r.id);
    if (!l) return true;
    if (l.payloadHash === r.payloadHash) return false;
    return !localWins(l, r);
  });
}

async function optInMatches(optIn: SyncKindOptIn, shouldContinue: () => boolean): Promise<boolean> {
  if (!shouldContinue()) return false;
  const current = await syncApi.kindOptInGet();
  return current.knownHost === optIn.knownHost && current.aiProfile === optIn.aiProfile && !!current.aiPermission === !!optIn.aiPermission && !!current.preferences === !!optIn.preferences && shouldContinue();
}

export async function runWebSync(
  dek: Uint8Array,
  optIn: SyncKindOptIn,
  shouldContinue: () => boolean = () => true,
): Promise<WebSyncResult | null> {
  let snapshot = await loadSnapshot(dek, optIn);
  let operationWarnings = [...snapshot.warnings];
  const reload = async (idsOverride?: SyncIds) => {
    snapshot = await loadSnapshot(dek, optIn, idsOverride);
    operationWarnings = [...new Set([...operationWarnings, ...snapshot.warnings])];
    snapshot = { ...snapshot, warnings: [...operationWarnings] };
  };
  if (!(await optInMatches(optIn, shouldContinue))) return null;

  const applySet = computeApplySet(snapshot.local, snapshot.remote);
  let applied = 0;
  for (let i = 0; i < applySet.length; i += 256) {
    if (!(await optInMatches(optIn, shouldContinue))) return null;
    const result = await syncApi.applyObjects(
      applySet.slice(i, i + 256).map((r) => ({
        id: r.id,
        kind: r.kind,
        payload: JSON.parse(r.plaintext) as import("../../ipc/types").JsonValue,
      })),
    );
    applied += result.applied;
    for (const object of result.objects) {
      if (object.warning) operationWarnings.push(`${object.id}: ${object.warning}`);
    }
  }
  if (applySet.length > 0) await reload();
  if (!(await optInMatches(optIn, shouldContinue))) return null;

  let winners = computeWinners(snapshot.local, snapshot.remote);
  let pushed = 0;
  if (winners.length > 0) {
    const fresh = await syncV2Api.ids();
    if (!(await optInMatches(optIn, shouldContinue))) return null;
    if (fresh.head !== snapshot.head) {
      await reload(fresh);
      winners = computeWinners(snapshot.local, snapshot.remote);
    }
    if (winners.length > 0) {
      let response: Awaited<ReturnType<typeof syncV2Api.push>>;
      try {
        response = await syncV2Api.push(snapshot.head, await sealAll(dek, winners));
      } catch (e) {
        if (!(e instanceof AuthApiError && e.status === 409)) throw e;
        const latest = await syncV2Api.ids();
        await reload(latest);
        winners = computeWinners(snapshot.local, snapshot.remote);
        if (winners.length === 0) return { ...snapshot, applied, pushed: 0 };
        if (!(await optInMatches(optIn, shouldContinue))) return null;
        response = await syncV2Api.push(snapshot.head, await sealAll(dek, winners));
      }
      if (response.applied === 0 && response.skipped === 0) {
        const latest = await syncV2Api.ids();
        await reload(latest);
        winners = computeWinners(snapshot.local, snapshot.remote);
        if (winners.length > 0) {
          if (!(await optInMatches(optIn, shouldContinue))) return null;
          response = await syncV2Api.push(snapshot.head, await sealAll(dek, winners));
        }
      }
      pushed = response.applied;
      await reload();
    }
  }
  return { ...snapshot, warnings: [...new Set([...snapshot.warnings, ...operationWarnings])], applied, pushed };
}

function rowLabel(r: Row): { text: string; tone: string; hint: string } {
  switch (r.state) {
    case "local-only":
      return { text: "仅本机", tone: "", hint: "云端还没有这条。推送会新建。" };
    case "remote-only":
      return { text: "仅云端", tone: "", hint: "本机还没有这条，拉取并应用会新建。" };
    case "same":
      return { text: "已一致", tone: "nx-badge-green", hint: "两边内容相同。" };
    case "local-newer":
      return { text: "本机较新", tone: "nx-badge-amber", hint: "本机版本较新，推送会覆盖云端。" };
    case "remote-newer":
      return { text: "云端较新", tone: "nx-badge-amber", hint: "云端版本较新，拉取并应用会覆盖本机。" };
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
  ai_profile: "AI 模型档案",
  ai_permission: "AI 权限",
  preference: "偏好设置",
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
        输入账号密码在本机解锁数据密钥（约需几秒），密钥不会离开本机。
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
        <button className="nx-btn nx-btn-primary" disabled={busy || !password} onClick={() => void submit()}>
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
  const dek = useAuth((s) => s.dek)!;
  const [local, setLocal] = useState<LocalEntity[] | null>(null);
  const [remote, setRemote] = useState<RemoteObject[] | null>(null);
  const [collectWarnings, setCollectWarnings] = useState<string[]>([]);
  const [refreshBusy, setRefreshBusy] = useState(false);
  const [optInBusy, setOptInBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [kindOptIn, setKindOptIn] = useState<SyncKindOptIn | null>(null);
  const optInEpochRef = useRef(0);
  const loadSeqRef = useRef(0);
  const kindOptInRef = useRef<SyncKindOptIn | null>(null);

  const optInPanelId = useId();
  const detailsPanelId = useId();
  const [optInOpen, setOptInOpen] = useState(false);
  const anyOptIn = !!kindOptIn && (kindOptIn.knownHost || kindOptIn.aiProfile || !!kindOptIn.aiPermission || !!kindOptIn.preferences);
  useEffect(() => {
    if (anyOptIn) setOptInOpen(true);
  }, [anyOptIn]);
  const [detailsOpen, setDetailsOpen] = useState(false);

  const load = useCallback((view: SyncKindOptIn, epoch: number) => {
    const seq = ++loadSeqRef.current;
    setRefreshBusy(true);
    setError(null);
    return loadSnapshot(dek, view)
      .then((snapshot) => {
        if (seq !== loadSeqRef.current || epoch !== optInEpochRef.current) return;
        setLocal(snapshot.local);
        setRemote(snapshot.remote);
        setCollectWarnings(snapshot.warnings);
      })
      .catch((e: unknown) => {
        if (seq === loadSeqRef.current && epoch === optInEpochRef.current) setError(describeError(e));
      })
      .finally(() => {
        if (seq === loadSeqRef.current) setRefreshBusy(false);
      });
  }, [dek]);

  useEffect(() => {
    let cancelled = false;
    const epoch = optInEpochRef.current;
    setRefreshBusy(true);
    Promise.resolve()
      .then(() => syncApi.kindOptInGet())
      .then((view) => {
        if (cancelled || epoch !== optInEpochRef.current) return;
        kindOptInRef.current = view;
        setKindOptIn(view);
        void load(view, epoch);
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(describeError(e));
      });

    const onResult = (event: Event) => {
      const result = (event as CustomEvent<WebSyncResult>).detail;
      setLocal(result.local);
      setRemote(result.remote);
      setCollectWarnings(result.warnings);
      setError(null);
    };
    const onError = (event: Event) => {
      setError(describeError((event as CustomEvent<unknown>).detail));
    };
    window.addEventListener(WEB_SYNC_RESULT_EVENT, onResult);
    window.addEventListener(WEB_SYNC_ERROR_EVENT, onError);
    return () => {
      cancelled = true;
      loadSeqRef.current += 1;
      window.removeEventListener(WEB_SYNC_RESULT_EVENT, onResult);
      window.removeEventListener(WEB_SYNC_ERROR_EVENT, onError);
    };
  }, [load]);

  const rows = useMemo(() => (local && remote ? buildRows(local, remote) : null), [local, remote]);

  const updateKindOptIn = async (patch: Partial<SyncKindOptIn>) => {
    const epoch = ++optInEpochRef.current;
    setOptInBusy(true);
    setRefreshBusy(true);
    setLocal(null);
    setRemote(null);
    setCollectWarnings([]);
    setError(null);
    try {
      const next = await syncApi.kindOptInSet(patch);
      if (epoch !== optInEpochRef.current) return;
      kindOptInRef.current = next;
      setKindOptIn(next);
      await load(next, epoch);
      window.dispatchEvent(new Event(WEB_SYNC_REQUEST_EVENT));
    } catch (e) {
      if (epoch === optInEpochRef.current) {
        setError(describeError(e));
        const current = kindOptInRef.current;
        if (current) await load(current, epoch);
      }
    } finally {
      if (epoch === optInEpochRef.current) setOptInBusy(false);
    }
  };

  const winners = useMemo(() => {
    if (!local || !remote) return [];
    return computeWinners(local, remote);
  }, [local, remote]);

  const applySet = useMemo(() => {
    if (!local || !remote) return [];
    return computeApplySet(local, remote);
  }, [local, remote]);


  const pushCount = winners.length;
  const sameCount = rows?.filter((r) => r.state === "same").length ?? 0;
  const detailsSummary = !rows
    ? "读取中…"
    : rows.length === 0
      ? "本机与云端都还没有可同步的内容"
      : `共 ${rows.length} 条 · 一致 ${sameCount} 条` +
        (sameCount < rows.length ? ` · 差异 ${rows.length - sameCount} 条` : "");

  return (
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <IconServer size={15} className="text-neutral-400" />
        <span className="nx-card-title">账号同步</span>
        <span className="nx-badge nx-badge-green">自动同步</span>
        <span className="nx-hint">云端 {remote?.length ?? "…"} 个对象</span>
        <div className="nx-spacer" />
        <span className="nx-hint">每 60 秒</span>
      </div>

      <p className="nx-hint mb-3">
        登录并解锁数据密钥后，系统会先拉取并合并云端内容，再推送本机变更。
        {refreshBusy ? "正在读取本机与云端数据…" : `当前待应用 ${applySet.length} 项，待推送 ${pushCount} 项。`}
      </p>

      {kindOptIn && (
        <div className="mb-3">
          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              className="flex items-center gap-1.5 text-[12px] text-neutral-400 transition-colors hover:text-neutral-100"
              aria-expanded={optInOpen}
              aria-controls={optInPanelId}
              onClick={() => setOptInOpen((v) => !v)}
            >
              {optInOpen ? (
                <IconChevronDown size={12} className="shrink-0" />
              ) : (
                <IconChevronRight size={12} className="shrink-0" />
              )}
              同步内容开关
            </button>
            <span className="nx-hint text-[11px]">
              已知主机 {kindOptIn.knownHost ? "已开启" : "已关闭"} · AI 模型档案 {kindOptIn.aiProfile ? "已开启" : "已关闭"} · AI 权限 {kindOptIn.aiPermission ? "已开启" : "已关闭"} · 外观快捷键 {kindOptIn.preferences ? "已开启" : "已关闭"}
            </span>
          </div>
          {optInOpen && (
            <div id={optInPanelId} className="mt-2 flex flex-col gap-2">
              <label className="flex items-start gap-2">
                <input
                  type="checkbox"
                  className="mt-0.5 h-4 w-4 shrink-0"
                  checked={kindOptIn.knownHost}
                  disabled={optInBusy}
                  onChange={(e) => void updateKindOptIn({ knownHost: e.target.checked })}
                />
                <span className="text-[12px] text-neutral-300">
                  同步已知主机（主机信任）
                  <span className="nx-hint block">默认开启。关闭后不再同步主机信任，也不会删除云端已有副本。</span>
                </span>
              </label>
              <label className="flex items-start gap-2">
                <input
                  type="checkbox"
                  className="mt-0.5 h-4 w-4 shrink-0"
                  checked={kindOptIn.aiProfile}
                  disabled={optInBusy}
                  onChange={(e) => void updateKindOptIn({ aiProfile: e.target.checked })}
                />
                <span className="text-[12px] text-neutral-300">
                  同步 AI 模型档案
                  <span className="nx-hint block">默认开启。关闭后不再同步模型档案；API 密钥仅在凭据库解锁时随档案同步。</span>
                </span>
              </label>
              <label className="flex items-start gap-2">
                <input
                  type="checkbox"
                  className="mt-0.5 h-4 w-4 shrink-0"
                  checked={!!kindOptIn.aiPermission}
                  disabled={optInBusy}
                  onChange={(e) => void updateKindOptIn({ aiPermission: e.target.checked })}
                />
                <span className="text-[12px] text-neutral-300">
                  同步 AI 权限与拦截规则
                  <span className="nx-hint block">默认开启。关闭后不再同步全局权限模式和危险命令拦截规则。</span>
                </span>
              </label>
              <label className="flex items-start gap-2">
                <input
                  type="checkbox"
                  className="mt-0.5 h-4 w-4 shrink-0"
                  checked={!!kindOptIn.preferences}
                  disabled={optInBusy}
                  onChange={(e) => void updateKindOptIn({ preferences: e.target.checked })}
                />
                <span className="text-[12px] text-neutral-300">
                  同步外观与快捷键
                  <span className="nx-hint block">默认开启。关闭后不再同步界面外观和快捷键覆盖；Cron 任务不同步。</span>
                </span>
              </label>
            </div>
          )}
        </div>
      )}

      {error && (
        <div className="nx-alert nx-alert-danger mb-3 flex items-start gap-2">
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words">自动同步失败：{error}，下一分钟自动重试。</span>
        </div>
      )}

      {collectWarnings.length > 0 && (
        <div className="nx-alert nx-alert-info mb-3 flex items-start gap-2">
          <IconInfo size={13} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1 break-words text-amber-300">{collectWarnings.join(";")}</span>
        </div>
      )}

      <div className="mb-1">
        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            className="flex items-center gap-1.5 text-[12px] text-neutral-400 transition-colors hover:text-neutral-100"
            aria-expanded={detailsOpen}
            aria-controls={detailsPanelId}
            onClick={() => setDetailsOpen((v) => !v)}
          >
            {detailsOpen ? (
              <IconChevronDown size={12} className="shrink-0" />
            ) : (
              <IconChevronRight size={12} className="shrink-0" />
            )}
            对比详情
          </button>
          <span className="nx-hint text-[11px]">{detailsSummary}</span>
        </div>
        {detailsOpen && (
          <div id={detailsPanelId} className="mt-2">
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
            {!rows && !error && <div className="nx-hint py-3 text-[12px]">正在读取本机与云端数据…</div>}
          </div>
        )}
      </div>

      <div className="nx-alert nx-alert-info mt-3 flex items-start gap-2">
        <IconInfo size={14} className="mt-0.5 shrink-0" />
        <div>
          会话记录（终端录像）默认不同步，可在对应位置单独开启；已知主机和 AI 模型档案默认同步，可关闭；关闭开关不会删除云端已有副本。冲突按最后修改时间裁决，请保持各设备时钟准确，否则「较新」判定可能不符合预期。
        </div>
      </div>
    </section>
  );
}
