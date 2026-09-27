// 文件类型判定与图标/配色映射：左栏文件树、文件浏览器、编辑器共用一份。
//
// 为什么单独抽出来：同一个文件在三处出现（左栏树、宽幅浏览器、编辑器标签），
// 图标与颜色必须完全一致，否则用户会以为看到了不同的东西。
import {
  IconArchive,
  IconCode,
  IconDatabase,
  IconFile,
  IconFolder,
  IconImage,
  IconSettings,
  IconTerminal,
  type IconProps,
} from "../../ui/icons";

type NxIcon = (p: IconProps) => ReturnType<typeof IconFile>;

/** 明确**不可**当文本编辑的扩展名（其余一律允许，交给编辑器试探解码）。 */
const BINARY_EXT = new Set([
  // 图片
  "png", "jpg", "jpeg", "gif", "webp", "bmp", "ico", "tiff", "psd", "heic",
  // 归档 / 安装包
  "zip", "tar", "gz", "tgz", "bz2", "xz", "zst", "7z", "rar", "jar", "war", "deb", "rpm", "apk", "dmg", "iso", "img",
  // 可执行 / 目标文件
  "so", "dll", "exe", "bin", "o", "a", "lib", "class", "pyc", "pyd", "wasm", "node",
  // 音视频
  "mp3", "wav", "flac", "aac", "ogg", "mp4", "mov", "avi", "mkv", "webm",
  // 字体 / 文档二进制
  "woff", "woff2", "ttf", "otf", "eot", "pdf", "doc", "docx", "xls", "xlsx", "ppt", "pptx",
  // 数据库文件
  "db", "sqlite", "sqlite3", "mdb",
]);

/** 取扩展名（小写，不含点）。 */
export function extOf(name: string): string {
  const i = name.lastIndexOf(".");
  // 以点开头的隐藏文件（.bashrc）不算有扩展名
  if (i <= 0 || i === name.length - 1) return "";
  return name.slice(i + 1).toLowerCase();
}

/** 是否可以用内置编辑器打开。 */
export function isEditableFile(name: string): boolean {
  return !BINARY_EXT.has(extOf(name));
}

/**
 * 内核**能解压**的归档后缀。
 *
 * 必须与 `src-tauri/src/fs/mod.rs` 的 `extract_command` 一一对应 ——
 * 两边不一致的后果是菜单给了「解压」、点了报"不支持的压缩格式"。
 * 判定用完整后缀而不是 `extOf`：`.tar.gz` 的最后一段是 `gz`，只看它会把
 * 普通 `.gz` 单文件也当成能解压的包。
 */
const EXTRACTABLE_SUFFIX = [
  ".tar.gz",
  ".tgz",
  ".tar.bz2",
  ".tbz2",
  ".tbz",
  ".tar.xz",
  ".txz",
  ".tar",
  ".zip",
];

/** 是否是内核能解压的归档。 */
export function isExtractableArchive(name: string): boolean {
  const lower = name.toLowerCase();
  return EXTRACTABLE_SUFFIX.some((s) => lower.endsWith(s));
}

/** 后端支持在编辑时保留备份的扩展名（保存提示里会区分文案）。 */
const SCRIPT_EXT = new Set(["sh", "bash", "zsh", "fish", "py", "pl", "rb", "ps1", "bat", "cmd"]);
const CODE_EXT = new Set([
  "js", "mjs", "cjs", "ts", "tsx", "jsx", "vue", "svelte",
  "go", "rs", "java", "kt", "kts", "c", "h", "cc", "cpp", "hpp", "cs", "swift", "php", "lua",
  "css", "scss", "less", "html", "htm", "xml", "svg", "vue",
]);
const CONFIG_EXT = new Set([
  "conf", "cnf", "cfg", "ini", "yml", "yaml", "toml", "env", "properties", "service", "timer", "socket", "rules", "editorconfig",
]);
const ARCHIVE_EXT = new Set(["zip", "tar", "gz", "tgz", "bz2", "xz", "7z", "rar", "deb", "rpm", "jar", "war"]);
const IMAGE_EXT = new Set(["png", "jpg", "jpeg", "gif", "webp", "bmp", "ico", "svg", "tiff"]);
const DB_EXT = new Set(["sql", "db", "sqlite", "sqlite3", "mdb"]);
const LOG_EXT = new Set(["log", "pid", "lock", "out", "err"]);

/** 无扩展名的常见配置文件（按整名匹配）。 */
const CONFIG_NAMES = new Set([
  ".bashrc", ".bash_profile", ".bash_logout", ".profile", ".zshrc", ".gitconfig", ".npmrc",
  ".wget-hsts", ".editorconfig", ".dockerignore", ".gitignore", ".env", ".vimrc", ".tmux.conf",
  "dockerfile", "makefile", "nginx.conf", "passwd", "fstab", "hosts", "hostname", "resolv.conf",
  "crontab", "sudoers", "mime.types", "fastcgi_params",
]);

export interface FileVisual {
  Icon: NxIcon;
  /** Tailwind 文字色类（用项目设计令牌，低饱和）。 */
  tone: string;
}

/**
 * 文件 → 图标 + 配色。
 *
 * 用色克制：目录暖黄、脚本绿、源码蓝、配置紫、归档浅黄，其余中性灰。
 * 彩色只在"类型"这一层出现，不参与任何状态表达（状态色另有 nx-badge）。
 */
export function fileVisual(name: string, kind: string): FileVisual {
  if (kind === "dir") return { Icon: IconFolder, tone: "text-amber-400/90" };
  if (kind === "symlink") return { Icon: IconFile, tone: "text-blue-300" };

  const ext = extOf(name);
  const lower = name.toLowerCase();
  if (ARCHIVE_EXT.has(ext)) return { Icon: IconArchive, tone: "text-amber-300/90" };
  if (IMAGE_EXT.has(ext)) return { Icon: IconImage, tone: "text-neutral-400" };
  if (DB_EXT.has(ext)) return { Icon: IconDatabase, tone: "text-purple-300" };
  if (SCRIPT_EXT.has(ext)) return { Icon: IconTerminal, tone: "text-green-400" };
  if (CODE_EXT.has(ext)) return { Icon: IconCode, tone: "text-blue-300" };
  if (CONFIG_EXT.has(ext) || CONFIG_NAMES.has(lower)) return { Icon: IconSettings, tone: "text-purple-300" };
  if (LOG_EXT.has(ext)) return { Icon: IconFile, tone: "text-neutral-500" };
  if (ext === "md" || ext === "txt" || ext === "csv" || ext === "tsv") {
    return { Icon: IconFile, tone: "text-neutral-500" };
  }
  return { Icon: IconFile, tone: "text-neutral-500" };
}

/** 人类可读体积。 */
export function formatSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MB`;
  return `${(n / 1024 ** 3).toFixed(2)} GB`;
}
