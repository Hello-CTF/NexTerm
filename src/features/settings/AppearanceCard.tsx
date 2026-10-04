import { useUi } from "../../app/store";
import type { ThemeMode } from "../../app/theme";
import { IconMonitor } from "../../ui/icons";

const OPTIONS: { value: ThemeMode; label: string }[] = [
  { value: "system", label: "跟随系统" },
  { value: "light", label: "浅色" },
  { value: "dark", label: "深色" },
];

export function AppearanceCard() {
  const themeMode = useUi((s) => s.themeMode);
  const resolvedTheme = useUi((s) => s.resolvedTheme);
  const setThemeMode = useUi((s) => s.setThemeMode);

  const current =
    themeMode === "system"
      ? `跟随系统 · 当前${resolvedTheme === "dark" ? "深色" : "浅色"}`
      : themeMode === "light"
        ? "浅色"
        : "深色";

  return (
    <section className="nx-card">
      <div className="mb-1 flex items-center gap-2">
        <IconMonitor size={15} className="text-neutral-400" />
        <span className="nx-card-title">外观</span>
        <span className="nx-badge">{current}</span>
      </div>
      <p className="nx-hint mb-3.5">
        界面配色随主题切换；终端与代码编辑器始终保持深色，不受主题影响。
      </p>
      <div className="nx-segment" role="group" aria-label="界面主题">
        {OPTIONS.map((option) => (
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
    </section>
  );
}
