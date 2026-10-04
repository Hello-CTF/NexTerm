const STYLE_ELEMENT_ID = "nx-md-highlight-theme";

const TOKEN_COLORS: Record<string, [string, string]> = {
  keyword: ["#b79ce8", "#6440b8"],
  processingInstruction: ["#b79ce8", "#6440b8"],
  atom: ["#e9c489", "#8a5a00"],
  number: ["#e9c489", "#8a5a00"],
  def: ["#79a8f8", "#3450c0"],
  builtin: ["#79a8f8", "#3450c0"],
  tag: ["#79a8f8", "#3450c0"],
  link: ["#79a8f8", "#3450c0"],
  url: ["#79a8f8", "#3450c0"],
  "variable-2": ["#9dc0fb", "#324a9e"],
  "variable-3": ["#9dc0fb", "#324a9e"],
  qualifier: ["#9dc0fb", "#324a9e"],
  type: ["#9dc0fb", "#324a9e"],
  class: ["#9dc0fb", "#324a9e"],
  property: ["#7fc7d6", "#0f766e"],
  attribute: ["#7fc7d6", "#0f766e"],
  "string-2": ["#7fc7d6", "#0f766e"],
  string: ["#7fd6a4", "#1a6b41"],
  quote: ["#7fd6a4", "#1a6b41"],
  positive: ["#7fd6a4", "#1a6b41"],
  comment: ["#8792a2", "#5d6572"],
  meta: ["#8a919f", "#5d6572"],
  bracket: ["#8a919f", "#5d6572"],
  punctuation: ["#8a919f", "#5d6572"],
  operator: ["#8a919f", "#5d6572"],
  header: ["#eef1f6", "#171c26"],
  strong: ["#eef1f6", "#171c26"],
  em: ["#c6cbd6", "#2f3642"],
  invalid: ["#f0a0a0", "#b52a2a"],
  negative: ["#f0a0a0", "#b52a2a"],
};

const RULES = Object.entries(TOKEN_COLORS)
  .map(
    ([token, [dark, light]]) =>
      `.nx-md-pre-body .nx-tok-${token}{color:${dark}}` +
      `:root[data-nx-theme="light"] .nx-md-pre-body .nx-tok-${token}{color:${light}}`,
  )
  .join("");

const CSS = `${RULES}.nx-md-pre-body .nx-tok-em{font-style:italic}.nx-md-pre-body .nx-tok-strong{font-weight:600}`;

export function ensureHighlightTheme(): void {
  if (typeof document === "undefined") return;
  if (document.getElementById(STYLE_ELEMENT_ID)) return;
  const style = document.createElement("style");
  style.id = STYLE_ELEMENT_ID;
  style.textContent = CSS;
  document.head.append(style);
}
