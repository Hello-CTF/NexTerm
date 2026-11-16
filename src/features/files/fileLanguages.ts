import { LanguageDescription, LanguageSupport, StreamLanguage, type StreamParser, type Language } from "@codemirror/language";
import type { Extension } from "@codemirror/state";
import { t } from "../../i18n";

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
  { id: "properties", label: t("INI / 配置"), filenames: [".editorconfig", ".gitconfig", ".npmrc", ".gitattributes"], extensions: [".ini", ".conf", ".env", ".cfg", ".properties"], load: () => stream(import("@codemirror/legacy-modes/mode/properties"), "properties") },
  { id: "java", label: "Java", extensions: [".java"], load: () => stream(import("@codemirror/legacy-modes/mode/clike"), "java") },
  { id: "javascript", label: "JavaScript", extensions: [".js", ".mjs", ".cjs", ".jsx"], load: () => stream(import("@codemirror/legacy-modes/mode/javascript"), "javascript") },
  { id: "json", label: "JSON", extensions: [".json", ".jsonc", ".json5"], load: () => stream(import("@codemirror/legacy-modes/mode/javascript"), "json") },
  { id: "kotlin", label: "Kotlin", extensions: [".kt", ".kts"], load: () => stream(import("@codemirror/legacy-modes/mode/clike"), "kotlin") },
  { id: "less", label: "Less", extensions: [".less"], load: () => stream(import("@codemirror/legacy-modes/mode/css"), "less") },
  { id: "lua", label: "Lua", extensions: [".lua"], load: () => stream(import("@codemirror/legacy-modes/mode/lua"), "lua") },
  { id: "nginx", label: "Nginx", filenames: ["nginx.conf"], extensions: [".nginx"], load: () => stream(import("@codemirror/legacy-modes/mode/nginx"), "nginx") },
  { id: "perl", label: "Perl", extensions: [".pl", ".pm"], load: () => stream(import("@codemirror/legacy-modes/mode/perl"), "perl") },
  { id: "powershell", label: "PowerShell", extensions: [".ps1", ".psm1", ".psd1"], load: () => stream(import("@codemirror/legacy-modes/mode/powershell"), "powerShell") },
  { id: "protobuf", label: "Protocol Buffers", extensions: [".proto"], load: () => stream(import("@codemirror/legacy-modes/mode/protobuf"), "protobuf") },
  { id: "python", label: "Python", extensions: [".py", ".pyw", ".pyi"], load: () => stream(import("@codemirror/legacy-modes/mode/python"), "python") },
  { id: "r", label: "R", extensions: [".r"], load: () => stream(import("@codemirror/legacy-modes/mode/r"), "r") },
  { id: "ruby", label: "Ruby", filenames: ["gemfile", "rakefile"], extensions: [".rb", ".rake"], load: () => stream(import("@codemirror/legacy-modes/mode/ruby"), "ruby") },
  { id: "rust", label: "Rust", extensions: [".rs"], load: () => stream(import("@codemirror/legacy-modes/mode/rust"), "rust") },
  { id: "sass", label: "Sass", extensions: [".sass"], load: () => stream(import("@codemirror/legacy-modes/mode/sass"), "sass") },
  { id: "scss", label: "SCSS", extensions: [".scss"], load: () => stream(import("@codemirror/legacy-modes/mode/css"), "sCSS") },
  { id: "shell", label: "Shell", filenames: [".bashrc", ".zshrc", ".profile", ".bash_profile", ".bash_login", ".zprofile", ".zshenv", ".zlogin", ".gitignore", ".dockerignore", ".npmignore"], extensions: [".sh", ".bash", ".zsh", ".ksh", ".bashrc", ".zshrc", ".profile"], load: () => stream(import("@codemirror/legacy-modes/mode/shell"), "shell") },
  { id: "sql", label: "SQL", extensions: [".sql"], load: async () => (await import("@codemirror/lang-sql")).sql() },
  { id: "stylus", label: "Stylus", extensions: [".styl", ".stylus"], load: () => stream(import("@codemirror/legacy-modes/mode/stylus"), "stylus") },
  { id: "swift", label: "Swift", extensions: [".swift"], load: () => stream(import("@codemirror/legacy-modes/mode/swift"), "swift") },
  { id: "toml", label: "TOML", extensions: [".toml"], load: () => stream(import("@codemirror/legacy-modes/mode/toml"), "toml") },
  { id: "typescript", label: "TypeScript", extensions: [".ts", ".tsx", ".mts", ".cts"], load: () => stream(import("@codemirror/legacy-modes/mode/javascript"), "typescript") },
  { id: "xml", label: "XML", extensions: [".xml", ".xsd", ".xsl", ".xslt", ".svg"], load: () => stream(import("@codemirror/legacy-modes/mode/xml"), "xml") },
  { id: "yaml", label: "YAML", extensions: [".yml", ".yaml"], load: () => stream(import("@codemirror/legacy-modes/mode/yaml"), "yaml") },
  { id: "systemd", label: "systemd unit", extensions: [".service", ".socket", ".timer", ".mount", ".automount", ".target", ".path", ".slice"], load: () => stream(import("@codemirror/legacy-modes/mode/properties"), "properties") },
  { id: "groovy", label: "Groovy", filenames: ["jenkinsfile"], extensions: [".groovy", ".gradle"], load: () => stream(import("@codemirror/legacy-modes/mode/groovy"), "groovy") },
  { id: "jinja2", label: "Jinja2", extensions: [".j2", ".jinja", ".jinja2"], load: () => stream(import("@codemirror/legacy-modes/mode/jinja2"), "jinja2") },
  { id: "http", label: "HTTP", extensions: [".http"], load: () => stream(import("@codemirror/legacy-modes/mode/http"), "http") },
  { id: "scala", label: "Scala", extensions: [".scala", ".sc"], load: () => stream(import("@codemirror/legacy-modes/mode/clike"), "scala") },
  { id: "dart", label: "Dart", extensions: [".dart"], load: () => stream(import("@codemirror/legacy-modes/mode/clike"), "dart") },
  { id: "objectivec", label: "Objective-C", extensions: [".m"], load: () => stream(import("@codemirror/legacy-modes/mode/clike"), "objectiveC") },
  { id: "objectivecpp", label: "Objective-C++", extensions: [".mm"], load: () => stream(import("@codemirror/legacy-modes/mode/clike"), "objectiveCpp") },
  { id: "haskell", label: "Haskell", extensions: [".hs"], load: () => stream(import("@codemirror/legacy-modes/mode/haskell"), "haskell") },
  { id: "erlang", label: "Erlang", extensions: [".erl", ".hrl"], load: () => stream(import("@codemirror/legacy-modes/mode/erlang"), "erlang") },
  { id: "clojure", label: "Clojure", extensions: [".clj", ".cljs", ".cljc", ".edn"], load: () => stream(import("@codemirror/legacy-modes/mode/clojure"), "clojure") },
  { id: "ocaml", label: "OCaml", extensions: [".ml", ".mli"], load: () => stream(import("@codemirror/legacy-modes/mode/mllike"), "oCaml") },
  { id: "fsharp", label: "F#", extensions: [".fs", ".fsx"], load: () => stream(import("@codemirror/legacy-modes/mode/mllike"), "fSharp") },
  { id: "julia", label: "Julia", extensions: [".jl"], load: () => stream(import("@codemirror/legacy-modes/mode/julia"), "julia") },
  { id: "fortran", label: "Fortran", extensions: [".f90", ".f95", ".f"], load: () => stream(import("@codemirror/legacy-modes/mode/fortran"), "fortran") },
  { id: "pascal", label: "Pascal", extensions: [".pas"], load: () => stream(import("@codemirror/legacy-modes/mode/pascal"), "pascal") },
  { id: "tcl", label: "Tcl", extensions: [".tcl"], load: () => stream(import("@codemirror/legacy-modes/mode/tcl"), "tcl") },
  { id: "verilog", label: "Verilog", extensions: [".v", ".sv"], load: () => stream(import("@codemirror/legacy-modes/mode/verilog"), "verilog") },
  { id: "vhdl", label: "VHDL", extensions: [".vhd", ".vhdl"], load: () => stream(import("@codemirror/legacy-modes/mode/vhdl"), "vhdl") },
  { id: "vb", label: "VB.NET", extensions: [".vb"], load: () => stream(import("@codemirror/legacy-modes/mode/vb"), "vb") },
  { id: "rpmspec", label: "RPM spec", extensions: [".spec"], load: () => stream(import("@codemirror/legacy-modes/mode/rpm"), "rpmSpec") },
  { id: "puppet", label: "Puppet", extensions: [".pp"], load: () => stream(import("@codemirror/legacy-modes/mode/puppet"), "puppet") },
  { id: "elm", label: "Elm", extensions: [".elm"], load: () => stream(import("@codemirror/legacy-modes/mode/elm"), "elm") },
  { id: "crystal", label: "Crystal", extensions: [".cr"], load: () => stream(import("@codemirror/legacy-modes/mode/crystal"), "crystal") },
  { id: "pug", label: "Pug", extensions: [".pug"], load: () => stream(import("@codemirror/legacy-modes/mode/pug"), "pug") },
  { id: "coffeescript", label: "CoffeeScript", extensions: [".coffee"], load: () => stream(import("@codemirror/legacy-modes/mode/coffeescript"), "coffeeScript") },
  {
    id: "markdown",
    label: "Markdown",
    extensions: [".md", ".markdown", ".mdown", ".mkd"],
    load: async () => (await import("@codemirror/lang-markdown")).markdown({ codeLanguages: markdownFenceLanguage }),
  },
] as const satisfies readonly FileLanguageDefinition[];

