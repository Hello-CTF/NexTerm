import { isLocale, LOCALE_NAMES, LOCALES, resolveLocale, SOURCE_LOCALE, storeLocale, storedLocale, type Locale } from "./locales";
import { compileServerCatalog, translateServer, type ServerCatalog } from "./server";

export { LOCALE_NAMES, LOCALES, SOURCE_LOCALE, isLocale, storedLocale, type Locale };

// A catalog entry is the translation, or plural forms chosen by the count parameter under the language's plural rules.
export type PluralForms = Partial<Record<Intl.LDMLPluralRule, string>> & { other: string };
export type CatalogEntry = string | PluralForms;
export type Catalog = Record<string, CatalogEntry>;

// One file per language and kind: ui holds the frontend's text, server the backend's messages (Go format templates).
const catalogFiles = import.meta.glob<{ default: Catalog }>("./locales/*/*.json");

let current: Locale = SOURCE_LOCALE;
let ui: Catalog = {};
let server: ServerCatalog = compileServerCatalog({});
let plurals = new Intl.PluralRules(SOURCE_LOCALE);

async function loadCatalog(locale: Locale, kind: "ui" | "server"): Promise<Catalog> {
  const load = catalogFiles[`./locales/${locale}/${kind}.json`];
  if (!load) return {};
  return (await load()).default;
}

// initI18n loads the language's catalogs. It runs before the interface modules load, so text built when a module
// loads is already translated. A catalog that fails to load leaves the interface in the source language.
export async function initI18n(locale: Locale = resolveLocale()): Promise<void> {
  applyLocale(locale, {}, {});
  if (locale === SOURCE_LOCALE) return;
  try {
    const [uiCatalog, serverCatalog] = await Promise.all([loadCatalog(locale, "ui"), loadCatalog(locale, "server")]);
    applyLocale(locale, uiCatalog, serverCatalog);
  } catch (error) {
    console.warn("i18n: catalog failed to load", error);
  }
}

// applyLocale installs catalogs directly; initI18n and tests use it.
export function applyLocale(locale: Locale, uiCatalog: Catalog, serverCatalog: Catalog): void {
  current = locale;
  ui = uiCatalog;
  server = compileServerCatalog(serverCatalog);
  plurals = new Intl.PluralRules(locale);
  if (typeof document !== "undefined") document.documentElement.lang = locale;
}

export function getLocale(): Locale {
  return current;
}

// setLocale saves the choice (null follows the system) and reloads, because text is built as modules load.
export function setLocale(locale: Locale | null): void {
  storeLocale(locale);
  globalThis.location?.reload();
}

// Values are converted the way a template literal converts them, so wrapping a template in t() keeps its output.
export type TranslateParams = Record<string, unknown>;

function interpolate(text: string, params: TranslateParams | undefined): string {
  if (!params) return text;
  return text.replace(/\{(\w+)\}/g, (whole, name: string) => (name in params ? `${params[name] as string}` : whole));
}

function pick(entry: CatalogEntry, params: TranslateParams | undefined): string {
  if (typeof entry === "string") return entry;
  const count = Number(params?.count);
  return (Number.isFinite(count) ? entry[plurals.select(count)] : undefined) ?? entry.other;
}

// t translates interface text. The key is the Simplified Chinese text itself, with {name} placeholders filled from
// params; a catalog entry may give plural forms, chosen by params.count. A key missing from the catalog shows as is.
export function t(source: string, params?: TranslateParams): string {
  const entry = ui[source];
  return interpolate(entry === undefined ? source : pick(entry, params), params);
}

// translateMessage translates text that arrives already written, such as a backend error: an exact catalog match
// first, then the backend's format templates. Text in no catalog stays as it is.
export function translateMessage(text: string): string {
  if (current === SOURCE_LOCALE || !text) return text;
  const exact = ui[text];
  if (typeof exact === "string") return exact;
  return translateServer(server, text);
}

// formatNumber and formatDateTime format for the interface language.
export function formatNumber(value: number, options?: Intl.NumberFormatOptions): string {
  return new Intl.NumberFormat(current, options).format(value);
}

export function formatDateTime(value: number | Date, options?: Intl.DateTimeFormatOptions): string {
  return new Intl.DateTimeFormat(current, options).format(value);
}
