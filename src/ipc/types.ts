export type JsonValue =
  | string
  | number
  | boolean
  | null
  | JsonValue[]
  | { [key: string]: JsonValue };

export type AiScopeDto = { sessionId: string | null, tabId: string | null, connId: string | null, assetId: string | null, };

export type AppErrorDto = { code: string, message: string, detail: JsonValue | null, };

export type AssetDto = { id: string, groupId: string | null, kind: string, name: string, host: string | null, port: number | null, username: string | null, authKind: string | null, keyPath: string | null, credId: string | null, options: JsonValue, tags: string, note: string, sort: number, createdAt: number, updatedAt: number, deletedAt: number | null, 
/**
 * 内置资产（应用自带的「当前设备」）：前端据此隐藏删除、锁定类型。
 */
builtin: boolean, };

export type AssetGroupDto = { id: string, parentId: string | null, name: string, sort: number, createdAt: number, updatedAt: number, };

export type AuditEntryDto = { id: number, ts: number, sessionId: string | null, assetId: string | null, 
/**
 * user | ai
 */
source: string, kind: string, payload: JsonValue, exitCode: number | null, durationMs: number | null, };

export type ContainerSummaryDto = { id: string, name: string, image: string, state: string, status: string, ports: string, composeProject: string | null, };

export type ConversationDto = { id: string, title: string, scope: JsonValue, createdAt: number, updatedAt: number, };

export type CredentialMetaDto = { id: string, name: string, kind: string, cipher: string, kekHint: string, createdAt: number, updatedAt: number, };

export type DigestEntry = { id: string, name: string, kind: string, host: string | null, username: string | null, updatedAt: number, deletedAt: number | null, 
/**
 * 引用了凭据（界面据此提示「这条带密码」）。
 */
hasCred: boolean, groupId: string | null, };

export type FileEntryDto = { name: string, path: string, 
/**
 * dir | file | symlink | other
 */
kind: string, size: number, mode: string, owner: string | null, group: string | null, mtime: number, symlinkTarget: string | null, };

/**
 * 转发能力自述（`forward_env` 的返回值）。
 *
 * 界面据此决定三件事：要不要显示「本平台不支持」的提示条、把创建按钮禁掉、
 * 以及转发地址该显示成 `127.0.0.1` 还是 `0.0.0.0`。
 *
 * 这些判断**不能放前端**：它们取决于「跑在哪种运行形态、部署在哪个平台」，
 * 是内核（编译期形态 + `NEXTERM_PLATFORM`）才知道的事实。前端自己猜会给出错误承诺
 * —— 比如在懒猫上画出「外部可访问 http://…:13306」而实际永远连不上。
 */
export type ForwardEnvDto = { 
/**
 * 本平台是否允许端口转发。懒猫微服上为 `false`。
 */
available: boolean, 
/**
 * 部署平台标识：`lazycat` / `other`。
 */
platform: string, 
/**
 * 转发监听地址。桌面形态是 `127.0.0.1`；服务端形态是 `0.0.0.0`。
 */
listenHost: string, };

export type ForwardSpecDto = { id: string, sessionId: string, listenPort: number, 
/**
 * 静态转发的目标；SOCKS5 动态转发没有固定目标 → `null`。
 */
targetHost: string | null, targetPort: number | null, 
/**
 * `local` 或 `socks`。
 */
kind: string, createdAt: number, };

export type ImageSummaryDto = { id: string, repository: string, tag: string, size: string, createdSince: string, };

/**
 * 导入结果。界面拿它报告「建了几条、更新了几条、什么被跳过了」。
 */
export type ImportReport = { groupsCreated: number, groupsUpdated: number, assetsCreated: number, assetsUpdated: number, credsCreated: number, credsUpdated: number, 
/**
 * 因为「本地这份更新」而跳过的条数（未开强制覆盖时）。
 */
skippedNewer: number, 
/**
 * 被拒的条数（内置资产、名称非法等）。
 */
refused: number, 
/**
 * 人话警告。界面必须展示 —— 这里的每一条都对应一个「用户以为同步了，
 * 其实没有」的坑（引用的私钥路径失效、凭据没跟过来、分组不存在…）。
 */
warnings: Array<string>, };

export type KnownHostDto = { id: string, host: string, port: number, keyType: string, fingerprint: string, addedAt: number, };

export type MessageDto = { id: string, conversationId: string, role: string, content: JsonValue, tokensIn: number | null, tokensOut: number | null, createdAt: number, };

/**
 * 一份模型档案（BYOK）。
 *
 * 字段与 `ProviderConfig` 一一对应，外加一个面向用户的展示名 `name` 与档案 `id`。
 */