export type FileLanguageId = (typeof DEFINITIONS)[number]["id"];
export type FileLanguageChoice = "auto" | "plain" | FileLanguageId;

export const FILE_LANGUAGE_OPTIONS: readonly { id: FileLanguageId; label: string }[] =
  DEFINITIONS.map(({ id, label }) => ({ id, label }));

export function fileLanguageLabel(language: FileLanguageId): string {
  return DEFINITIONS.find((definition) => definition.id === language)?.label ?? language;
}

const NGINX_SUPPORT_FILES = ["mime.types", "fastcgi_params", "scgi_params", "uwsgi_params", "proxy_params"];

// Only configuration files match: a log or script that merely has "nginx" in its path must not.
function isNginxConfig(lower: string): boolean {
  const segments = lower.split(/[\\/]/);
  const name = segments.pop() ?? "";
  if (name === "nginx.conf" || name.endsWith(".nginx")) return true;
  const nginxDirIndex = segments.lastIndexOf("nginx");
  if (nginxDirIndex < 0) return false;
  if (name.endsWith(".conf") || NGINX_SUPPORT_FILES.includes(name)) return true;
  const dirsBelow = segments.slice(nginxDirIndex + 1);
  return dirsBelow.some((dir) => dir === "sites-available" || dir === "sites-enabled");
}

