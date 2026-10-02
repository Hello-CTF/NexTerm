// CodeMirror 深色语法高亮。
//
// 为什么需要这个文件：`basicSetup` 自带的是**浅色**高亮方案
// （keyword #708、string #a11、comment #940 这类深色），
// 落在我们的 #101217 画布上几乎读不出来。
//
// 而自定义高亮需要一个 Tag 对象表，它来自 @lezer/highlight —— 那不是本项目的直接依赖
// （pnpm 严格模式下不可直接 import）。于是换个思路：
// 直接复用内置样式的 `specs`（Tag 对象 + class 名都在里面），
// 按 Tag 的语义名重新配色即可，既不新增依赖，也不受版本改名影响。

import {
  HighlightStyle,
  defaultHighlightStyle,
  syntaxHighlighting,
} from "@codemirror/language";
import { Prec } from "@codemirror/state";

/**
 * Tag 语义名 → 颜色。
 *
 * ⚠️ 这套色板跟的是**终端 ANSI 色板**（`XtermView.tsx` 的 `THEME`），
 * 不是界面强调色（`styles.css` 的 `--color-accent`）。两者是两条独立的轴：
 * 强调色管界面 chrome（选中态、主按钮），只在关键动作上出现；
 * 这里管内容语义（函数/类型/字符串），需要同时出现好几种颜色且都读得清。
 * **换界面强调色时不要顺手改这里**，否则编辑器和终端会脱钩。
 */
const TOKEN_COLORS: Record<string, string> = {
  // 关键字 / 控制流 / 修饰符
  keyword: "#b79ce8",
  controlKeyword: "#b79ce8",
  operatorKeyword: "#b79ce8",
  definitionKeyword: "#b79ce8",
  moduleKeyword: "#b79ce8",
  self: "#b79ce8",
  modifier: "#b79ce8",
  processingInstruction: "#b79ce8",

  // 字符串 / 正则
  string: "#7fd6a4",
  special: "#7fc7d6",
  regexp: "#7fc7d6",
  escape: "#7fc7d6",
  character: "#7fd6a4",

  // 数字 / 常量
  number: "#e9c489",
  integer: "#e9c489",
  float: "#e9c489",
  bool: "#e9c489",
  atom: "#e9c489",
  null: "#e9c489",
  unit: "#e9c489",

  // 注释
  comment: "#8792a2",
  lineComment: "#8792a2",
  blockComment: "#8792a2",
  docComment: "#8792a2",

  // 函数 / 定义 / 类型
  function: "#79a8f8",
  definition: "#9dc0fb",
  typeName: "#9dc0fb",
  className: "#9dc0fb",
  namespace: "#9dc0fb",
  tagName: "#79a8f8",
  labelName: "#9dc0fb",
  macroName: "#b79ce8",

  // 属性 / 变量
  propertyName: "#7fc7d6",
  attributeName: "#7fc7d6",
  variableName: "#c6cbd6",
  constant: "#e9c489",
  standard: "#e9c489",
  local: "#c6cbd6",

  // 标点 / 元信息
  operator: "#8a919f",
  punctuation: "#8a919f",
  bracket: "#8a919f",
  separator: "#8a919f",
  meta: "#8a919f",
  annotation: "#8a919f",

  // 文本语义
  heading: "#eef1f6",
  strong: "#eef1f6",
  emphasis: "#c6cbd6",
  link: "#79a8f8",
  url: "#79a8f8",
  invalid: "#f0a0a0",
  strikethrough: "#8a919f",
  content: "#c6cbd6",
  monospace: "#c6cbd6",
};

/** 没匹配上语义名时的兜底色：统一中性灰，保证至少可读。 */
const FALLBACK = "#aab2c0";

function tagName(tag: unknown): string {
  const t = tag as { name?: string };
  return typeof t?.name === "string" ? t.name : String(tag);
}

/**
 * 基于内置样式的 specs 重新着色。
 *
 * 保留原有的非颜色属性（斜体、下划线等），只替换 `color`，
 * 这样注释仍是斜体、链接仍带下划线。
 */
function darkHighlight(): HighlightStyle {
  const specs = defaultHighlightStyle.specs.map((spec) => {
    const tags = Array.isArray(spec.tag) ? spec.tag : [spec.tag];
    const color = tags.map(tagName).reduce<string | undefined>((hit, name) => hit ?? TOKEN_COLORS[name], undefined);
    return { ...spec, color: color ?? FALLBACK };
  });
  return HighlightStyle.define(specs);
}

/** 编辑器共用扩展：深色语法高亮（最高优先级，压过 basicSetup 里的浅色方案）。 */
export const nxHighlight = Prec.highest(syntaxHighlighting(darkHighlight()));
