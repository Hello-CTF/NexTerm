#!/usr/bin/env node
// Interface translation tooling. Simplified Chinese in the code is the source text and the key of every catalog.
//
//   node scripts/i18n.mjs check [--strict]            stale keys, placeholder and plural mismatches; --strict also
//                                                     fails on missing translations
//   node scripts/i18n.mjs status                      translated / total per language and kind
//   node scripts/i18n.mjs sync                        drop stale keys and sort every catalog
//   node scripts/i18n.mjs missing <locale> <kind> [--offset N] [--limit N] [--out file]
//                                                     untranslated source keys as a JSON array
//   node scripts/i18n.mjs merge <locale> <kind> <file> merge a JSON object of translations after validating it
//   node scripts/i18n.mjs validate <locale> <kind> <file>  report the problems of a translation file
//   node scripts/i18n.mjs chunks <kind> <size> <dir> [--locale <locale>]
//                                                     untranslated keys with a file that uses each, in numbered files
//   node scripts/i18n.mjs wrap [--dry] <file|dir>...  wrap Chinese JSX text and string literals in t()
//
// Kinds: ui is the frontend's t() keys; server is the backend's Chinese string literals (Go format strings).
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const SRC = path.join(ROOT, "src");
const I18N_DIR = path.join(SRC, "i18n");
const LOCALES_DIR = path.join(I18N_DIR, "locales");
const SOURCE_LOCALE = "zh-CN";
const KINDS = ["ui", "server"];
const CJK = /[一-鿿]/;
const PLURAL_FORMS = new Set(["zero", "one", "two", "few", "many", "other"]);
const SKIP_DIRS = new Set(["test", "bindings", "generated", "i18n", "node_modules"]);
const GO_DIRS = ["internal", "cmd"];

function die(message) {
  console.error(`i18n: ${message}`);
  process.exit(1);
}

function readLocales() {
  const source = fs.readFileSync(path.join(I18N_DIR, "locales.ts"), "utf8");
  const match = /export const LOCALES = \[([^\]]+)\]/.exec(source);
  if (!match) die("LOCALES not found in src/i18n/locales.ts");
  return [...match[1].matchAll(/"([^"]+)"/g)].map((m) => m[1]).filter((locale) => locale !== SOURCE_LOCALE);
}

function walk(dir, accept, out = []) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    if (entry.isDirectory()) {
      if (!SKIP_DIRS.has(entry.name) && !entry.name.startsWith(".")) walk(path.join(dir, entry.name), accept, out);
    } else if (accept(entry.name)) {
      out.push(path.join(dir, entry.name));
    }
  }
  return out;
}

const isTsSource = (name) => /\.(ts|tsx)$/.test(name) && !/\.d\.ts$/.test(name) && !/\.test\.tsx?$/.test(name);

function parse(file, text = fs.readFileSync(file, "utf8")) {
  const kind = file.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS;
  return ts.createSourceFile(file, text, ts.ScriptTarget.Latest, true, kind);
}

// ---------- source keys ----------

// translatorNames returns the local names bound to the i18n t function in a file.
function translatorNames(sf) {
  const names = new Set();
  for (const statement of sf.statements) {
    if (!ts.isImportDeclaration(statement) || !ts.isStringLiteral(statement.moduleSpecifier)) continue;
    if (!/(^|\/)i18n$/.test(statement.moduleSpecifier.text)) continue;
    const bindings = statement.importClause?.namedBindings;
    if (!bindings || !ts.isNamedImports(bindings)) continue;
    for (const element of bindings.elements) {
      if ((element.propertyName ?? element.name).text === "t") names.add(element.name.text);
    }
  }
  return names;
}

function uiKeys() {
  const keys = new Map();
  for (const file of walk(SRC, isTsSource)) {
    const sf = parse(file);
    const names = translatorNames(sf);
    if (names.size === 0) continue;
    const visit = (node) => {
      if (ts.isCallExpression(node) && ts.isIdentifier(node.expression) && names.has(node.expression.text)) {
        const [first] = node.arguments;
        if (first && (ts.isStringLiteral(first) || ts.isNoSubstitutionTemplateLiteral(first))) {
          if (!keys.has(first.text)) keys.set(first.text, path.relative(ROOT, file));
        } else if (first) {
          const { line } = sf.getLineAndCharacterOfPosition(node.getStart());
          console.warn(`i18n: ${path.relative(ROOT, file)}:${line + 1}: t() needs a literal key`);
        }
      }
      ts.forEachChild(node, visit);
    };
    visit(sf);
  }
  return keys;
}

