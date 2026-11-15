import { describe, expect, it } from "vitest";
import { ensureSyntaxTree, language } from "@codemirror/language";
import { EditorState } from "@codemirror/state";
import {
  detectFileLanguage,
  FILE_LANGUAGE_OPTIONS,
  loadFileLanguageExtension,
  resolveFileLanguage,
  type FileLanguageId,
} from "../../features/files/fileLanguages";

describe("file editor language detection", () => {
  it.each([
    ["/tmp/script.PY", "python"],
    ["/tmp/component.TSX", "typescript"],
    ["/tmp/styles.SCSS", "scss"],
    ["/tmp/query.SQL", "sql"],
    ["C:\\src\\MAIN.Go", "go"],
    ["/tmp/analysis.R", "r"],
  ] as const)("matches %s case-insensitively", (path, language) => {
    expect(detectFileLanguage(path)).toBe(language);
  });

  it.each([
    ["/tmp/Dockerfile", "dockerfile"],
    ["/tmp/CONTAINERFILE", "dockerfile"],
    ["/tmp/CMakeLists.txt", "cmake"],
    ["/root/.BASHRC", "shell"],
    ["/root/.zprofile", "shell"],
    ["/etc/nginx/sites-enabled/default", "nginx"],
    ["/tmp/Gemfile", "ruby"],
  ] as const)("detects special filename %s", (path, language) => {
    expect(detectFileLanguage(path)).toBe(language);
  });

  it.each([
    ["/a.py", "python"],
    ["/a.sh", "shell"],
    ["/a.bash", "shell"],
    ["/a.bashrc", "shell"],
    ["/a.zshrc", "shell"],
    ["/a.profile", "shell"],
    ["/etc/nginx/nginx.conf", "nginx"],
    ["/a.yml", "yaml"],
    ["/a.yaml", "yaml"],
    ["/a.ini", "properties"],
    ["/a.conf", "properties"],
    ["/a.env", "properties"],
    ["/a.js", "javascript"],
    ["/a.mjs", "javascript"],
    ["/a.json", "json"],
    ["/a.ts", "typescript"],
    ["/a.sql", "sql"],
  ] as const)("keeps the existing default for %s", (path, language) => {
    expect(detectFileLanguage(path)).toBe(language);
  });

  it.each([
    ["/a.c", "c"],
    ["/a.h", "c"],
    ["/a.cpp", "cpp"],
    ["/a.hpp", "cpp"],
    ["/a.cs", "csharp"],
    ["/a.java", "java"],
    ["/a.kt", "kotlin"],
    ["/a.go", "go"],
    ["/a.rs", "rust"],
    ["/a.rb", "ruby"],
    ["/a.pl", "perl"],
    ["/a.lua", "lua"],
    ["/a.swift", "swift"],
    ["/a.ps1", "powershell"],
    ["/a.html", "html"],
    ["/a.xml", "xml"],
    ["/a.css", "css"],
    ["/a.scss", "scss"],
    ["/a.less", "less"],
    ["/a.diff", "diff"],
    ["/a.toml", "toml"],
    ["/a.proto", "protobuf"],
    ["/a.cmake", "cmake"],
  ] as const)("adds the installed legacy mode for %s", (path, language) => {
    expect(detectFileLanguage(path)).toBe(language);
  });

  it("falls back to plain text for unknown and ordinary text paths", () => {
    expect(detectFileLanguage("/a.txt")).toBeNull();
    expect(detectFileLanguage("/a.unknown")).toBeNull();
    expect(detectFileLanguage("/README")).toBeNull();
    expect(resolveFileLanguage("/a.py", "plain")).toBeNull();
  });

  it("resolves plain text without loading a pack", async () => {
    await expect(loadFileLanguageExtension("/a.unknown", "auto")).resolves.toEqual([]);
    await expect(loadFileLanguageExtension("/a.py", "plain")).resolves.toEqual([]);
  });

  it("uses an explicit language even when the path is unknown", () => {
    const cases: Array<[string, FileLanguageId]> = [
      ["/a.unknown", "python"],
      ["/a.txt", "sql"],
      ["/a.py", "javascript"],
    ];
    for (const [path, language] of cases) {
      expect(resolveFileLanguage(path, language)).toBe(language);
    }
  });

  it.each(FILE_LANGUAGE_OPTIONS)("creates a usable $id language extension", async ({ id }) => {
    const state = EditorState.create({
      doc: "value",
      extensions: await loadFileLanguageExtension("/a.unknown", id),
    });

    expect(state.facet(language)).not.toBeNull();
    expect(ensureSyntaxTree(state, state.doc.length, 1000)).not.toBeNull();
  });
});

