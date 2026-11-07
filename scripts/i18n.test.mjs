import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { entryProblems, joinJsxFiles, wrapFile } from "./i18n.mjs";

function wrapped(name, source) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "i18n-wrap-"));
  const file = path.join(dir, name);
  fs.writeFileSync(file, source);
  const result = wrapFile(file, false);
  return { text: fs.readFileSync(file, "utf8"), result };
}

test("wraps shown text and leaves compared, matched and keyed text alone", () => {
  const { text } = wrapped(
    "view.tsx",
    [
      'import { x } from "./x";',
      'const LABELS = { "键": "标签" };',
      'export function View({ n, title }: { n: number; title: string }) {',
      '  if (title === "工具") return null;',
      '  if (title.includes("失败")) return null;',
      '  return <div title="提示">保存 <b>{n}</b> {`共 ${n} 台`}<i>设置 &gt; 终端</i></div>;',
      "}",
      "",
    ].join("\n"),
  );
  assert.match(text, /import \{ t \} from ".*i18n";/);
  assert.match(text, /"键": t\("标签"\)/);
  assert.match(text, /title === "工具"/);
  assert.match(text, /title\.includes\("失败"\)/);
  assert.match(text, /title=\{t\("提示"\)\}/);
  assert.match(text, /\{t\("保存"\)\} <b>/);
  assert.match(text, /t\("共 \{n\} 台", \{ n \}\)/);
  assert.match(text, /<i>\{t\("设置 > 终端"\)\}<\/i>/);
});

test("imports t under another name when the file already binds t", () => {
  const { text } = wrapped("tabs.ts", 'export const names = (tabs: { id: string }[]) => tabs.map((t) => `${t.id} 号`);\n');
  assert.match(text, /import \{ t as tr \} from/);
  assert.match(text, /tr\("\{id\} 号", \{ id: t\.id \}\)/);
});

test("validates placeholders and plural forms of a translation", () => {
  assert.deepEqual(entryProblems("ui", "共 {count} 台", { one: "{count} host", other: "{count} hosts" }), []);
  assert.notDeepEqual(entryProblems("ui", "共 {count} 台", "hosts"), []);
  assert.deepEqual(entryProblems("server", "连接 %s 失败: %v", "Could not connect to %s: %v"), []);
  assert.notDeepEqual(entryProblems("server", "连接 %s 失败: %v", "Could not connect: %v"), []);
});

test("joins JSX text and string values into one t() call, and stops at elements and unknown values", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "i18n-join-"));
  fs.writeFileSync(path.join(dir, "tsconfig.json"), JSON.stringify({ compilerOptions: { jsx: "preserve", strict: true, target: "es2022", module: "esnext", moduleResolution: "bundler", noEmit: true } }));
  fs.mkdirSync(path.join(dir, "i18n"));
  fs.writeFileSync(path.join(dir, "i18n", "index.ts"), "export function t(key: string, params?: Record<string, unknown>): string { return key; }\n");
  const file = path.join(dir, "view.tsx");
  fs.writeFileSync(
    file,
    [
      'import { t } from "./i18n";',
      "declare const JSX: any;",
      "export function View({ n, name, icon, maybe }: { n: number; name: string; icon: object; maybe?: string }) {",
      "  return (",
      "    <div>",
      '      <p>{t("共")} {n} {t("台主机")}</p>',
      '      <p>{t("输出")} {n} tokens',
      "      </p>",
      '      <p>{t("主机")}{name}<b>{n}</b>{t("在线")}</p>',
      '      <p>{t("图标")}{icon as never}{t("结束")}</p>',
      '      <p>{t("可能")}{maybe}{t("为空")}</p>',
      "    </div>",
      "  );",
      "}",
      "",
    ].join("\n"),
  );
  joinJsxFiles([file], path.join(dir, "tsconfig.json"), false);
  const text = fs.readFileSync(file, "utf8");
  assert.match(text, /<p>\{t\("共 \{n\} 台主机", \{ n \}\)\}<\/p>/);
  assert.match(text, /\{t\("输出 \{n\} tokens", \{ n \}\)\}/);
  assert.match(text, /<p>\{t\("主机\{name\}", \{ name \}\)\}<b>/);
  assert.match(text, /\{t\("图标"\)\}\{icon as never\}\{t\("结束"\)\}/);
  assert.match(text, /\{t\("可能"\)\}\{maybe\}\{t\("为空"\)\}/);
});