// Go interpreted string literals with Chinese text outside tests and comments: the messages the backend can show.
function unquoteGo(body) {
  return body.replace(/\\(u[0-9a-fA-F]{4}|U[0-9a-fA-F]{8}|x[0-9a-fA-F]{2}|[0-7]{3}|.)/g, (_, esc) => {
    switch (esc[0]) {
      case "n": return "\n";
      case "t": return "\t";
      case "r": return "\r";
      case "u":
      case "U": return String.fromCodePoint(parseInt(esc.slice(1), 16));
      case "x": return String.fromCharCode(parseInt(esc.slice(1), 16));
      default:
        return /^[0-7]{3}$/.test(esc) ? String.fromCharCode(parseInt(esc, 8)) : esc;
    }
  });
}

function stripGoComments(text) {
  let out = "";
  let i = 0;
  while (i < text.length) {
    const c = text[i];
    if (c === "/" && text[i + 1] === "/") {
      while (i < text.length && text[i] !== "\n") i++;
    } else if (c === "/" && text[i + 1] === "*") {
      const end = text.indexOf("*/", i + 2);
      i = end < 0 ? text.length : end + 2;
    } else if (c === '"' || c === "`" || c === "'") {
      const start = i++;
      while (i < text.length && text[i] !== c) {
        if (c !== "`" && text[i] === "\\") i++;
        i++;
      }
      out += text.slice(start, ++i);
    } else {
      out += c;
      i++;
    }
  }
  return out;
}

function serverKeys() {
  const keys = new Map();
  for (const dir of GO_DIRS) {
    const files = walk(path.join(ROOT, dir), (name) => name.endsWith(".go") && !name.endsWith("_test.go"));
    for (const file of files) {
      const text = stripGoComments(fs.readFileSync(file, "utf8"));
      for (const match of text.matchAll(/"((?:[^"\\\n]|\\.)*)"/g)) {
        const value = unquoteGo(match[1]);
        if (CJK.test(value) && !keys.has(value)) keys.set(value, path.relative(ROOT, file));
      }
    }
  }
  return keys;
}

function sourceKeys(kind) {
  return kind === "ui" ? uiKeys() : serverKeys();
}

// ---------- catalogs ----------

function catalogPath(locale, kind) {
  return path.join(LOCALES_DIR, locale, `${kind}.json`);
}

function readCatalog(locale, kind) {
  const file = catalogPath(locale, kind);
  if (!fs.existsSync(file)) return {};
  try {
    return JSON.parse(fs.readFileSync(file, "utf8"));
  } catch (error) {
    die(`${path.relative(ROOT, file)}: ${error.message}`);
  }
}

function writeCatalog(locale, kind, catalog) {
  const sorted = Object.fromEntries(Object.keys(catalog).sort().map((key) => [key, catalog[key]]));
  fs.mkdirSync(path.dirname(catalogPath(locale, kind)), { recursive: true });
  fs.writeFileSync(catalogPath(locale, kind), `${JSON.stringify(sorted, null, 2)}\n`);
}

