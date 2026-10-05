
import {
  HighlightStyle,
  defaultHighlightStyle,
  syntaxHighlighting,
} from "@codemirror/language";
import { Prec } from "@codemirror/state";

const TOKEN_COLORS: Record<string, string> = {
  keyword: "var(--nx-cm-keyword)",
  controlKeyword: "var(--nx-cm-keyword)",
  operatorKeyword: "var(--nx-cm-keyword)",
  definitionKeyword: "var(--nx-cm-keyword)",
  moduleKeyword: "var(--nx-cm-keyword)",
  self: "var(--nx-cm-keyword)",
  modifier: "var(--nx-cm-keyword)",
  processingInstruction: "var(--nx-cm-keyword)",
  macroName: "var(--nx-cm-keyword)",

  string: "var(--nx-cm-string)",
  character: "var(--nx-cm-string)",

  special: "var(--nx-cm-special)",
  regexp: "var(--nx-cm-special)",
  escape: "var(--nx-cm-special)",
  propertyName: "var(--nx-cm-special)",
  attributeName: "var(--nx-cm-special)",

  number: "var(--nx-cm-number)",
  integer: "var(--nx-cm-number)",
  float: "var(--nx-cm-number)",
  bool: "var(--nx-cm-number)",
  atom: "var(--nx-cm-number)",
  null: "var(--nx-cm-number)",
  unit: "var(--nx-cm-number)",
  constant: "var(--nx-cm-number)",
  standard: "var(--nx-cm-number)",

  comment: "var(--nx-cm-comment)",
  lineComment: "var(--nx-cm-comment)",
  blockComment: "var(--nx-cm-comment)",
  docComment: "var(--nx-cm-comment)",

  function: "var(--nx-cm-function)",
  tagName: "var(--nx-cm-function)",
  link: "var(--nx-cm-function)",
  url: "var(--nx-cm-function)",

  definition: "var(--nx-cm-type)",
  typeName: "var(--nx-cm-type)",
  className: "var(--nx-cm-type)",
  namespace: "var(--nx-cm-type)",
  labelName: "var(--nx-cm-type)",

  variableName: "var(--nx-cm-variable)",
  local: "var(--nx-cm-variable)",
  emphasis: "var(--nx-cm-variable)",
  content: "var(--nx-cm-variable)",
  monospace: "var(--nx-cm-variable)",

  operator: "var(--nx-cm-operator)",
  punctuation: "var(--nx-cm-operator)",
  bracket: "var(--nx-cm-operator)",
  separator: "var(--nx-cm-operator)",
  meta: "var(--nx-cm-operator)",
  annotation: "var(--nx-cm-operator)",
  strikethrough: "var(--nx-cm-operator)",

  heading: "var(--nx-cm-heading)",
  strong: "var(--nx-cm-heading)",

  invalid: "var(--nx-cm-invalid)",
};

const FALLBACK = "var(--nx-cm-fallback)";

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
