// Docker 洞察视图的**展示级**脱敏与脱敏 view model。
//
// 边界口径（review r1 P1）：当前 RPC 契约（Go SDK / CLI 回退 / 旧 Rust CLI）返回的
// 都是**未脱敏的原始 inspect**，内核没有 reveal 契约，所以前端不提供任何
// 「显示真实值」的路径 —— DOM 只消费 `buildInspectViewModel` 产出的脱敏
// view model。若未来确需 reveal，应由内核提供独立、明确授权/审计的契约，
// 而不是切换已缓存 raw JSON 的展示状态。
//
// 这里做的只是「不在界面上明文渲染敏感值」。真正的所有权校验与强制遮蔽必须在
// 内核 RPC 边界完成（session → transport → container 归属、审计），本模块既不
// 构成授权，也不能替代内核遮蔽。

/** 命中即遮蔽的键名（小写匹配）。env 名、label 键、路径段、卷名共用。 */
const SENSITIVE_KEY = /(pass|secret|token|key|credential|auth|pwd|private)/i;

/** 命中即按敏感处理的容器内文件名（只列目录，本就没有读取内容的 RPC）。 */
const SENSITIVE_FILE =
 /(^\.env)|(\.(pem|key|p12|pfx|jks|keystore|kubeconfig)$)|(^(id_rsa|id_dsa|id_ecdsa|id_ed25519)(\.|$))|(credential|secret|token|password|passwd)/i;

/** 遮蔽占位符。用固定文案而不是星号长度，避免按长度猜原值。 */
export const REDACTED_MARK = "•••（已遮蔽）";

export function isSensitiveName(name: string): boolean {
  return SENSITIVE_KEY.test(name);
}

export function isSensitiveFileName(name: string): boolean {
  return SENSITIVE_FILE.test(name.trim());
}

/** `K=V` 形式 env 项拆键值；没有 `=` 的按无值处理（键本身可能敏感）。 */
export function splitEnvEntry(entry: string): { name: string; value: string } {
  const i = entry.indexOf("=");
  if (i < 0) return { name: entry, value: "" };
  return { name: entry.slice(0, i), value: entry.slice(i + 1) };
}

export function isSensitiveEnvEntry(entry: string): boolean {
  return isSensitiveName(splitEnvEntry(entry).name);
}

/** 卷名遮蔽：docker named-volume 的名字（如 db-passwords）可能带敏感词。 */
export function redactMountName(name: string): string {
  return name && isSensitiveName(name) ? REDACTED_MARK : name;
}

/**
 * 挂载源路径遮蔽：逐段判敏感，只遮敏感段，保留可排查的目录结构。
 * 真实 named-volume 的 Source 形如 `/var/lib/docker/volumes/<卷名>/_data`，
 * 只遮 Name 不遮 Source 等于没遮 —— 卷名会从路径里原样漏出来（review r1 P2）。
 */
export function redactMountSource(source: string): string {
  if (!source) return source;
  return source
    .split("/")
    .map((seg) => (seg && isSensitiveName(seg) ? REDACTED_MARK : seg))
    .join("/");
}

/**
 * 敏感文件名的遮蔽显示值。**导航用原始值，DOM 只用这个显示值**（review r1 P2）：
 * 文本、title、面包屑一律用本函数的输出，原始文件名不上屏。
 * 保留非敏感的扩展名（如 `.yaml`）以便区分文件类型；扩展名本身敏感
 * （`.key` / `.pem`）或点文件（`.env.production`）时整体遮蔽。
 */
export function redactFileName(name: string): string {
  if (name.startsWith(".")) return REDACTED_MARK;
  const dot = name.lastIndexOf(".");
  if (dot <= 0) return REDACTED_MARK;
  const ext = name.slice(dot);
  return isSensitiveFileName(ext) ? REDACTED_MARK : `${REDACTED_MARK}${ext}`;
}

/**
 * 完整路径的逐段脱敏显示（review r2 P2）：任何一段敏感都只遮那一段，结构保留。
 * 所有 title / 导航展示 / 错误回显一律用本函数的输出；原始路径只保留在
 * setPath / RPC 参数 / query key 等导航内部。非敏感后代的完整路径也走这里 ——
 * 否则 `/secrets/app` 里的 `secrets` 会从 title 上屏。
 */
export function redactContainerPath(path: string): string {
  return path
    .split("/")
    .map((seg) => (seg && isSensitiveFileName(seg) ? redactFileName(seg) : seg))
    .join("/");
}

/**
 * 错误回显脱敏：后端 `ls` stderr 会把请求路径原样包进错误（如
 * `ls: /secrets/app: Permission denied`）。把文本里的原始路径替换成逐段脱敏
 * 显示路径 —— 前缀替换同时盖住「错误里带了更深层子路径」的形态。
 */
export function redactPathInText(text: string, rawPath: string): string {
  if (!rawPath || rawPath === "/") return text;
  return text.split(rawPath).join(redactContainerPath(rawPath));
}

/**
 * 递归脱敏 inspect JSON（`docker inspect` 的原始结构：对象或单元素数组）。
 *
 * 规则：
 *  - 对象键命中 SENSITIVE_KEY → 整棵子树替换为 REDACTED_MARK；
 *  - `Env` 数组（`Config.Env` 的 `K=V` 字符串）→ 键敏感时值遮蔽；
 *  - `Mounts[]` 的 `Name`（卷名）与 `Source`（路径段）按值/段遮蔽；
 *  - 其余原样保留 —— 用户排查问题需要看到真实结构，只遮值不遮形。
 */
export function redactInspectTree(value: unknown): unknown {
  return walk(value, undefined);
}

