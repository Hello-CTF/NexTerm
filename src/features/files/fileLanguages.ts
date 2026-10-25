import { StreamLanguage, type StreamParser } from "@codemirror/language";
import type { Extension } from "@codemirror/state";

// Language packs are loaded on demand so they stay out of the editor chunk.
async function stream<K extends string>(
  mode: Promise<Record<K, StreamParser<unknown>>>,
  name: K,
): Promise<Extension> {
  return StreamLanguage.define((await mode)[name]);
}

interface FileLanguageDefinition {
  id: string;
  label: string;
  load: () => Promise<Extension>;
  filenames?: readonly string[];
  extensions?: readonly string[];
}

const DEFINITIONS = [
  { id: "c", label: "C", extensions: [".c", ".h"], load: () => stream(import("@codemirror/legacy-modes/mode/clike"), "c") },
  { id: "cpp", label: "C++", extensions: [".cpp", ".cc", ".cxx", ".c++", ".hpp", ".hh", ".hxx"], load: () => stream(import("@codemirror/legacy-modes/mode/clike"), "cpp") },
  { id: "csharp", label: "C#", extensions: [".cs"], load: () => stream(import("@codemirror/legacy-modes/mode/clike"), "csharp") },
  { id: "cmake", label: "CMake", filenames: ["cmakelists.txt"], extensions: [".cmake"], load: () => stream(import("@codemirror/legacy-modes/mode/cmake"), "cmake") },
  { id: "css", label: "CSS", extensions: [".css"], load: () => stream(import("@codemirror/legacy-modes/mode/css"), "css") },
  { id: "diff", label: "Diff", extensions: [".diff", ".patch"], load: () => stream(import("@codemirror/legacy-modes/mode/diff"), "diff") },
  { id: "dockerfile", label: "Dockerfile", filenames: ["dockerfile", "containerfile"], extensions: [".dockerfile"], load: () => stream(import("@codemirror/legacy-modes/mode/dockerfile"), "dockerFile") },
  { id: "go", label: "Go", extensions: [".go"], load: () => stream(import("@codemirror/legacy-modes/mode/go"), "go") },
  { id: "html", label: "HTML", extensions: [".html", ".htm", ".xhtml", ".vue"], load: () => stream(import("@codemirror/legacy-modes/mode/xml"), "html") },
  { id: "properties", label: "INI / 配置", filenames: [".editorconfig", ".gitconfig"], extensions: [".ini", ".conf", ".env", ".cfg", ".properties"], load: () => stream(import("@codemirror/legacy-modes/mode/properties"), "properties") },
  { id: "java", label: "Java", extensions: [".java"], load: () => stream(import("@codemirror/legacy-modes/mode/clike"), "java") },
  { id: "javascript", label: "JavaScript", extensions: [".js", ".mjs", ".cjs", ".jsx"], load: () => stream(import("@codemirror/legacy-modes/mode/javascript"), "javascript") },
  { id: "json", label: "JSON", extensions: [".json", ".jsonc", ".json5"], load: () => stream(import("@codemirror/legacy-modes/mode/javascript"), "json") },
  { id: "kotlin", label: "Kotlin", extensions: [".kt", ".kts"], load: () => stream(import("@codemirror/legacy-modes/mode/clike"), "kotlin") },
  { id: "less", label: "Less", extensions: [".less"], load: () => stream(import("@codemirror/legacy-modes/mode/css"), "less") },
  { id: "lua", label: "Lua", extensions: [".lua"], load: () => stream(import("@codemirror/legacy-modes/mode/lua"), "lua") },
  { id: "nginx", label: "Nginx", filenames: ["nginx.conf"], load: () => stream(import("@codemirror/legacy-modes/mode/nginx"), "nginx") },
  { id: "perl", label: "Perl", extensions: [".pl", ".pm"], load: () => stream(import("@codemirror/legacy-modes/mode/perl"), "perl") },
  { id: "powershell", label: "PowerShell", extensions: [".ps1", ".psm1", ".psd1"], load: () => stream(import("@codemirror/legacy-modes/mode/powershell"), "powerShell") },
  { id: "protobuf", label: "Protocol Buffers", extensions: [".proto"], load: () => stream(import("@codemirror/legacy-modes/mode/protobuf"), "protobuf") },
  { id: "python", label: "Python", extensions: [".py", ".pyw", ".pyi"], load: () => stream(import("@codemirror/legacy-modes/mode/python"), "python") },
  { id: "r", label: "R", extensions: [".r"], load: () => stream(import("@codemirror/legacy-modes/mode/r"), "r") },
  { id: "ruby", label: "Ruby", filenames: ["gemfile", "rakefile"], extensions: [".rb", ".rake"], load: () => stream(import("@codemirror/legacy-modes/mode/ruby"), "ruby") },
  { id: "rust", label: "Rust", extensions: [".rs"], load: () => stream(import("@codemirror/legacy-modes/mode/rust"), "rust") },
  { id: "sass", label: "Sass", extensions: [".sass"], load: () => stream(import("@codemirror/legacy-modes/mode/sass"), "sass") },
  { id: "scss", label: "SCSS", extensions: [".scss"], load: () => stream(import("@codemirror/legacy-modes/mode/css"), "sCSS") },
  { id: "shell", label: "Shell", filenames: [".bashrc", ".zshrc", ".profile", ".bash_profile", ".bash_login", ".zprofile", ".zshenv", ".zlogin"], extensions: [".sh", ".bash", ".zsh", ".ksh", ".bashrc", ".zshrc", ".profile"], load: () => stream(import("@codemirror/legacy-modes/mode/shell"), "shell") },
  { id: "sql", label: "SQL", extensions: [".sql"], load: async () => (await import("@codemirror/lang-sql")).sql() },
  { id: "stylus", label: "Stylus", extensions: [".styl", ".stylus"], load: () => stream(import("@codemirror/legacy-modes/mode/stylus"), "stylus") },
  { id: "swift", label: "Swift", extensions: [".swift"], load: () => stream(import("@codemirror/legacy-modes/mode/swift"), "swift") },
  { id: "toml", label: "TOML", extensions: [".toml"], load: () => stream(import("@codemirror/legacy-modes/mode/toml"), "toml") },
  { id: "typescript", label: "TypeScript", extensions: [".ts", ".tsx", ".mts", ".cts"], load: () => stream(import("@codemirror/legacy-modes/mode/javascript"), "typescript") },
  { id: "xml", label: "XML", extensions: [".xml", ".xsd", ".xsl", ".xslt", ".svg"], load: () => stream(import("@codemirror/legacy-modes/mode/xml"), "xml") },
  { id: "yaml", label: "YAML", extensions: [".yml", ".yaml"], load: () => stream(import("@codemirror/legacy-modes/mode/yaml"), "yaml") },
] as const satisfies readonly FileLanguageDefinition[];

