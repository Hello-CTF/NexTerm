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

const BINARY_EXT = new Set([
  "png", "jpg", "jpeg", "gif", "webp", "bmp", "ico", "tiff", "psd", "heic",
  "zip", "tar", "gz", "tgz", "bz2", "xz", "zst", "7z", "rar", "jar", "war", "deb", "rpm", "apk", "dmg", "iso", "img",
  "so", "dll", "exe", "bin", "o", "a", "lib", "class", "pyc", "pyd", "wasm", "node",
  "mp3", "wav", "flac", "aac", "ogg", "mp4", "mov", "avi", "mkv", "webm",
  "woff", "woff2", "ttf", "otf", "eot", "pdf", "doc", "docx", "xls", "xlsx", "ppt", "pptx",
  "db", "sqlite", "sqlite3", "mdb",
]);

export function extOf(name: string): string {
  const i = name.lastIndexOf(".");
  if (i <= 0 || i === name.length - 1) return "";
  return name.slice(i + 1).toLowerCase();
}

export function isEditableFile(name: string): boolean {
  return !BINARY_EXT.has(extOf(name));
}

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

export function isExtractableArchive(name: string): boolean {
  const lower = name.toLowerCase();
  return EXTRACTABLE_SUFFIX.some((s) => lower.endsWith(s));
}

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

const CONFIG_NAMES = new Set([
  ".bashrc", ".bash_profile", ".bash_logout", ".profile", ".zshrc", ".gitconfig", ".npmrc",
  ".wget-hsts", ".editorconfig", ".dockerignore", ".gitignore", ".env", ".vimrc", ".tmux.conf",
  "dockerfile", "makefile", "nginx.conf", "passwd", "fstab", "hosts", "hostname", "resolv.conf",
  "crontab", "sudoers", "mime.types", "fastcgi_params",
]);

export interface FileVisual {
  Icon: NxIcon;
  tone: string;
}

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

export function formatSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MB`;
  return `${(n / 1024 ** 3).toFixed(2)} GB`;
}
