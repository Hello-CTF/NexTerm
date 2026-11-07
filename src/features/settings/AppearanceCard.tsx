import { useEffect, useId, useState } from "react";
import type { SettingsPanelId } from "./settingsIndex";
import { useUi } from "../../app/store";
import type { ThemeMode } from "../../app/theme";
import {
  resetAppearancePrefs,
  setTerminalContrast,
  setTerminalFontFamily,
  setTerminalFontSize,
  setTerminalTheme,
  setUiFontSize,
  TERMINAL_FONT_FAMILIES,
  TERMINAL_FONT_SIZE_MAX,
  TERMINAL_FONT_SIZE_MIN,
  UI_FONT_PRESETS,
  UI_FONT_SIZE_MAX,
  UI_FONT_SIZE_MIN,
  uiFontSize,
  useAppearancePrefs,
  useResolvedTerminalTheme,
  type TerminalContrast,
  type TerminalFontFamily,
  type TerminalThemePref,
} from "../../app/preferences";
import { IconChevronDown, IconChevronRight, IconMinus, IconMonitor, IconPlus, IconRefresh } from "../../ui/icons";
import { Select } from "../../ui/Select";
import { SyncScopeBadge } from "./SyncScopeBadge";
import { LOCALE_NAMES, LOCALES, setLocale, storedLocale, t, type Locale } from "../../i18n";

const FOLLOW_SYSTEM = "system";

const THEME_OPTIONS: { value: ThemeMode; label: string }[] = [
  { value: "system", label: t("跟随系统") },
  { value: "light", label: t("浅色") },
  { value: "dark", label: t("深色") },
];

const TERM_THEME_OPTIONS: { value: TerminalThemePref; label: string }[] = [
  { value: "dark", label: t("深色") },
  { value: "light", label: t("浅色") },
  { value: "interface", label: t("跟随界面") },
];

const TERM_CONTRAST_OPTIONS: { value: TerminalContrast; label: string }[] = [
  { value: "standard", label: t("标准") },
  { value: "high", label: t("高") },
  { value: "maximum", label: t("极高") },
];

const TERM_FONT_LABELS: Record<TerminalFontFamily, string> = {
  cascadia: t("Cascadia（默认）"),
  system: t("系统等宽"),
  "jetbrains-mono": "JetBrains Mono",
  "fira-code": "Fira Code",
  "source-code-pro": "Source Code Pro",
  menlo: "Menlo",
  consolas: "Consolas",
};

function formatFontSize(size: number): string {
  return String(Math.round(size * 100) / 100);
}

