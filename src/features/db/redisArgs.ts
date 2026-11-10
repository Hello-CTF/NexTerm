export type RedisArgsError = "unbalanced-quotes" | "invalid-utf8";

export type RedisArgsResult = { ok: true; args: string[] } | { ok: false; error: RedisArgsError };

const DOUBLE_QUOTE_ESCAPES: Record<string, number> = { n: 10, r: 13, t: 9, b: 8, a: 7 };

function isSpace(c: string | undefined): boolean {
  return c === " " || c === "\t" || c === "\n" || c === "\r" || c === "\f" || c === "\v";
}

function isHex(c: string | undefined): boolean {
  return c !== undefined && /^[0-9a-fA-F]$/.test(c);
}

const encoder = new TextEncoder();
const decoder = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true });

// parseRedisArgs splits a console line like redis-cli: whitespace separates
// arguments, double quotes accept \n \r \t \b \a \xHH and backslash escapes,
// single quotes are literal except \', and a closing quote must end the
// argument. Arguments are sent as text, so \xHH bytes must form valid UTF-8.
export function parseRedisArgs(line: string): RedisArgsResult {
  const args: string[] = [];
  const chars = [...line];
  let i = 0;
  while (true) {
    while (isSpace(chars[i])) i++;
    if (i >= chars.length) return { ok: true, args };
    const bytes: number[] = [];
    const push = (c: string) => bytes.push(...encoder.encode(c));
    let quote: '"' | "'" | null = null;
    let done = false;
    while (!done) {
      const c = chars[i];
      if (quote === '"') {
        if (c === undefined) return { ok: false, error: "unbalanced-quotes" };
        if (c === "\\" && chars[i + 1] === "x" && isHex(chars[i + 2]) && isHex(chars[i + 3])) {
          bytes.push(parseInt(chars[i + 2] + chars[i + 3], 16));
          i += 4;
          continue;
        }
        if (c === "\\" && chars[i + 1] !== undefined) {
          const escaped = chars[i + 1];
          const control = DOUBLE_QUOTE_ESCAPES[escaped];
          if (control !== undefined) bytes.push(control);
          else push(escaped);
          i += 2;
          continue;
        }
        if (c === '"') {
          if (chars[i + 1] !== undefined && !isSpace(chars[i + 1])) return { ok: false, error: "unbalanced-quotes" };
          done = true;
        } else {
          push(c);
        }
      } else if (quote === "'") {
        if (c === undefined) return { ok: false, error: "unbalanced-quotes" };
        if (c === "\\" && chars[i + 1] === "'") {
          push("'");
          i += 2;
          continue;
        }
        if (c === "'") {
          if (chars[i + 1] !== undefined && !isSpace(chars[i + 1])) return { ok: false, error: "unbalanced-quotes" };
          done = true;
        } else {
          push(c);
        }
      } else if (c === undefined || isSpace(c)) {
        done = true;
      } else if (c === '"' || c === "'") {
        quote = c;
      } else {
        push(c);
      }
      if (i < chars.length) i++;
    }
    try {
      args.push(decoder.decode(new Uint8Array(bytes)));
    } catch {
      return { ok: false, error: "invalid-utf8" };
    }
  }
}