function walk(value: unknown, keyHint: string | undefined): unknown {
  if (keyHint && isSensitiveName(keyHint)) return REDACTED_MARK;

  if (Array.isArray(value)) {
    // Env 是 string[]（K=V），逐条按键名遮蔽；其余数组递归。
    if (keyHint === "Env") {
      return value.map((entry) => {
        if (typeof entry !== "string") return walk(entry, undefined);
        const { name } = splitEnvEntry(entry);
        return isSensitiveName(name) ? `${name}=${REDACTED_MARK}` : entry;
      });
    }
    // 数组元素带一个 `键[]` 提示，供对象分支做容器内判断（见下）。
    return value.map((item) => walk(item, keyHint ? `${keyHint}[]` : undefined));
  }

  if (value && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      if (keyHint === "Mounts[]" && typeof v === "string") {
        // 卷名与源路径按值/段补刀：`Name` / `Source` 键名本身不敏感，
        // 键值规则盖不到它们携带的敏感卷名（如 db-passwords）。
        if (k === "Name") {
          out[k] = redactMountName(v);
          continue;
        }
        if (k === "Source") {
          out[k] = redactMountSource(v);
          continue;
        }
      }
      out[k] = walk(v, k);
    }
    return out;
  }

  return value;
}

// ───────── 脱敏 view model（DOM 的唯一数据源） ─────────

export interface InspectEnvRow {
  key: string;
  /** 已脱敏：敏感键的值是 REDACTED_MARK。 */
  value: string;
}

export interface InspectLabelRow {
  key: string;
  value: string;
}

export interface InspectMountRow {
  type: string;
  name: string;
  source: string;
  destination: string;
  /** docker inspect 的 Mounts[].RW 是 boolean（兼容字符串形态）。 */
  rw: boolean;
}

export interface InspectViewModel {
  name: string;
  image: string;
  state: string;
  startedAt: string;
  restartCount: string;
  env: InspectEnvRow[];
  labels: InspectLabelRow[];
  mounts: InspectMountRow[];
  /** 完整配置的脱敏 JSON 文本（已脱敏树序列化）。 */
  json: string;
}

/**
 * 从原始 inspect JSON 构建脱敏 view model。组件只允许渲染本函数的输出 ——
 * 原始 JSON 不出这个模块（review r1 P1）。
 */
export function buildInspectViewModel(raw: unknown): InspectViewModel {
  // docker CLI 的 inspect 输出是单元素数组，moby SDK 返回单个对象，两种都接。
  const root = Array.isArray(raw) ? raw[0] : raw;

  const env: InspectEnvRow[] = readStringArray(root, "Config.Env").map((entry) => {
    const { name, value } = splitEnvEntry(entry);
    return { key: name, value: isSensitiveName(name) ? REDACTED_MARK : value };
  });

  const labels: InspectLabelRow[] = Object.entries(readStringRecord(root, "Config.Labels")).map(
    ([key, value]) => ({ key, value: isSensitiveName(key) ? REDACTED_MARK : value }),
  );

  const mounts: InspectMountRow[] = readObjectArray(root, "Mounts").map((m) => ({
    type: pickInspectString(m, "Type"),
    name: redactMountName(pickInspectString(m, "Name")),
    source: redactMountSource(pickInspectString(m, "Source")),
    destination: pickInspectString(m, "Destination"),
    rw: pickInspectBool(m, "RW") ?? false,
  }));

  return {
    name: pickInspectString(root, "Name").replace(/^\//, ""),
    image: pickInspectString(root, "Config.Image"),
    state: pickInspectString(root, "State.Status"),
    startedAt: pickInspectString(root, "State.StartedAt"),
    restartCount: pickInspectNumber(root, "State.RestartCount"),
    env,
    labels,
    mounts,
    json: JSON.stringify(redactInspectTree(raw), null, 2),
  };
}

/** 从 inspect JSON 里防御性取字符串字段（路径用 `.` 分隔）。 */
export function pickInspectString(root: unknown, path: string): string {
  return readPath(root, path) as string;
}

/** 同上，取数字并渲染成字符串；取不到返回 ""。 */
export function pickInspectNumber(root: unknown, path: string): string {
  const node = readPath(root, path, true);
  return typeof node === "number" ? String(node) : "";
}

/** 同上，取 boolean（真实契约是 boolean；容忍 "true"/"false" 字符串）。 */
export function pickInspectBool(root: unknown, path: string): boolean | null {
  const node = readPath(root, path, true);
  if (node === true || node === "true") return true;
  if (node === false || node === "false") return false;
  return null;
}

function readPath(root: unknown, path: string, rawValue = false): unknown {
  let node: unknown = root;
  for (const part of path.split(".")) {
    if (!node || typeof node !== "object") return rawValue ? undefined : "";
    node = (node as Record<string, unknown>)[part];
  }
  return rawValue ? node : typeof node === "string" ? node : "";
}

function readStringArray(root: unknown, path: string): string[] {
  const node = readPath(root, path, true);
  return Array.isArray(node) ? node.filter((v): v is string => typeof v === "string") : [];
}

function readStringRecord(root: unknown, path: string): Record<string, string> {
  const node = readPath(root, path, true);
  if (!node || typeof node !== "object" || Array.isArray(node)) return {};
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(node as Record<string, unknown>)) {
    if (typeof v === "string") out[k] = v;
  }
  return out;
}

function readObjectArray(root: unknown, path: string): Record<string, unknown>[] {
  const node = readPath(root, path, true);
  if (!Array.isArray(node)) return [];
  return node.filter(
    (v): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v),
  );
}