describe("extended server file formats", () => {
  it.each([
    ["/a.md", "markdown"], ["/a.MARKDOWN", "markdown"], ["/a.mdown", "markdown"], ["/a.mkd", "markdown"],
    ["/etc/systemd/system/a.service", "systemd"], ["/a.socket", "systemd"], ["/a.timer", "systemd"],
    ["/a.mount", "systemd"], ["/a.target", "systemd"], ["/a.path", "systemd"], ["/a.slice", "systemd"],
    ["/repo/.gitignore", "shell"], ["/repo/.dockerignore", "shell"], ["/repo/.npmrc", "properties"],
    ["/ci/Jenkinsfile", "groovy"], ["/a.groovy", "groovy"], ["/build.gradle", "groovy"],
    ["/a.j2", "jinja2"], ["/a.jinja", "jinja2"], ["/a.jinja2", "jinja2"], ["/a.http", "http"],
    ["/a.scala", "scala"], ["/a.sc", "scala"], ["/a.dart", "dart"],
    ["/a.m", "objectivec"], ["/a.mm", "objectivecpp"], ["/a.hs", "haskell"],
    ["/a.erl", "erlang"], ["/a.hrl", "erlang"],
    ["/a.clj", "clojure"], ["/a.cljs", "clojure"], ["/a.cljc", "clojure"], ["/a.edn", "clojure"],
    ["/a.ml", "ocaml"], ["/a.mli", "ocaml"], ["/a.fs", "fsharp"], ["/a.fsx", "fsharp"],
    ["/a.jl", "julia"], ["/a.f90", "fortran"], ["/a.f95", "fortran"], ["/a.f", "fortran"],
    ["/a.pas", "pascal"], ["/a.tcl", "tcl"], ["/a.v", "verilog"], ["/a.sv", "verilog"],
    ["/a.vhd", "vhdl"], ["/a.vhdl", "vhdl"], ["/a.vb", "vb"], ["/a.spec", "rpmspec"],
    ["/a.pp", "puppet"], ["/a.elm", "elm"], ["/a.cr", "crystal"], ["/a.pug", "pug"],
    ["/a.coffee", "coffeescript"],
  ] as const)("detects %s", (path, language) => {
    expect(detectFileLanguage(path)).toBe(language);
  });

  it("keeps neighbouring extensions on their existing languages", () => {
    expect(detectFileLanguage("/a.cljs")).toBe("clojure");
    expect(detectFileLanguage("/a.spec.ts")).toBe("typescript");
    expect(detectFileLanguage("/a.pdf")).toBeNull();
    expect(detectFileLanguage("/README")).toBeNull();
  });

  it.each([
    "/etc/nginx/nginx.conf", "/usr/local/nginx/conf/nginx.conf", "/etc/nginx/conf.d/site.conf",
    "/etc/nginx/sites-available/example.com", "/etc/nginx/sites-enabled/default",
    "/etc/nginx/mime.types", "/srv/site.nginx",
  ])("detects nginx config %s", (path) => {
    expect(detectFileLanguage(path)).toBe("nginx");
  });

  it.each([
    ["/var/log/nginx/access.log", null], ["/var/log/nginx/error.log", null],
    ["/opt/nginx-helper.py", "python"], ["/opt/nginx/reload.sh", "shell"],
    ["/etc/apache2/sites-enabled/000-default.conf", "properties"], ["/etc/app/app.conf", "properties"],
    ["/home/u/nginx-notes.md", "markdown"],
  ] as const)("does not treat %s as nginx", (path, language) => {
    expect(detectFileLanguage(path)).toBe(language);
  });

  it.each([
    ["/a.md", "# Title\n\n```bash\necho hi\n```\n"],
    ["/a.scala", "object A { val x = 1 }"],
    ["/a.hs", "main = putStrLn \"x\""],
    ["/a.service", "[Unit]\nDescription=x\n"],
    ["/a.ml", "let x = 1"],
  ])("loads an editor extension for %s", async (path, doc) => {
    const state = EditorState.create({ doc, extensions: await loadFileLanguageExtension(path) });
    expect(state.facet(language)).not.toBeNull();
    expect(ensureSyntaxTree(state, state.doc.length, 1000)).not.toBeNull();
  });

  it("highlights fenced code in markdown with the existing lazy languages", async () => {
    const doc = "```json\n{\"a\": 1}\n```\n";
    const initial = EditorState.create({ doc, extensions: await loadFileLanguageExtension("/a.md") });
    // The fence language loads lazily while the first parse runs; a later parse picks it up.
    ensureSyntaxTree(initial, initial.doc.length, 1000);
    await new Promise((resolve) => setTimeout(resolve, 50));
    const state = initial.update({ changes: { from: doc.length, insert: "\n" } }).state;
    const tree = ensureSyntaxTree(state, state.doc.length, 1000);
    expect(tree?.toString()).toContain("FencedCode");
    const inner = tree?.resolveInner(doc.indexOf("{"), 1);
    expect(inner?.type.name).not.toBe("CodeText");
    expect(inner?.type.name).not.toBe("FencedCode");
  });
});
