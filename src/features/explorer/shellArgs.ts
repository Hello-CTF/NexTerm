import { t } from "../../i18n";

// Limits enforced by the backend (internal/store/asset_shell_options.go).
export const MAX_SHELL_ARGS = 32;
export const MAX_SHELL_ARG_BYTES = 1024;

// parseShellArgs splits the start-argument field into arguments: whitespace separates them, and single or double
// quotes keep spaces inside one argument ("-d" "My Distro"). There are no escape characters, so Windows paths keep
// their backslashes. An unterminated quote runs to the end of the text.
export function parseShellArgs(text: string): string[] {
  const args: string[] = [];
  let current = "";
  let quote: string | null = null;
  let started = false;
  for (const char of text) {
    if (quote) {
      if (char === quote) quote = null;
      else current += char;
    } else if (char === '"' || char === "'") {
      quote = char;
      started = true;
    } else if (/\s/.test(char)) {
      if (started) args.push(current);
      current = "";
      started = false;
    } else {
      current += char;
      started = true;
    }
  }
  if (started) args.push(current);
  return args;
}

// formatShellArgs is the inverse of parseShellArgs for display: an argument with whitespace or a quote, or an
// empty one, is wrapped in the quote it does not contain.
export function formatShellArgs(args: readonly string[]): string {
  return args
    .map((arg) => {
      if (arg !== "" && !/[\s"']/.test(arg)) return arg;
      return arg.includes('"') ? `'${arg}'` : `"${arg}"`;
    })
    .join(" ");
}

export function readShellArgs(options: Record<string, unknown> | undefined | null): string[] {
  const value = options?.shellArgs;
  return Array.isArray(value) ? value.filter((arg): arg is string => typeof arg === "string") : [];
}

export function shellArgsError(args: readonly string[]): string | null {
  if (args.length > MAX_SHELL_ARGS) return t("Shell 启动参数最多 {count} 个", { count: MAX_SHELL_ARGS });
  const encoder = new TextEncoder();
  for (const arg of args) {
    if (encoder.encode(arg).length > MAX_SHELL_ARG_BYTES) {
      return t("单个 Shell 启动参数不能超过 {count} 字节", { count: MAX_SHELL_ARG_BYTES });
    }
    if (/[\u0000-\u001f\u007f-\u009f]/.test(arg)) return t("Shell 启动参数不能包含控制字符");
  }
  return null;
}