export function AppearanceCard({ reveal }: { reveal?: { panel: SettingsPanelId; n: number } | null }) {
  const themeMode = useUi((s) => s.themeMode);
  const resolvedTheme = useUi((s) => s.resolvedTheme);
  const setThemeMode = useUi((s) => s.setThemeMode);
  const prefs = useAppearancePrefs();

  const current =
    themeMode === "system"
      ? t("跟随系统 · 当前{value}", { value: resolvedTheme === "dark" ? t("深色") : t("浅色") })
      : themeMode === "light"
        ? t("浅色")
        : t("深色");

  const resolvedTermTheme = useResolvedTerminalTheme();
  const appearanceIsDefault =
    prefs.uiFontPreset === 13 &&
    prefs.uiFontScale === 1 &&
    prefs.terminalFontSize === 13 &&
    prefs.terminalFontFamily === "cascadia" &&
    prefs.terminalTheme === "dark" &&
    prefs.terminalContrast === "standard";
  const advancedPanelId = useId();
  const [advancedOpen, setAdvancedOpen] = useState(false);
  useEffect(() => {
    if (reveal?.panel === "appearance-advanced") setAdvancedOpen(true);
  }, [reveal]);

  // A size saved by an older preset/scale pair that matches no preset shows as 自定义 with its effective value, and is
  // only rewritten when the user enters a different size.
  const effectiveFontSize = formatFontSize(uiFontSize(prefs));
  const [customFontOpen, setCustomFontOpen] = useState(false);
  const [customFontDraft, setCustomFontDraft] = useState("");
  const showCustomFont = customFontOpen || !UI_FONT_PRESETS.some((preset) => formatFontSize(preset) === effectiveFontSize);
  useEffect(() => {
    if (showCustomFont) setCustomFontDraft(effectiveFontSize);
  }, [showCustomFont, effectiveFontSize]);
  const commitCustomFont = () => {
    const num = Number(customFontDraft);
    if (customFontDraft.trim() === "" || !Number.isFinite(num) || customFontDraft.trim() === effectiveFontSize) return;
    setUiFontSize(num);
  };

  const resetAll = () => {
    setThemeMode("system");
    resetAppearancePrefs();
  };

  return (
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <IconMonitor size={15} className="text-neutral-400" />
        <span className="nx-card-title">{t("外观")}</span>
        <span className="nx-badge">{current}</span>
        <SyncScopeBadge scope="sync" />
      </div>
      <p className="nx-hint mb-3.5">{t("终端与代码编辑器的主题和字号独立于界面设置。")}</p>
      <div className="mb-3.5">
        <div className="nx-setting-label mb-1.5 flex items-center gap-2" data-setting="ui-language">
          {t("界面语言")}
          <SyncScopeBadge scope="local" />
        </div>
        <Select
          className="w-full"
          ariaLabel={t("界面语言")}
          value={storedLocale() ?? FOLLOW_SYSTEM}
          options={[
            { value: FOLLOW_SYSTEM, label: t("跟随系统") },
            ...LOCALES.map((locale) => ({ value: locale, label: LOCALE_NAMES[locale] })),
          ]}
          onChange={(value) => setLocale(value === FOLLOW_SYSTEM ? null : (value as Locale))}
        />
        <div className="nx-hint mt-1.5">{t("切换后界面会重新加载；终端会话不受影响。")}</div>
      </div>
      <div className="nx-setting-label mb-1.5">{t("界面主题")}</div>
      <div className="nx-segment" role="group" aria-label={t("界面主题")} data-setting="ui-theme">
        {THEME_OPTIONS.map((option) => (
          <button
            key={option.value}
            type="button"
            className={`nx-segment-item ${themeMode === option.value ? "is-active" : ""}`}
            aria-pressed={themeMode === option.value}
            onClick={() => setThemeMode(option.value)}
          >
            {option.label}
          </button>
        ))}
      </div>

      <div className="mt-3.5">
        <div className="nx-setting-label mb-1.5" data-setting="ui-font-size">{t("界面字号（像素）")}</div>
        <div className="nx-segment flex-wrap" role="group" aria-label={t("界面字号")}>
          {UI_FONT_PRESETS.map((preset) => {
            const active = !showCustomFont && formatFontSize(preset) === effectiveFontSize;
            return (
              <button
                key={preset}
                type="button"
                className={`nx-segment-item ${active ? "is-active" : ""}`}
                aria-pressed={active}
                onClick={() => {
                  setCustomFontOpen(false);
                  setUiFontSize(preset);
                }}
              >
                {preset}
              </button>
            );
          })}
          <button
            type="button"
            className={`nx-segment-item ${showCustomFont ? "is-active" : ""}`}
            aria-pressed={showCustomFont}
            onClick={() => setCustomFontOpen(true)}
          >
            {t("自定义")}
          </button>
        </div>
        {showCustomFont && (
          <input
            type="number"
            className="nx-input nx-input-sm mt-1.5 w-[96px] font-mono"
            aria-label={t("自定义界面字号")}
            min={UI_FONT_SIZE_MIN}
            max={UI_FONT_SIZE_MAX}
            step={0.5}
            value={customFontDraft}
            onChange={(e) => setCustomFontDraft(e.target.value)}
            onBlur={commitCustomFont}
            onKeyDown={(e) => {
              if (e.key === "Enter") e.currentTarget.blur();
            }}
          />
        )}
      </div>

      <div className="mt-3.5">
        <div className="mb-1.5 flex items-center gap-2">
          <span className="nx-setting-label" data-setting="terminal-theme">{t("终端与编辑器主题")}</span>
          {prefs.terminalTheme === "interface" && (
            <span className="nx-badge">{t("当前")}{resolvedTermTheme === "dark" ? t("深色") : t("浅色")}</span>
          )}
        </div>
        <div className="nx-segment" role="group" aria-label={t("终端与编辑器主题")}>
          {TERM_THEME_OPTIONS.map((option) => (
            <button
              key={option.value}
              type="button"
              className={`nx-segment-item ${prefs.terminalTheme === option.value ? "is-active" : ""}`}
              aria-pressed={prefs.terminalTheme === option.value}
              onClick={() => setTerminalTheme(option.value)}
            >
              {option.label}
            </button>
          ))}
        </div>
      </div>

      <div className="mt-3.5">
        <div className="nx-setting-label mb-1.5" data-setting="terminal-font-size">{t("终端字号")}</div>
        <div className="flex items-center gap-2">
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm"
            aria-label={t("减小终端字号")}
            disabled={prefs.terminalFontSize <= TERMINAL_FONT_SIZE_MIN}
            onClick={() => setTerminalFontSize(prefs.terminalFontSize - 1)}
          >
            <IconMinus size={11} />
          </button>
          <span className="nx-badge min-w-14 text-center">{prefs.terminalFontSize}px</span>
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm"
            aria-label={t("增大终端字号")}
            disabled={prefs.terminalFontSize >= TERMINAL_FONT_SIZE_MAX}
            onClick={() => setTerminalFontSize(prefs.terminalFontSize + 1)}
          >
            <IconPlus size={11} />
          </button>
        </div>
      </div>

      <div className="mt-3.5">
        <div className="nx-setting-label mb-1.5" data-setting="terminal-font-family">{t("终端字体")}</div>
        <Select
          className="w-full"
          ariaLabel={t("终端字体")}
          value={prefs.terminalFontFamily}
          options={TERMINAL_FONT_FAMILIES.map((family) => ({ value: family, label: TERM_FONT_LABELS[family] }))}
          onChange={(value) => setTerminalFontFamily(value as TerminalFontFamily)}
        />
        <div className="nx-hint mt-1.5">{t("只用本机已安装的字体，未安装时用系统等宽字体。")}</div>
      </div>

      <div className="mt-4">
        <button
          type="button"
          className="flex items-center gap-1.5 text-[12px] text-neutral-400 transition-colors hover:text-neutral-100"
          aria-expanded={advancedOpen}
          aria-controls={advancedPanelId}
          onClick={() => setAdvancedOpen((v) => !v)}
        >
          {advancedOpen ? (
            <IconChevronDown size={12} className="shrink-0" />
          ) : (
            <IconChevronRight size={12} className="shrink-0" />
          )}
          {t("高级设置")}
        </button>
        {advancedOpen && (
          <div id={advancedPanelId}>
            <div className="mt-3">
              <div className="nx-setting-label mb-1.5" data-setting="terminal-contrast">{t("终端对比度")}</div>
              <div className="nx-segment" role="group" aria-label={t("终端对比度")}>
                {TERM_CONTRAST_OPTIONS.map((option) => (
                  <button
                    key={option.value}
                    type="button"
                    className={`nx-segment-item ${prefs.terminalContrast === option.value ? "is-active" : ""}`}
                    aria-pressed={prefs.terminalContrast === option.value}
                    onClick={() => setTerminalContrast(option.value)}
                  >
                    {option.label}
                  </button>
                ))}
              </div>
            </div>

            <div className="mt-3.5 flex items-center gap-2">
              <button
                type="button"
                className="nx-btn nx-btn-ghost nx-btn-sm"
                title={t("恢复默认主题与外观（跟随系统、深色终端、标准对比度、Cascadia、13px）")}
                aria-label={t("恢复默认主题与外观")}
                disabled={themeMode === "system" && appearanceIsDefault}
                onClick={resetAll}
              >
                <IconRefresh size={11} />
                {t("恢复默认")}
              </button>
              <span className="nx-hint min-w-0 text-[10.5px]">{t("只重置主题与外观。")}</span>
            </div>
          </div>
        )}
      </div>
    </section>
  );
}