const UI_PLACEHOLDER = /\{(\w+)\}/g;
const GO_VERB = /%(?:\[\d+\])?[-+# 0]*\d*(?:\.\d+)?[sdvqwxXfgtT]/g;

function placeholders(kind, text) {
  if (kind === "ui") return [...new Set([...text.matchAll(UI_PLACEHOLDER)].map((m) => m[1]))].sort().join(",");
  return (text.match(GO_VERB) ?? []).map((verb) => verb.replace(/^%\[\d+\]/, "%")).sort().join(",");
}

// entryProblems lists what is wrong with one translation of a source key.
function entryProblems(kind, key, value) {
  const want = placeholders(kind, key);
  const forms = typeof value === "string" ? { other: value } : value;
  if (!forms || typeof forms !== "object" || Array.isArray(forms)) return ["must be a string or plural forms"];
  if (typeof forms.other !== "string") return ["plural forms need other"];
  const problems = [];
  for (const [form, text] of Object.entries(forms)) {
    if (!PLURAL_FORMS.has(form)) problems.push(`unknown plural form ${form}`);
    else if (typeof text !== "string" || text.trim() === "") problems.push(`empty ${form}`);
    else if (placeholders(kind, text) !== want) problems.push(`placeholders ${placeholders(kind, text) || "none"} != ${want || "none"}`);
  }
  if (kind === "ui" && typeof value !== "string" && !/\{\w+\}/.test(key)) problems.push("plural forms need a key with a placeholder");
  return problems;
}

function check(strict) {
  const locales = readLocales();
  let failed = false;
  for (const kind of KINDS) {
    const keys = sourceKeys(kind);
    for (const locale of locales) {
      const catalog = readCatalog(locale, kind);
      let missing = 0;
      for (const key of keys.keys()) if (!(key in catalog)) missing++;
      for (const [key, value] of Object.entries(catalog)) {
        if (!keys.has(key)) {
          console.error(`${locale}/${kind}: stale key ${JSON.stringify(key)}`);
          failed = true;
          continue;
        }
        for (const problem of entryProblems(kind, key, value)) {
          console.error(`${locale}/${kind}: ${JSON.stringify(key)}: ${problem}`);
          failed = true;
        }
      }
      if (missing > 0) {
        console[strict ? "error" : "log"](`${locale}/${kind}: ${missing} of ${keys.size} untranslated`);
        if (strict) failed = true;
      }
    }
  }
  if (failed) die("catalog check failed (node scripts/i18n.mjs sync drops stale keys)");
  console.log("i18n: catalogs ok");
}

function status() {
  const locales = readLocales();
  for (const kind of KINDS) {
    const keys = sourceKeys(kind);
    for (const locale of locales) {
      const catalog = readCatalog(locale, kind);
      const done = [...keys.keys()].filter((key) => key in catalog).length;
      console.log(`${locale.padEnd(6)} ${kind.padEnd(6)} ${done}/${keys.size}`);
    }
  }
}

function sync() {
  const locales = readLocales();
  for (const kind of KINDS) {
    const keys = sourceKeys(kind);
    for (const locale of locales) {
      const catalog = readCatalog(locale, kind);
      const kept = Object.fromEntries(Object.entries(catalog).filter(([key]) => keys.has(key)));
      writeCatalog(locale, kind, kept);
    }
  }
  console.log("i18n: catalogs synced");
}

function option(args, name, fallback) {
  const index = args.indexOf(name);
  return index >= 0 ? args[index + 1] : fallback;
}

function requireTarget(locale, kind) {
  if (!readLocales().includes(locale)) die(`unknown locale ${locale}`);
  if (!KINDS.includes(kind)) die(`unknown kind ${kind}`);
}

function missing(args) {
  const [locale, kind] = args;
  requireTarget(locale, kind);
  const catalog = readCatalog(locale, kind);
  const offset = Number(option(args, "--offset", "0"));
  const limit = Number(option(args, "--limit", "1000000"));
  const keys = [...sourceKeys(kind).keys()].filter((key) => !(key in catalog)).sort().slice(offset, offset + limit);
  const out = option(args, "--out", null);
  const json = `${JSON.stringify(keys, null, 2)}\n`;
  if (out) fs.writeFileSync(out, json);
  else process.stdout.write(json);
  if (out) console.log(`i18n: ${keys.length} keys written to ${out}`);
}

// chunks writes the untranslated keys of a kind, each with a file that uses it, into numbered files of size keys.
function chunks(args) {
  const [kind, sizeText, dir] = args;
  if (!KINDS.includes(kind) || !dir) die("usage: chunks <kind> <size> <dir> [--locale <locale>]");
  const locale = option(args, "--locale", null);
  const catalog = locale ? readCatalog(locale, kind) : {};
  const size = Number(sizeText) || 300;
  const entries = [...sourceKeys(kind).entries()].filter(([key]) => !(key in catalog)).sort(([a], [b]) => (a < b ? -1 : 1));
  fs.mkdirSync(dir, { recursive: true });
  let count = 0;
  for (let i = 0; i < entries.length; i += size) {
    const part = entries.slice(i, i + size).map(([key, file]) => ({ key, file }));
    fs.writeFileSync(path.join(dir, `${kind}-${String(++count).padStart(3, "0")}.json`), `${JSON.stringify(part, null, 1)}\n`);
  }
  console.log(`i18n: ${entries.length} ${kind} keys in ${count} chunks under ${dir}`);
}

// validate reports the problems of a translation file without merging it.
function validate(args) {
  const [locale, kind, file] = args;
  requireTarget(locale, kind);
  if (!file) die("validate needs a file");
  const additions = JSON.parse(fs.readFileSync(file, "utf8"));
  const keys = sourceKeys(kind);
  let problems = 0;
  for (const [key, value] of Object.entries(additions)) {
    const found = keys.has(key) ? entryProblems(kind, key, value) : ["not a source key"];
    for (const problem of found) {
      console.error(`${JSON.stringify(key)}: ${problem}`);
      problems++;
    }
  }
  console.log(`i18n: ${Object.keys(additions).length} entries, ${problems} problems`);
  if (problems > 0) process.exitCode = 2;
}

function merge(args) {
  const [locale, kind, file] = args;
  requireTarget(locale, kind);
  if (!file) die("merge needs a file");
  const additions = JSON.parse(fs.readFileSync(file, "utf8"));
  const keys = sourceKeys(kind);
  const catalog = readCatalog(locale, kind);
  let merged = 0;
  const rejected = [];
  for (const [key, value] of Object.entries(additions)) {
    if (!keys.has(key)) {
      rejected.push(`${JSON.stringify(key)}: not a source key`);
      continue;
    }
    const problems = entryProblems(kind, key, value);
    if (problems.length > 0) {
      rejected.push(`${JSON.stringify(key)}: ${problems.join("; ")}`);
      continue;
    }
    catalog[key] = value;
    merged++;
  }
  writeCatalog(locale, kind, catalog);
  for (const line of rejected) console.error(`rejected ${line}`);
  console.log(`i18n: merged ${merged}, rejected ${rejected.length} into ${locale}/${kind}`);
  if (rejected.length > 0) process.exitCode = 2;
}

// ---------- wrap ----------

// Strings that are matched or compared rather than shown keep their Chinese: these method calls compare text.
const MATCHING_METHODS = new Set([
  "includes", "startsWith", "endsWith", "indexOf", "lastIndexOf", "split", "replace", "replaceAll", "match",
  "matchAll", "test", "exec", "localeCompare", "has", "get", "delete", "search",
]);
const COMPARISON = new Set([
  ts.SyntaxKind.EqualsEqualsEqualsToken, ts.SyntaxKind.ExclamationEqualsEqualsToken,
  ts.SyntaxKind.EqualsEqualsToken, ts.SyntaxKind.ExclamationEqualsToken, ts.SyntaxKind.InKeyword,
]);
const SKIPPED_CALLEES = new Set(["console", "RegExp", "describe", "it", "test"]);
const REVIEW_REASONS = new Set(["comparison", "matching", "case", "index", "key", "enum", "type"]);

function calleeRoot(expression) {
  let node = expression;
  while (ts.isPropertyAccessExpression(node) || ts.isCallExpression(node)) node = node.expression;
  return ts.isIdentifier(node) ? node.text : "";
}

// skipReason explains why a Chinese literal is left alone, or returns null when it is shown text.
function skipReason(node, names) {
  const parent = node.parent;
  if (!parent) return "root";
  if (ts.isImportDeclaration(parent) || ts.isExportDeclaration(parent) || ts.isExternalModuleReference(parent)) return "module";
  if (ts.isLiteralTypeNode(parent)) return "type";
  if (ts.isCaseClause(parent)) return "case";
  if ((ts.isPropertyAssignment(parent) || ts.isPropertySignature(parent) || ts.isMethodDeclaration(parent) || ts.isEnumMember(parent)) && parent.name === node) return "key";
  if (ts.isEnumMember(parent)) return "enum";
  if (ts.isElementAccessExpression(parent) && parent.argumentExpression === node) return "index";
  if (ts.isBinaryExpression(parent) && COMPARISON.has(parent.operatorToken.kind)) return "comparison";
  if (ts.isCallExpression(parent) || ts.isNewExpression(parent)) {
    const callee = parent.expression;
    if (ts.isIdentifier(callee) && names.has(callee.text)) return "translated";
    if (ts.isPropertyAccessExpression(callee) && MATCHING_METHODS.has(callee.name.text)) return "matching";
    if (SKIPPED_CALLEES.has(calleeRoot(callee))) return "skipped call";
  }
  if (ts.isExpressionStatement(parent)) return "directive";
  return null;
}

function bindsName(sf, name) {
  let found = false;
  const visit = (node) => {
    if (found) return;
    if (
      (ts.isParameter(node) || ts.isVariableDeclaration(node) || ts.isFunctionDeclaration(node) || ts.isBindingElement(node) || ts.isImportSpecifier(node) || ts.isClassDeclaration(node)) &&
      node.name && ts.isIdentifier(node.name) && node.name.text === name
    ) {
      if (!(ts.isImportSpecifier(node) && translatorNames(sf).has(name))) found = true;
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
  return found;
}

function paramName(expression, used) {
  let base = "value";
  if (ts.isIdentifier(expression)) base = expression.text;
  else if (ts.isPropertyAccessExpression(expression)) base = expression.name.text;
  else if (ts.isCallExpression(expression) && ts.isPropertyAccessExpression(expression.expression)) base = expression.expression.name.text;
  base = base.replace(/[^\w]/g, "") || "value";
  let name = base;
  for (let i = 2; used.has(name); i++) name = `${base}${i}`;
  used.add(name);
  return name;
}

function jsxDisplayText(raw) {
  // JSX joins the lines of a text node with single spaces and drops lines that are only whitespace.
  const lines = raw.split(/\r?\n/);
  if (lines.length === 1) return raw.trim();
  return lines.map((line) => line.replace(/\t/g, " ").trim()).filter(Boolean).join(" ");
}

function applyEdits(text, offset, edits) {
  let out = text;
  for (const edit of [...edits].sort((a, b) => b.start - a.start)) {
    out = out.slice(0, edit.start - offset) + edit.text + out.slice(edit.end - offset);
  }
  return out;
}

function importPath(file) {
  let rel = path.relative(path.dirname(file), I18N_DIR).split(path.sep).join("/");
  if (!rel.startsWith(".")) rel = `./${rel}`;
  return rel;
}

function wrapFile(file, dry) {
  const text = fs.readFileSync(file, "utf8");
  if (!CJK.test(text)) return { file, count: 0, skipped: {}, review: [] };
  const sf = parse(file, text);
  const existing = translatorNames(sf);
  let fn = [...existing][0];
  let needsImport = false;
  if (!fn) {
    fn = bindsName(sf, "t") ? "tr" : "t";
    if (fn === "tr" && bindsName(sf, "tr")) return { file, count: 0, skipped: { "name conflict": 1 }, review: [`${path.relative(ROOT, file)}: binds both t and tr; not wrapped`] };
    needsImport = true;
  }
  const names = new Set([...existing, fn]);
  const edits = [];
  const skipped = {};
  const review = [];
  const note = (reason, node) => {
    skipped[reason] = (skipped[reason] ?? 0) + 1;
    if (REVIEW_REASONS.has(reason)) {
      const { line } = sf.getLineAndCharacterOfPosition(node.getStart());
      review.push(`${path.relative(ROOT, file)}:${line + 1}: ${reason}: ${node.getText().slice(0, 60)}`);
    }
  };
  const call = (key, params) => `${fn}(${JSON.stringify(key)}${params ? `, ${params}` : ""})`;
  const take = (start, end) => {
    // Edits inside a range that is being rewritten are folded into the new text and leave the list.
    const inner = edits.filter((edit) => edit.start >= start && edit.end <= end);
    for (const edit of inner) edits.splice(edits.indexOf(edit), 1);
    return applyEdits(text.slice(start, end), start, inner);
  };

  const visit = (node) => {
    ts.forEachChild(node, visit);
    if (ts.isJsxText(node)) {
      if (!CJK.test(node.text)) return;
      const raw = node.getFullText();
      const display = jsxDisplayText(raw);
      if (!display) return;
      const leading = raw.match(/^\s*/)[0];
      const trailing = raw.slice(leading.length).match(/\s*$/)[0];
      edits.push({ start: node.getFullStart(), end: node.getEnd(), text: `${leading}{${call(display)}}${trailing}` });
      return;
    }
    if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) {
      if (!CJK.test(node.text)) return;
      if (ts.isJsxAttribute(node.parent)) {
        edits.push({ start: node.getStart(), end: node.getEnd(), text: `{${call(node.text)}}` });
        return;
      }
      if (ts.isTaggedTemplateExpression(node.parent)) return;
      const reason = skipReason(node, names);
      if (reason) {
        note(reason, node);
        return;
      }
      edits.push({ start: node.getStart(), end: node.getEnd(), text: call(node.text) });
      return;
    }
    if (ts.isTemplateExpression(node)) {
      const literal = node.head.text + node.templateSpans.map((span) => span.literal.text).join("");
      if (!CJK.test(literal) || ts.isTaggedTemplateExpression(node.parent)) return;
      const reason = skipReason(node, names);
      if (reason) {
        note(reason, node);
        return;
      }
      const used = new Set();
      let key = node.head.text;
      const params = [];
      for (const span of node.templateSpans) {
        const name = paramName(span.expression, used);
        const value = take(span.expression.getStart(), span.expression.getEnd());
        params.push(value === name ? name : `${name}: ${value}`);
        key += `{${name}}${span.literal.text}`;
      }
      edits.push({ start: node.getStart(), end: node.getEnd(), text: call(key, `{ ${params.join(", ")} }`) });
    }
  };
  visit(sf);
  if (edits.length === 0) return { file, count: 0, skipped, review };

  let out = applyEdits(text, 0, edits);
  if (needsImport) {
    const imports = sf.statements.filter((statement) => ts.isImportDeclaration(statement));
    const spec = fn === "t" ? "{ t }" : "{ t as tr }";
    const line = `import ${spec} from "${importPath(file)}";\n`;
    if (imports.length > 0) {
      const last = imports[imports.length - 1].getEnd();
      const at = out.indexOf("\n", applyEdits(text.slice(0, last), 0, edits.filter((edit) => edit.end <= last)).length) + 1;
      out = out.slice(0, at) + line + out.slice(at);
    } else {
      out = line + out;
    }
  }
  if (!dry) fs.writeFileSync(file, out);
  return { file, count: edits.length, skipped, review };
}

function wrap(args) {
  const dry = args.includes("--dry");
  const targets = args.filter((arg) => arg !== "--dry");
  if (targets.length === 0) die("wrap needs files or directories");
  const files = targets.flatMap((target) => {
    const full = path.resolve(target);
    return fs.statSync(full).isDirectory() ? walk(full, isTsSource) : [full];
  });
  let total = 0;
  const skippedTotal = {};
  const reviewTotal = [];
  for (const file of files) {
    const { count, skipped, review } = wrapFile(file, dry);
    reviewTotal.push(...review);
    total += count;
    for (const [reason, n] of Object.entries(skipped)) skippedTotal[reason] = (skippedTotal[reason] ?? 0) + n;
    if (count > 0 || Object.keys(skipped).length > 0) {
      const notes = Object.entries(skipped).map(([reason, n]) => `${reason} ${n}`).join(", ");
      console.log(`${path.relative(ROOT, file)}: ${count} wrapped${notes ? `; left: ${notes}` : ""}`);
    }
  }
  console.log(`i18n: ${dry ? "would wrap" : "wrapped"} ${total} in ${files.length} files; left: ${JSON.stringify(skippedTotal)}`);
  if (reviewTotal.length > 0) {
    console.log("\nCompared or matched Chinese text left as is; check that the text it is compared with is not translated:");
    for (const line of reviewTotal) console.log(`  ${line}`);
  }
}

export { wrapFile, entryProblems };

const isMain = process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url);
const [commandName, ...rest] = isMain ? process.argv.slice(2) : [null];
if (isMain) switch (commandName) {
  case "check": check(rest.includes("--strict")); break;
  case "status": status(); break;
  case "sync": sync(); break;
  case "missing": missing(rest); break;
  case "merge": merge(rest); break;
  case "chunks": chunks(rest); break;
  case "validate": validate(rest); break;
  case "wrap": wrap(rest); break;
  default: die("usage: node scripts/i18n.mjs check|status|sync|missing|merge|wrap (see the header)");
}