export type ModelProfile = { 
/**
 * ULID。新建时传空串，由 [`ModelProfileStore::upsert`] 分配。
 */
id: string, 
/**
 * 展示名（下拉里显示的就是它）。
 */
name: string, baseUrl: string, apiKey: string, model: string, temperature: number, 
/**
 * 上下文窗口（1k–2M，非法值兜底 —— 外部配置可能乱填）。
 */
contextWindow: number, 
/**
 * None = 跟随系统代理（与 SSH/WinRM 策略相反，§8.7）。
 */
proxy: string | null, stream: boolean,
/**
 * 回退模型（可选）：主模型调用失败时改用的模型名。
 *
 * 三态语义，保存方必须区分「清除」与「别动它」：
 *   · 字符串 = 设置回退模型；
 *   · `null` = **明确清除**（不使用回退）；
 *   · 字段缺失（undefined）= 旧数据 / 旧内核没有这一项 —— 整包保存时必须
 *     原样带回读到的值，不得因为表单没碰过它就当成 `null` 清掉。
 */
fallbackModel?: string | null, };

/**
 * 档案表：全部档案 + 当前激活项的 id。
 *
 * 直接作为 `ai_model_profiles` 的返回值给前端，省得前端拉两次。
 */
export type ModelProfilesView = { profiles: Array<ModelProfile>, activeId: string | null, };

export type MountEntryDto = { id: string, localPoint: string, remote: string, sessionId: string | null, createdAt: number | null, };

export type ProviderConfigDto = { baseUrl: string, apiKey: string, model: string, temperature: number, contextWindow: number, proxy: string | null, stream: boolean,
/**
 * 回退模型（可选）：主模型调用失败时改用的模型名。
 * 三态语义与 [`ModelProfile`] 相同：字符串 = 设置；`null` = 明确清除；
 * 缺失 = 没有这一项，保存与展示无关字段时不得顺带清掉。
 */
fallbackModel?: string | null, };

export type QueryResultDto = { columns: Array<string>, rows: Array<Array<JsonValue>>, rowsAffected: number, durationMs: number, truncated: boolean, error: string | null, };

export type RedisKeyViewDto = { key: string, keyType: string, ttl: number, value: JsonValue, };

export type ScreenSnapshotDto = { text: string, lines: Array<string>, cursorRow: number, cursorCol: number, cols: number, rows: number, altScreen: boolean, lastOutputMsAgo: number, };

export type SessionInfoDto = { id: string, assetId: string | null, name: string, kind: string, 
/**
 * connecting | connected | reconnecting | disconnected | failed
 */
status: string, tabs: Array<string>, createdAt: number, };

/**
 * 会话状态事件（`session://status`）。
 *
 * `version` 是会话内单调递增的事件版本号（内核 `Session.eventVersion`）：
 * 重连/重放可能把旧状态事件重新推到前端，按版本号比较即可丢弃乱序的旧事件，
 * 不需要按内容猜测。
 */
export type SessionStatusEvent = { sessionId: string, status: string, error: string | null, version: number, };

export type SnippetDto = { id: string, groupId: string | null, name: string, body: string, sort: number, createdAt: number, updatedAt: number, };

/**
 * 本机摘要：给界面做「哪边有、哪边新」的对比，**不含任何密文**。
 */
export type SyncDigest = { origin: string, protocol: number, appVersion: string, 
/**
 * 本实例是桌面版还是服务端（界面据此区分「本机 / 对端」）。
 */
desktop: boolean, assets: Array<DigestEntry>, };

/**
 * 出站连接配置。
 *
 * ⚠️ `token` 是**明文存在本地库**的（和凭据库分开）。这是刻意的取舍：它要能
 * 在用户没解锁凭据库时也把「上次连的那个盒子」显示出来；换成密文就得先解锁
 * 才能看一眼设置页。风险面在界面上明说（密码框 + 文案）。
 */
export type SyncLink = { 
/**
 * 对端服务端地址，如 `https://nexterm.heiyu.space`（微服）或
 * `https://sync.example.com`（自建）。
 */
url: string, 
/**
 * 对端服务端装在哪儿：`box`（懒猫微服）| `server`（自建服务器）。
 *
 * **只影响界面提示，不影响协议** —— 两个位置发的是同一个头、同一套校验。
 * 留着它是因为「令牌去哪儿拿」这件事两者完全不同，而这正是用户最容易卡住的地方。
 */
tokenKind: string, 
/**
 * 服务端自己生成的那串令牌。
 */
token: string, 
/**
 * 跳过 TLS 证书校验（自签证书的内网/自建地址才需要）。
 */
insecure: boolean, 
/**
 * 最近一次连通性探测成功的时间（Unix 毫秒；0 = 没测过）
 */
verifiedAt: number, 
/**
 * 最近一次探测的失败原因（成功时为空）
 */
lastError: string | null, };

/**
 * 终端退出事件（`terminal://exit`）。`version` 同 `SessionStatusEvent`：
 * 标签内单调递增，用于识别重连重放的旧事件。
 */
export type TerminalExitEvent = { tabId: string, exitCode: number | null, version: number, };

export type VaultStatusDto = { initialized: boolean, 
/**
 * not_init | dpapi | master
 */
mode: string, unlocked: boolean, autoLockMinutes: number, };
