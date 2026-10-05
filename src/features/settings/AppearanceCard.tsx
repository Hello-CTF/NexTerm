import { useUi } from "../../app/store";
import type { ThemeMode } from "../../app/theme";
import {
  resetAppearancePrefs,
  setTerminalFontSize,
  setTerminalTheme,
  setUiFontPreset,
  setUiFontScale,
  TERMINAL_FONT_SIZE_MAX,
  TERMINAL_FONT_SIZE_MIN,
  UI_FONT_PRESETS,
  UI_FONT_SCALE_STEPS,
  useAppearancePrefs,
  useResolvedTerminalTheme,
  type TerminalThemePref,
} from "../../app/preferences";
import { IconMinus, IconMonitor, IconPlus, IconRefresh } from "../../ui/icons";

const THEME_OPTIONS: { value: ThemeMode; label: string }[] = [
  { value: "system", label: "跟随系统" },
  { value: "light", label: "浅色" },
  { value: "dark", label: "深色" },
];

const TERM_THEME_OPTIONS: { value: TerminalThemePref; label: string }[] = [
  { value: "dark", label: "深色" },
  { value: "light", label: "浅色" },
  { value: "interface", label: "跟随界面" },
];

export function AppearanceCard() {
  const themeMode = useUi((s) => s.themeMode);
  const resolvedTheme = useUi((s) => s.resolvedTheme);
  const setThemeMode = useUi((s) => s.setThemeMode);
  const prefs = useAppearancePrefs();

  const current =
    themeMode === "system"
      ? `跟随系统 · 当前${resolvedTheme === "dark" ? "深色" : "浅色"}`
      : themeMode === "light"
        ? "浅色"
        : "深色";

  const resolvedTermTheme = useResolvedTerminalTheme();
  const appearanceIsDefault =
    prefs.uiFontPreset === 13 &&
    prefs.uiFontScale === 1 &&
    prefs.terminalFontSize === 13 &&
    prefs.terminalTheme === "dark";

  const resetAll = () => {
    setThemeMode("system");
    resetAppearancePrefs();
  };

  return (
    <section className="nx-card">
      <div className="mb-1 flex items-center gap-2">
        <IconMonitor size={15} className="text-neutral-400" />
        <span className="nx-card-title">外观</span>
        <span className="nx-badge">{current}</span>
      </div>
      <p className="nx-hint mb-3.5">
        界面配色随主题切换；终端与代码编辑器有独立的主题与字号，默认深色，不受界面主题影响。
      </p>
      <div className="nx-segment" role="group" aria-label="界面主题">
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

      <div className="mt-4">
        <div className="nx-setting-label mb-1.5">界面字号（像素）</div>
        <div className="nx-segment" role="group" aria-label="界面字号">
          {UI_FONT_PRESETS.map((preset) => (
            <button
              key={preset}
              type="button"
              className={`nx-segment-item ${prefs.uiFontPreset === preset ? "is-active" : ""}`}
              aria-pressed={prefs.uiFontPreset === preset}
              onClick={() => setUiFontPreset(preset)}
            >
              {preset}
            </button>
          ))}
        </div>
      </div>

      <div className="mt-3.5">
        <div className="nx-setting-label mb-1.5">界面文字倍率（叠加在系统缩放之上）</div>
        <div className="nx-segment" role="group" aria-label="界面文字倍率">
          {UI_FONT_SCALE_STEPS.map((step) => (
            <button
              key={step}
              type="button"
              className={`nx-segment-item ${prefs.uiFontScale === step ? "is-active" : ""}`}
              aria-pressed={prefs.uiFontScale === step}
              onClick={() => setUiFontScale(step)}
            >
              {Math.round(step * 100)}%
            </button>
          ))}
        </div>
      </div>

      <div className="mt-3.5">
        <div className="mb-1.5 flex items-center gap-2">
          <span className="nx-setting-label">终端与编辑器主题</span>
          {prefs.terminalTheme === "interface" && (
            <span className="nx-badge">当前{resolvedTermTheme === "dark" ? "深色" : "浅色"}</span>
          )}
        </div>
        <div className="nx-segment" role="group" aria-label="终端与编辑器主题">
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
        <div className="nx-setting-label mb-1.5">终端字号（像素）</div>
        <div className="flex items-center gap-2">
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm"
            aria-label="减小终端字号"
            disabled={prefs.terminalFontSize <= TERMINAL_FONT_SIZE_MIN}
            onClick={() => setTerminalFontSize(prefs.terminalFontSize - 1)}
          >
            <IconMinus size={11} />
          </button>
          <span className="nx-badge min-w-14 text-center">{prefs.terminalFontSize}px</span>
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm"
            aria-label="增大终端字号"
            disabled={prefs.terminalFontSize >= TERMINAL_FONT_SIZE_MAX}
            onClick={() => setTerminalFontSize(prefs.terminalFontSize + 1)}
          >
            <IconPlus size={11} />
          </button>
          <span className="nx-hint">12-18px，立即生效并重算终端行列。</span>
        </div>
      </div>

      <div className="mt-3.5 flex items-center gap-2">
        <button
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-sm"
          title="恢复默认主题与外观（跟随系统、深色终端、13px、100% 倍率）；不影响布局、凭据与模型档案"
          aria-label="恢复默认主题与外观"
          disabled={themeMode === "system" && appearanceIsDefault}
          onClick={resetAll}
        >
          <IconRefresh size={11} />
          恢复默认
        </button>
        <span className="nx-hint min-w-0 text-[10.5px]">只重置主题与外观偏好，布局、凭据与模型档案不受影响。</span>
      </div>
    </section>
  );
}
