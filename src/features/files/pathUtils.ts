// 文件路径的小工具：左栏文件树（FileTree）与宽幅文件浏览器（FileBrowser）共用。
//
// 为什么单独放一个文件：这两处的路径算术必须完全一致 —— 面包屑、上级、拼接、
// 盘符根判断，任何一边漏一条规则，同一个路径在两个界面里就会长得不一样。
//
// 两条必须记住的约定：
//   1. 后端给的路径可能是反斜杠（Windows 本地会话返回 `C:\Users\x\y`），
//      进来先 `norm()`。Windows 的文件 API 本来就接受正斜杠，回传也没问题。
//   2. `~` 由后端展开（本地用 `dirs::home_dir()`，SSH 用 SFTP `realpath(".")`），
//      所以 `~` 可以当成一个正常的"根"来用。

/** 家目录。后端展开，前端只负责把它当作一个可以落脚的位置。 */
export const HOME = "~";

/** 反斜杠统一成正斜杠。 */
export function norm(path: string): string {
  return path.replace(/\\/g, "/");
}

/** 到顶了没有：类 Unix 的 `/` 或盘符根 `C:/`。 */
export function isRoot(path: string): boolean {
  const p = norm(path);
  return p === "/" || /^[A-Za-z]:\/$/.test(p);
}

/** 拼接子项。`~` 与 `/` 都能直接接。 */
export function joinPath(dir: string, name: string): string {
  const base = norm(dir);
  return base.endsWith("/") ? base + name : `${base}/${name}`;
}

/** 上一级；到根返回 null。`~` 的上一级是 `/`（允许从家目录向上浏览整机）。 */
export function parentOf(path: string): string | null {
  const p = norm(path);
  if (isRoot(p)) return null;
  if (p === HOME) return "/";
  const i = p.lastIndexOf("/");
  if (i <= 0) return "/";
  const parent = p.slice(0, i);
  // `C:` 要补回盘符根的形式，否则下一次 list 会落到"当前盘的工作目录"
  return /^[A-Za-z]:$/.test(parent) ? `${parent}/` : parent;
}

/** 路径最后一段（下载时给默认文件名用）。 */
export function baseName(path: string): string {
  return norm(path).split("/").filter(Boolean).pop() ?? path;
}

/** 面包屑：`/`、`~`、盘符根都作为首段，每段可点击跳转。 */
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

/**
 * 归一化用户手输的路径：去空白、反斜杠转正斜杠、去尾斜杠。
 * 空输入返回 null（调用方当作"取消"）；只输 `/` 时保留 `/`
 * —— 否则会被尾斜杠正则吃成空串。
 */
export function normalizeTypedPath(input: string): string | null {
  const typed = input.trim();
  if (!typed) return null;
  const normalized = norm(typed);
  const trimmed = normalized.replace(/\/+$/, "");
  if (trimmed === "") return "/";
  return /^[A-Za-z]:$/.test(trimmed) && normalized.includes("/") ? `${trimmed}/` : trimmed;
}

/**
 * 把「用户随手敲出来的」路径落成绝对路径。
 *
 * 终端里输入的路径天然是相对当前目录的（`./a.log` / `logs/x.log` / 裸文件名），
 * 而内核的 fs_* 系列要的是一个它认得的位置串。所以相对路径一律按 cwd 拼；
 * cwd 拿不到（本地会话、刚重连）时保持原样 —— 让内核自己按它自己的当前目录解释，
 * 比前端瞎猜一个前缀靠谱。
 */
export function resolveRemotePath(cwd: string | null, input: string): string {
  const typed = input.trim();
  const raw = norm(typed);
  if (!raw) return raw;
  // 已经是绝对路径：POSIX 根、家目录、Windows 盘符
  if (raw.startsWith("/") || raw.startsWith("~") || /^[A-Za-z]:[\\/]/.test(typed)) return raw;
  const normalizedBase = cwd ? norm(cwd) : "";
  const base = /^[A-Za-z]:\/$/.test(normalizedBase)
    ? normalizedBase
    : normalizedBase.replace(/\/+$/, "");
  const rel = raw.replace(/^\.\//, "");
  if (!base) return rel;
  return joinPath(base, rel);
}

/**
 * 一段终端选区「像不像一个路径」。
 *
 * 只做粗筛：单行、不含中文标点与空白包裹 —— 够用来决定要不要把它预填进
 * 「下载哪个文件」的输入框，判错也只是预填了个要改的值，不会做错事。
 */
export function looksLikePath(text: string): boolean {
  const t = text.trim();
  if (!t || t.length > 512) return false;
  if (t.includes("\n")) return false;
  // 有空格的行更像一句命令；但不否定「带空格的目录名」，所以只挡明显的命令式
  if (/[\s;|&><`]/.test(t)) return false;
  return true;
}