// Fence info strings (```bash) that are not a language id or file extension.
const FENCE_ALIASES: Record<string, FileLanguageId> = {
  bash: "shell", sh: "shell", zsh: "shell", console: "shell", js: "javascript", ts: "typescript",
  py: "python", python3: "python", yml: "yaml", golang: "go", rs: "rust", rb: "ruby", ps1: "powershell",
  docker: "dockerfile", ini: "properties", conf: "properties", "c++": "cpp", "c#": "csharp", cs: "csharp",
  kt: "kotlin", md: "markdown", objc: "objectivec",
};

function markdownFenceLanguage(info: string): LanguageDescription | null {
  const key = info.trim().split(/\s+/)[0]?.toLowerCase() ?? "";
  if (!key) return null;
  const id =
    FENCE_ALIASES[key] ??
    DEFINITIONS.find(
      (definition) => definition.id === key || ("extensions" in definition && (definition.extensions as readonly string[]).includes(`.${key}`)),
    )?.id;
  const definition = DEFINITIONS.find((item) => item.id === id);
  if (!definition || definition.id === "markdown") return null;
  return LanguageDescription.of({
    name: definition.id,
    load: async () => {
      const extension = await loadFileLanguageExtension("", definition.id);
      return extension instanceof LanguageSupport ? extension : new LanguageSupport(extension as Language);
    },
  });
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
  if (isNginxConfig(lower)) return "nginx";
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
