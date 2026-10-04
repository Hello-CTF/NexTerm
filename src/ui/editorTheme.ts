
import {
  HighlightStyle,
  defaultHighlightStyle,
  syntaxHighlighting,
} from "@codemirror/language";
import { Prec } from "@codemirror/state";

const TOKEN_COLORS: Record<string, string> = {
  keyword: "#b79ce8",
  controlKeyword: "#b79ce8",
  operatorKeyword: "#b79ce8",
  definitionKeyword: "#b79ce8",
  moduleKeyword: "#b79ce8",
  self: "#b79ce8",
  modifier: "#b79ce8",
  processingInstruction: "#b79ce8",

  string: "#7fd6a4",
  special: "#7fc7d6",
  regexp: "#7fc7d6",
  escape: "#7fc7d6",
  character: "#7fd6a4",

  number: "#e9c489",
  integer: "#e9c489",
  float: "#e9c489",
  bool: "#e9c489",
  atom: "#e9c489",
  null: "#e9c489",
  unit: "#e9c489",

  comment: "#8792a2",
  lineComment: "#8792a2",
  blockComment: "#8792a2",
  docComment: "#8792a2",

  function: "#79a8f8",
  definition: "#9dc0fb",
  typeName: "#9dc0fb",
  className: "#9dc0fb",
  namespace: "#9dc0fb",
  tagName: "#79a8f8",
  labelName: "#9dc0fb",
  macroName: "#b79ce8",

  propertyName: "#7fc7d6",
  attributeName: "#7fc7d6",
  variableName: "#c6cbd6",
  constant: "#e9c489",
  standard: "#e9c489",
  local: "#c6cbd6",

  operator: "#8a919f",
  punctuation: "#8a919f",
  bracket: "#8a919f",
  separator: "#8a919f",
  meta: "#8a919f",
  annotation: "#8a919f",

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

const FALLBACK = "#aab2c0";

function tagName(tag: unknown): string {
  const t = tag as { name?: string };
  return typeof t?.name === "string" ? t.name : String(tag);
}

function darkHighlight(): HighlightStyle {
  const specs = defaultHighlightStyle.specs.map((spec) => {
    const tags = Array.isArray(spec.tag) ? spec.tag : [spec.tag];
    const color = tags.map(tagName).reduce<string | undefined>((hit, name) => hit ?? TOKEN_COLORS[name], undefined);
    return { ...spec, color: color ?? FALLBACK };
  });
  return HighlightStyle.define(specs);
}

export const nxHighlight = Prec.highest(syntaxHighlighting(darkHighlight()));
