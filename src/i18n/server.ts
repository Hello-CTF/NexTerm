// Backend messages reach the interface already written in Chinese. The server catalog maps each Go format string to
// its translation; a message is matched against the templates and its arguments are carried over, translated in turn
// when they are messages themselves (a wrapped error).

type Entry = string | { other: string };

interface Template {
  matcher: RegExp;
  target: string;
  verbs: string[];
}

export interface ServerCatalog {
  exact: Map<string, string>;
  templates: Template[];
}

const VERB = /%(?:\[\d+\])?[-+# 0]*\d*(?:\.\d+)?[sdvqwxXfgtT]/g;
const CJK = /[一-鿿]/;
const MAX_DEPTH = 3;

function escapeRegExp(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function verbPattern(verb: string): string {
  const kind = verb.at(-1);
  if (kind === "d") return "(-?\\d+)";
  if (kind === "f" || kind === "g") return "(-?[\\d.eE+-]+)";
  return "([\\s\\S]+?)";
}

function textOf(entry: Entry): string {
  return typeof entry === "string" ? entry : entry.other;
}

export function compileServerCatalog(catalog: Record<string, Entry>): ServerCatalog {
  const exact = new Map<string, string>();
  const templates: Template[] = [];
  for (const [source, entry] of Object.entries(catalog)) {
    const target = textOf(entry);
    const verbs = source.match(VERB) ?? [];
    if (verbs.length === 0) {
      exact.set(source.replaceAll("%%", "%"), target.replaceAll("%%", "%"));
      continue;
    }
    const literals = source.split(VERB).map((part) => escapeRegExp(part.replaceAll("%%", "%")));
    let pattern = literals[0];
    verbs.forEach((verb, index) => {
      pattern += verbPattern(verb) + literals[index + 1];
    });
    templates.push({ matcher: new RegExp(`^${pattern}$`), target, verbs });
  }
  // Longer literal text first, so a specific template wins over a general one.
  templates.sort((a, b) => b.matcher.source.length - a.matcher.source.length);
  return { exact, templates };
}

function fill(target: string, args: string[]): string {
  let next = 0;
  return target
    .replace(VERB, (verb) => {
      const indexed = /^%\[(\d+)\]/.exec(verb);
      const index = indexed ? Number(indexed[1]) - 1 : next++;
      return args[index] ?? "";
    })
    .replaceAll("%%", "%");
}

export function translateServer(catalog: ServerCatalog, text: string, depth = 0): string {
  if (depth > MAX_DEPTH || !CJK.test(text)) return text;
  const exact = catalog.exact.get(text);
  if (exact !== undefined) return exact;
  for (const template of catalog.templates) {
    const match = template.matcher.exec(text);
    if (!match) continue;
    const args = match.slice(1).map((arg, index) => {
      const kind = template.verbs[index]?.at(-1);
      return kind === "s" || kind === "v" || kind === "w" || kind === "q" ? translateServer(catalog, arg, depth + 1) : arg;
    });
    return fill(template.target, args);
  }
  return text;
}