export type FileLanguageId = (typeof DEFINITIONS)[number]["id"];
export type FileLanguageChoice = "auto" | "plain" | FileLanguageId;

export const FILE_LANGUAGE_OPTIONS: readonly { id: FileLanguageId; label: string }[] =
  DEFINITIONS.map(({ id, label }) => ({ id, label }));

export function fileLanguageLabel(language: FileLanguageId): string {
  return DEFINITIONS.find((definition) => definition.id === language)?.label ?? language;
}

export function detectFileLanguage(path: string): FileLanguageId | null {
  const lower = path.toLowerCase();
  if (lower.endsWith(".py")) return "python";
  if (
    lower.endsWith(".sh") ||
    lower.endsWith(".bash") ||
    lower.endsWith(".bashrc") ||
    lower.endsWith(".zshrc") ||
    lower.endsWith(".profile")
  ) {
    return "shell";
  }
  if (lower.includes("nginx")) return "nginx";
  if (lower.endsWith(".yml") || lower.endsWith(".yaml")) return "yaml";
  if (lower.endsWith(".ini") || lower.endsWith(".conf") || lower.endsWith(".env")) {
    return "properties";
  }
  if (lower.endsWith(".js") || lower.endsWith(".mjs")) return "javascript";
  if (lower.endsWith(".json")) return "json";
  if (lower.endsWith(".ts")) return "typescript";
  if (lower.endsWith(".sql")) return "sql";

  const name = lower.split(/[\\/]/).pop() ?? lower;
  for (const definition of DEFINITIONS) {
    const filenames: readonly string[] | undefined =
      "filenames" in definition ? definition.filenames : undefined;
    if (filenames?.includes(name)) return definition.id;
  }
  for (const definition of DEFINITIONS) {
    const extensions: readonly string[] | undefined =
      "extensions" in definition ? definition.extensions : undefined;
    if (extensions?.some((extension) => name.endsWith(extension))) return definition.id;
  }
  return null;
}

export function resolveFileLanguage(
  path: string,
  choice: FileLanguageChoice = "auto",
): FileLanguageId | null {
  if (choice === "plain") return null;
  return choice === "auto" ? detectFileLanguage(path) : choice;
}

const loaded = new Map<FileLanguageId, Promise<Extension>>();

/**
 * Resolves the CodeMirror extension for a file. The promise rejects when the language pack
 * cannot be fetched; callers keep the editor usable as plain text in that case.
 */
export function loadFileLanguageExtension(
  path: string,
  choice: FileLanguageChoice = "auto",
): Promise<Extension> {
  const language = resolveFileLanguage(path, choice);
  if (!language) return Promise.resolve([]);
  const definition = DEFINITIONS.find((item) => item.id === language);
  if (!definition) return Promise.resolve([]);
  let pending = loaded.get(language);
  if (!pending) {
    pending = definition.load();
    loaded.set(language, pending);
    // A failed fetch must not be cached, otherwise a transient network error sticks until reload.
    pending.catch(() => {
      if (loaded.get(language) === pending) loaded.delete(language);
    });
  }
  return pending;
}
