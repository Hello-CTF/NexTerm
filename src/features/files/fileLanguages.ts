import { StreamLanguage } from "@codemirror/language";
import type { Extension } from "@codemirror/state";
import { sql } from "@codemirror/lang-sql";
import { c, cpp, csharp, java, kotlin } from "@codemirror/legacy-modes/mode/clike";
import { cmake } from "@codemirror/legacy-modes/mode/cmake";
import { css, less, sCSS } from "@codemirror/legacy-modes/mode/css";
import { diff } from "@codemirror/legacy-modes/mode/diff";
import { dockerFile } from "@codemirror/legacy-modes/mode/dockerfile";
import { go } from "@codemirror/legacy-modes/mode/go";
import { javascript, json, typescript } from "@codemirror/legacy-modes/mode/javascript";
import { lua } from "@codemirror/legacy-modes/mode/lua";
import { nginx } from "@codemirror/legacy-modes/mode/nginx";
import { perl } from "@codemirror/legacy-modes/mode/perl";
import { powerShell } from "@codemirror/legacy-modes/mode/powershell";
import { properties } from "@codemirror/legacy-modes/mode/properties";
import { protobuf } from "@codemirror/legacy-modes/mode/protobuf";
import { python } from "@codemirror/legacy-modes/mode/python";
import { r } from "@codemirror/legacy-modes/mode/r";
import { ruby } from "@codemirror/legacy-modes/mode/ruby";
import { rust } from "@codemirror/legacy-modes/mode/rust";
import { sass } from "@codemirror/legacy-modes/mode/sass";
import { shell } from "@codemirror/legacy-modes/mode/shell";
import { stylus } from "@codemirror/legacy-modes/mode/stylus";
import { swift } from "@codemirror/legacy-modes/mode/swift";
import { toml } from "@codemirror/legacy-modes/mode/toml";
import { html, xml } from "@codemirror/legacy-modes/mode/xml";
import { yaml } from "@codemirror/legacy-modes/mode/yaml";

interface FileLanguageDefinition {
  id: string;
  label: string;
  create: () => Extension;
  filenames?: readonly string[];
  extensions?: readonly string[];
}

const DEFINITIONS = [
  { id: "c", label: "C", extensions: [".c", ".h"], create: () => StreamLanguage.define(c) },
  { id: "cpp", label: "C++", extensions: [".cpp", ".cc", ".cxx", ".c++", ".hpp", ".hh", ".hxx"], create: () => StreamLanguage.define(cpp) },
  { id: "csharp", label: "C#", extensions: [".cs"], create: () => StreamLanguage.define(csharp) },
  { id: "cmake", label: "CMake", filenames: ["cmakelists.txt"], extensions: [".cmake"], create: () => StreamLanguage.define(cmake) },
  { id: "css", label: "CSS", extensions: [".css"], create: () => StreamLanguage.define(css) },
  { id: "diff", label: "Diff", extensions: [".diff", ".patch"], create: () => StreamLanguage.define(diff) },
  { id: "dockerfile", label: "Dockerfile", filenames: ["dockerfile", "containerfile"], extensions: [".dockerfile"], create: () => StreamLanguage.define(dockerFile) },
  { id: "go", label: "Go", extensions: [".go"], create: () => StreamLanguage.define(go) },
  { id: "html", label: "HTML", extensions: [".html", ".htm", ".xhtml", ".vue"], create: () => StreamLanguage.define(html) },
  { id: "properties", label: "INI / 配置", filenames: [".editorconfig", ".gitconfig"], extensions: [".ini", ".conf", ".env", ".cfg", ".properties"], create: () => StreamLanguage.define(properties) },
  { id: "java", label: "Java", extensions: [".java"], create: () => StreamLanguage.define(java) },
  { id: "javascript", label: "JavaScript", extensions: [".js", ".mjs", ".cjs", ".jsx"], create: () => StreamLanguage.define(javascript) },
  { id: "json", label: "JSON", extensions: [".json", ".jsonc", ".json5"], create: () => StreamLanguage.define(json) },
  { id: "kotlin", label: "Kotlin", extensions: [".kt", ".kts"], create: () => StreamLanguage.define(kotlin) },
  { id: "less", label: "Less", extensions: [".less"], create: () => StreamLanguage.define(less) },
  { id: "lua", label: "Lua", extensions: [".lua"], create: () => StreamLanguage.define(lua) },
  { id: "nginx", label: "Nginx", filenames: ["nginx.conf"], create: () => StreamLanguage.define(nginx) },
  { id: "perl", label: "Perl", extensions: [".pl", ".pm"], create: () => StreamLanguage.define(perl) },
  { id: "powershell", label: "PowerShell", extensions: [".ps1", ".psm1", ".psd1"], create: () => StreamLanguage.define(powerShell) },
  { id: "protobuf", label: "Protocol Buffers", extensions: [".proto"], create: () => StreamLanguage.define(protobuf) },
  { id: "python", label: "Python", extensions: [".py", ".pyw", ".pyi"], create: () => StreamLanguage.define(python) },
  { id: "r", label: "R", extensions: [".r"], create: () => StreamLanguage.define(r) },
  { id: "ruby", label: "Ruby", filenames: ["gemfile", "rakefile"], extensions: [".rb", ".rake"], create: () => StreamLanguage.define(ruby) },
  { id: "rust", label: "Rust", extensions: [".rs"], create: () => StreamLanguage.define(rust) },
  { id: "sass", label: "Sass", extensions: [".sass"], create: () => StreamLanguage.define(sass) },
  { id: "scss", label: "SCSS", extensions: [".scss"], create: () => StreamLanguage.define(sCSS) },
  { id: "shell", label: "Shell", filenames: [".bashrc", ".zshrc", ".profile", ".bash_profile", ".bash_login", ".zprofile", ".zshenv", ".zlogin"], extensions: [".sh", ".bash", ".zsh", ".ksh", ".bashrc", ".zshrc", ".profile"], create: () => StreamLanguage.define(shell) },
  { id: "sql", label: "SQL", extensions: [".sql"], create: () => sql() },
  { id: "stylus", label: "Stylus", extensions: [".styl", ".stylus"], create: () => StreamLanguage.define(stylus) },
  { id: "swift", label: "Swift", extensions: [".swift"], create: () => StreamLanguage.define(swift) },
  { id: "toml", label: "TOML", extensions: [".toml"], create: () => StreamLanguage.define(toml) },
  { id: "typescript", label: "TypeScript", extensions: [".ts", ".tsx", ".mts", ".cts"], create: () => StreamLanguage.define(typescript) },
  { id: "xml", label: "XML", extensions: [".xml", ".xsd", ".xsl", ".xslt", ".svg"], create: () => StreamLanguage.define(xml) },
  { id: "yaml", label: "YAML", extensions: [".yml", ".yaml"], create: () => StreamLanguage.define(yaml) },
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

export function fileLanguageExtension(
  path: string,
  choice: FileLanguageChoice = "auto",
): Extension {
  const language = resolveFileLanguage(path, choice);
  if (!language) return [];
  return DEFINITIONS.find((definition) => definition.id === language)?.create() ?? [];
}
