
const SENSITIVE_KEY = /(pass|secret|token|key|credential|auth|pwd|private)/i;

const SENSITIVE_FILE =
 /(^\.env)|(\.(pem|key|p12|pfx|jks|keystore|kubeconfig)$)|(^(id_rsa|id_dsa|id_ecdsa|id_ed25519)(\.|$))|(credential|secret|token|password|passwd)/i;

export const REDACTED_MARK = "•••（已遮蔽）";

export function isSensitiveName(name: string): boolean {
  return SENSITIVE_KEY.test(name);
}

export function isSensitiveFileName(name: string): boolean {
  return SENSITIVE_FILE.test(name.trim());
}

export function splitEnvEntry(entry: string): { name: string; value: string } {
  const i = entry.indexOf("=");
  if (i < 0) return { name: entry, value: "" };
  return { name: entry.slice(0, i), value: entry.slice(i + 1) };
}

export function isSensitiveEnvEntry(entry: string): boolean {
  return isSensitiveName(splitEnvEntry(entry).name);
}

export function redactMountName(name: string): string {
  return name && isSensitiveName(name) ? REDACTED_MARK : name;
}

export function redactMountSource(source: string): string {
  if (!source) return source;
  return source
    .split("/")
    .map((seg) => (seg && isSensitiveName(seg) ? REDACTED_MARK : seg))
    .join("/");
}

export function redactBindEntry(entry: string): string {
  const parts = entry.split(":");
  if (parts.length < 2) return redactMountSource(entry);
  return [redactMountSource(parts[0]), ...parts.slice(1)].join(":");
}

export function redactFileName(name: string): string {
  if (name.startsWith(".")) return REDACTED_MARK;
  const dot = name.lastIndexOf(".");
  if (dot <= 0) return REDACTED_MARK;
  const ext = name.slice(dot);
  return isSensitiveFileName(ext) ? REDACTED_MARK : `${REDACTED_MARK}${ext}`;
}

export function redactContainerPath(path: string): string {
  return path
    .split("/")
    .map((seg) => (seg && isSensitiveFileName(seg) ? redactFileName(seg) : seg))
    .join("/");
}

export function redactPathInText(text: string, rawPath: string): string {
  if (!rawPath || rawPath === "/") return text;
  return text.split(rawPath).join(redactContainerPath(rawPath));
}

export function redactInspectTree(value: unknown): unknown {
  return walk(value, undefined);
}

function walk(value: unknown, keyHint: string | undefined): unknown {
  if (keyHint && isSensitiveName(keyHint)) return REDACTED_MARK;

  if (Array.isArray(value)) {
    if (keyHint === "Env") {
      return value.map((entry) => {
        if (typeof entry !== "string") return walk(entry, undefined);
        const { name } = splitEnvEntry(entry);
        return isSensitiveName(name) ? `${name}=${REDACTED_MARK}` : entry;
      });
    }
    if (keyHint === "Binds") {
      return value.map((entry) =>
        typeof entry === "string" ? redactBindEntry(entry) : walk(entry, undefined),
      );
    }
    return value.map((item) => walk(item, keyHint ? `${keyHint}[]` : undefined));
  }

  if (value && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      if (keyHint === "Mounts[]" && typeof v === "string") {
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

export interface InspectEnvRow {
  key: string;
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
  json: string;
}

export function buildInspectViewModel(raw: unknown): InspectViewModel {
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

export function pickInspectString(root: unknown, path: string): string {
  return readPath(root, path) as string;
}

export function pickInspectNumber(root: unknown, path: string): string {
  const node = readPath(root, path, true);
  return typeof node === "number" ? String(node) : "";
}

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
