export type JsonValue =
  | string
  | number
  | boolean
  | null
  | JsonValue[]
  | { [key: string]: JsonValue };

export type AiScopeDto = { sessionId: string | null, tabId: string | null, connId: string | null, assetId: string | null, };

export type AiHitlEventDto = { runId: string, checkpointId: string, requestId?: string, 
kind: string, 
reason: string, message?: string, attempt: number, seq: number, };

export type AiHitlInterruptDto = { id: string, runId: string, checkpointId: string, checkpointHash: string, targetId: string, callId: string, tool: string, 
kind: string, parameters: JsonValue, parameterHash: string, question?: AiHitlQuestionDto, nonce: string, 
createdAt: string, expiresAt: string, attempt: number, seq: number, };

export type AiHitlQuestionDto = { id: string, text: string, options?: Array<string>, };

export type AiHitlSnapshotDto = { runId: string, checkpointId: string, 
status: string, attempt: number, seq: number, pending: Array<AiHitlInterruptDto>, terminal?: AiHitlEventDto, };

export type AiRunDto = { id: string, conversationId: string, status: string, attempt: number, seq: number,
planMode: boolean, source: string, profileId?: string, answer: string, turns: number, tokensIn: number, tokensOut: number,
cacheCreationTokens: number, latencyMs: number, retries: number, failures: number, error?: string,
createdAt: number, updatedAt: number, finishedAt?: number, };

export type AiRunEventDto = Record<string, unknown>;

export type AppErrorDto = { code: string, message: string, detail: JsonValue | null, };

export type AssetDto = { id: string, groupId: string | null, kind: string, name: string, host: string | null, port: number | null, username: string | null, authKind: string | null, keyPath: string | null, credId: string | null, options: JsonValue, tags: string, note: string, sort: number, createdAt: number, updatedAt: number, deletedAt: number | null, 
builtin: boolean, };

export type AssetGroupDto = { id: string, parentId: string | null, name: string, sort: number, createdAt: number, updatedAt: number, };

export type AuditEntryDto = { id: number, ts: number, sessionId: string | null, assetId: string | null, 
source: string, kind: string, payload: JsonValue, exitCode: number | null, durationMs: number | null, };

export type ContainerSummaryDto = { id: string, name: string, image: string, state: string, status: string, ports: string, composeProject: string | null, };

export type ConversationDto = { id: string, title: string, scope: JsonValue, createdAt: number, updatedAt: number, };

export type CredentialMetaDto = { id: string, name: string, kind: string, cipher: string, kekHint: string, createdAt: number, updatedAt: number, };

export type DigestEntry = { id: string, name: string, kind: string, host: string | null, username: string | null, updatedAt: number, deletedAt: number | null, 
hasCred: boolean, groupId: string | null, };

export type FileEntryDto = { name: string, path: string, 
kind: string, size: number, mode: string, owner: string | null, group: string | null, mtime: number, symlinkTarget: string | null, };

export type ForwardEnvDto = { 
available: boolean, 
platform: string, 
listenHost: string, };

export type ForwardSpecDto = { id: string, sessionId: string, listenPort: number, 
targetHost: string | null, targetPort: number | null, 
kind: string, createdAt: number, };

export type ImageSummaryDto = { id: string, repository: string, tag: string, size: string, createdSince: string, };

export type SkippedNewerEntry = { kind: string, id: string, name: string, 
localRevision: number, remoteRevision: number, equalRevision: boolean, };

export type ImportReport = { groupsCreated: number, groupsUpdated: number, assetsCreated: number, assetsUpdated: number, credsCreated: number, credsUpdated: number,
credsDeleted: number,
snippetsCreated: number, snippetsUpdated: number,
skippedNewer: number,
skippedNewerDetails?: Array<SkippedNewerEntry>,
refused: number,
warnings: Array<string>, };

export type KnownHostDto = { id: string, host: string, port: number, keyType: string, fingerprint: string, addedAt: number, };

export type SshHostKeyProbeDto = { host: string, port: number, keyType: string, fingerprint: string, state: "known" | "changed" | "pending", known?: Array<{ keyType: string, fingerprint: string }>, };

export type AssetProbeResultDto = { assetId: string, reachable: boolean, kind?: string, error?: string, durationMs: number, };

export type AssetProbeBatchDto = { results: Array<AssetProbeResultDto>, };

