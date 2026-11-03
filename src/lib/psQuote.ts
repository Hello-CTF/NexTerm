// PowerShell treats U+2018-U+201B like the ASCII apostrophe, so each of them is doubled as well
// (mirrors internal/platform/psquote).
export function psQuote(value: string): string {
  return `'${value.replace(/['‘-‛]/g, (quote) => quote + quote)}'`;
}
