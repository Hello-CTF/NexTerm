// The interface languages. Simplified Chinese is the source language: the text in the code is the key of every
// catalog, and a string missing from a catalog shows in Chinese.
export const LOCALES = ["zh-CN", "zh-TW", "en", "ja", "ko", "de", "fr", "es", "ru"] as const;

export type Locale = (typeof LOCALES)[number];

export const SOURCE_LOCALE: Locale = "zh-CN";

// Each language named in itself, as a language menu shows it.
export const LOCALE_NAMES: Record<Locale, string> = {
  "zh-CN": "简体中文",
  "zh-TW": "繁體中文",
  en: "English",
  ja: "日本語",
  ko: "한국어",
  de: "Deutsch",
  fr: "Français",
  es: "Español",
  ru: "Русский",
};

// The language is a per-machine choice and never syncs; without one the system language decides.
export const LOCALE_STORAGE_KEY = "nexterm.locale";

export function isLocale(value: unknown): value is Locale {
  return typeof value === "string" && (LOCALES as readonly string[]).includes(value);
}

// matchLocale maps a BCP 47 tag to a supported language: traditional-script and Taiwan, Hong Kong or Macau Chinese
// to zh-TW, other Chinese to zh-CN, and the rest by primary language.
export function matchLocale(tag: string): Locale | null {
  const lower = tag.trim().toLowerCase();
  if (!lower) return null;
  const [primary, ...rest] = lower.split(/[-_]/);
  if (primary === "zh") {
    return rest.some((part) => part === "hant" || part === "tw" || part === "hk" || part === "mo") ? "zh-TW" : "zh-CN";
  }
  return LOCALES.find((locale) => locale === primary) ?? null;
}

export function storedLocale(): Locale | null {
  try {
    const value = globalThis.localStorage?.getItem(LOCALE_STORAGE_KEY);
    return isLocale(value) ? value : null;
  } catch {
    return null;
  }
}

// storeLocale saves the choice; null returns to following the system language.
export function storeLocale(locale: Locale | null): void {
  try {
    if (locale) globalThis.localStorage?.setItem(LOCALE_STORAGE_KEY, locale);
    else globalThis.localStorage?.removeItem(LOCALE_STORAGE_KEY);
  } catch {
    // Storage can be blocked; the choice then lasts for this page only.
  }
}

export function systemLocale(languages: readonly string[] | undefined = globalThis.navigator?.languages): Locale {
  for (const tag of languages ?? []) {
    const match = matchLocale(tag);
    if (match) return match;
  }
  return languages && languages.length > 0 ? "en" : SOURCE_LOCALE;
}

// resolveLocale is the language the interface starts in. Tests always run in the source language, so assertions on
// interface text do not depend on the machine running them.
export function resolveLocale(): Locale {
  const env = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env;
  if (env?.VITEST) return SOURCE_LOCALE;
  return storedLocale() ?? systemLocale();
}