export type MessageDto = { id: string, conversationId: string, role: string, content: JsonValue, tokensIn: number | null, tokensOut: number | null, createdAt: number, };

export type ModelProfile = { 
id: string, 
name: string, baseUrl: string, apiKey: string, model: string, temperature: number, 
contextWindow: number, 
proxy: string | null, stream: boolean,
fallbackModel?: string | null,
maxTokens?: number | null,
requestTimeoutSeconds?: number | null, idleTimeoutSeconds?: number | null,
circuitFailureThreshold?: number | null, circuitCooldownSeconds?: number | null, };

export type ModelListDto = { models: Array<string>, malformed: number, };

export type ProviderTestResult = { modelsOk: boolean, modelsError: string | null, chatOk: boolean, chatError: string | null, };

export type ModelProfilesView = { profiles: Array<ModelProfile>, activeId: string | null, };

export type AiUsageSummaryRow = { source: string, profileId: string, runs: number, tokensIn: number, tokensOut: number, cacheCreationTokens: number, averageLatencyMs: number, };

export type AiCircuitStatusDto = { consecutiveFailures: number, openUntil: number | null, };

export type MountEntryDto = { id: string, localPoint: string, remote: string, sessionId: string | null, createdAt: number | null, };

export type ProviderConfigDto = { baseUrl: string, apiKey: string, model: string, temperature: number, contextWindow: number, proxy: string | null, stream: boolean,
fallbackModel?: string | null,
maxTokens?: number | null, };

export type QueryResultDto = { columns: Array<string>, rows: Array<Array<JsonValue>>, rowsAffected: number, durationMs: number, truncated: boolean, error: string | null, };

export type RedisKeyViewDto = { key: string, keyType: string, ttl: number, value: JsonValue, };

export type ScreenSnapshotDto = { text: string, lines: Array<string>, cursorRow: number, cursorCol: number, cols: number, rows: number, altScreen: boolean, lastOutputMsAgo: number, };

export type SessionInfoDto = { id: string, assetId: string | null, name: string, kind: string, 
status: string, tabs: Array<string>, createdAt: number, };

export type SessionStatusEvent = { sessionId: string, status: string, error: string | null, version: number, };

export type SnippetDto = { id: string, groupId: string | null, name: string, body: string, sort: number, createdAt: number, updatedAt: number, };

export type SyncDigest = { origin: string, protocol: number, appVersion: string, 
desktop: boolean, assets: Array<DigestEntry>, };

export type SyncBundleGroup = { id: string, parentId: string | null, name: string, sort: number, createdAt: number, updatedAt: number, };

export type SyncBundleAsset = { id: string, groupId: string | null, kind: string, name: string, host: string | null, port: number | null, username: string | null, authKind: string | null, keyPath: string | null, credId: string | null, optionsJson: string, tags: string, note: string, sort: number, createdAt: number, updatedAt: number, deletedAt: number | null, };

export type SyncBundleCredential = { id: string, name: string, kind: string, secret: string, updatedAt?: number, };

export type SyncCredentialTombstone = { id: string, deletedAt: number, };

export type SyncBundleSnippet = { id: string, groupId: string | null, name: string, body: string, sort: number, createdAt: number, updatedAt: number, };

export type SyncBundle = { protocol: number, origin: string, exportedAt: number, groups: Array<SyncBundleGroup>, assets: Array<SyncBundleAsset>, creds: Array<SyncBundleCredential>, credTombstones?: Array<SyncCredentialTombstone>, snippets?: Array<SyncBundleSnippet>, warnings?: Array<string>, };

export type SyncLink = { 
url: string, 
tokenKind: string, 
token: string, 
insecure: boolean, 
verifiedAt: number, 
lastError: string | null, };

export type SyncTokenDto = { id: string, clientId: string, purpose: string, createdAt: number, expiresAt: number, revokedAt: number | null, lastUsedAt: number | null, };

export type SyncTokenIssueResult = { token: SyncTokenDto, secret: string, };

export type SyncBundleWriteResult = { encrypted: boolean, warning?: string, };

export type AuditCountDto = { total: number, };

export type TerminalExitEvent = { tabId: string, exitCode: number | null, version: number, };

export type VaultStatusDto = { initialized: boolean, 
mode: string, unlocked: boolean, autoLockMinutes: number, };
