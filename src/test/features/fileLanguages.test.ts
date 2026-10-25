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
