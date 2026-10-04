
export const HOME = "~";

export function norm(path: string): string {
  return path.replace(/\\/g, "/");
}

export function isRoot(path: string): boolean {
  const p = norm(path);
  return p === "/" || /^[A-Za-z]:\/$/.test(p);
}

export function joinPath(dir: string, name: string): string {
  const base = norm(dir);
  return base.endsWith("/") ? base + name : `${base}/${name}`;
}

export function parentOf(path: string): string | null {
  const p = norm(path);
  if (isRoot(p)) return null;
  if (p === HOME) return "/";
  const i = p.lastIndexOf("/");
  if (i <= 0) return "/";
  const parent = p.slice(0, i);
  return /^[A-Za-z]:$/.test(parent) ? `${parent}/` : parent;
}

export function baseName(path: string): string {
  return norm(path).split("/").filter(Boolean).pop() ?? path;
}

export function crumbsOf(path: string): { label: string; path: string }[] {
  const p = norm(path);
  if (p.startsWith("~")) {
    const out = [{ label: "~", path: HOME }];
    let acc = HOME;
    for (const s of p.slice(1).split("/").filter(Boolean)) {
      acc = joinPath(acc, s);
      out.push({ label: s, path: acc });
    }
    return out;
  }
  const drive = /^([A-Za-z]):/.exec(p);
  const rootLabel = drive ? `${drive[1]}:` : "/";
  const rootPath = drive ? `${drive[1]}:/` : "/";
  const out = [{ label: rootLabel, path: rootPath }];
  let acc = rootPath;
  for (const s of p.slice(rootPath.length).split("/").filter(Boolean)) {
    acc = joinPath(acc, s);
    out.push({ label: s, path: acc });
  }
  return out;
}

export function normalizeTypedPath(input: string): string | null {
  const typed = input.trim();
  if (!typed) return null;
  const normalized = norm(typed);
  const trimmed = normalized.replace(/\/+$/, "");
  if (trimmed === "") return "/";
  return /^[A-Za-z]:$/.test(trimmed) && normalized.includes("/") ? `${trimmed}/` : trimmed;
}

export function resolveRemotePath(cwd: string | null, input: string): string {
  const typed = input.trim();
  const raw = norm(typed);
  if (!raw) return raw;
  if (raw.startsWith("/") || raw.startsWith("~") || /^[A-Za-z]:[\\/]/.test(typed)) return raw;
  const normalizedBase = cwd ? norm(cwd) : "";
  const base = /^[A-Za-z]:\/$/.test(normalizedBase)
    ? normalizedBase
    : normalizedBase.replace(/\/+$/, "");
  const rel = raw.replace(/^\.\//, "");
  if (!base) return rel;
  return joinPath(base, rel);
}

export function looksLikePath(text: string): boolean {
  const t = text.trim();
  if (!t || t.length > 512) return false;
  if (t.includes("\n")) return false;
  if (/[\s;|&><`]/.test(t)) return false;
  return true;
}

