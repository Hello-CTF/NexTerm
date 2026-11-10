// App accent presets. Each preset defines the accent and the blue scale separately for the dark and the light
// interface, so text on accent tints and white text on solid accent buttons keep their contrast in both themes.
// Red, rose and amber are left out: they mark production hosts, errors and warnings.

export type AccentPreset = "indigo" | "blue" | "teal" | "green" | "violet";

export const ACCENT_SCALE_STEPS = ["accent", "50", "300", "400", "500", "600", "700", "900"] as const;
export type AccentScaleStep = (typeof ACCENT_SCALE_STEPS)[number];
export type AccentScale = Record<AccentScaleStep, string>;

export interface AccentPresetDef {
  id: AccentPreset;
  dark: AccentScale;
  light: AccentScale;
}

export const DEFAULT_ACCENT: AccentPreset = "indigo";

// indigo is the shipped palette in src/styles/tokens.css; it applies no overrides.
export const ACCENT_PRESETS: readonly AccentPresetDef[] = [
  {
    id: "indigo",
    dark: { accent: "#516cd6", "50": "#f2f3f8", "300": "#acb9ec", "400": "#8b9de4", "500": "#516cd6", "600": "#3c59cc", "700": "#3d509e", "900": "#293461" },
    light: { accent: "#516cd6", "50": "#eef2fc", "300": "#3b5bd0", "400": "#516cd6", "500": "#3c59cc", "600": "#3450c0", "700": "#324a9e", "900": "#23306b" },
  },
  {
    id: "blue",
    dark: { accent: "#3b82f6", "50": "#eff6ff", "300": "#93c5fd", "400": "#60a5fa", "500": "#3b82f6", "600": "#2563eb", "700": "#1d4ed8", "900": "#1e3a8a" },
    light: { accent: "#2563eb", "50": "#eff6ff", "300": "#1d4ed8", "400": "#2563eb", "500": "#1d4ed8", "600": "#1d4ed8", "700": "#1e40af", "900": "#1e3a8a" },
  },
  {
    id: "teal",
    dark: { accent: "#14b8a6", "50": "#f0fdfa", "300": "#5eead4", "400": "#2dd4bf", "500": "#14b8a6", "600": "#0f766e", "700": "#115e59", "900": "#134e4a" },
    light: { accent: "#0d9488", "50": "#f0fdfa", "300": "#115e59", "400": "#0d9488", "500": "#0f766e", "600": "#0f766e", "700": "#134e4a", "900": "#042f2e" },
  },
  {
    id: "green",
    dark: { accent: "#10b981", "50": "#ecfdf5", "300": "#6ee7b7", "400": "#34d399", "500": "#10b981", "600": "#047857", "700": "#065f46", "900": "#064e3b" },
    light: { accent: "#059669", "50": "#ecfdf5", "300": "#065f46", "400": "#059669", "500": "#047857", "600": "#047857", "700": "#064e3b", "900": "#022c22" },
  },
  {
    id: "violet",
    dark: { accent: "#8b5cf6", "50": "#f5f3ff", "300": "#c4b5fd", "400": "#a78bfa", "500": "#8b5cf6", "600": "#7c3aed", "700": "#6d28d9", "900": "#4c1d95" },
    light: { accent: "#7c3aed", "50": "#f5f3ff", "300": "#6d28d9", "400": "#7c3aed", "500": "#6d28d9", "600": "#6d28d9", "700": "#5b21b6", "900": "#4c1d95" },
  },
];

export const ACCENT_PRESET_IDS: readonly AccentPreset[] = ACCENT_PRESETS.map((preset) => preset.id);

export function parseAccentPreset(value: unknown): AccentPreset {
  return typeof value === "string" && (ACCENT_PRESET_IDS as readonly string[]).includes(value) ? (value as AccentPreset) : DEFAULT_ACCENT;
}

// accentVariables returns the custom properties the accent rules in tokens.css read; the default preset returns
// none, so the shipped tokens apply unchanged.
export function accentVariables(id: AccentPreset): Record<string, string> {
  const preset = ACCENT_PRESETS.find((item) => item.id === id);
  if (!preset || id === DEFAULT_ACCENT) return {};
  const vars: Record<string, string> = {};
  for (const theme of ["dark", "light"] as const) {
    for (const step of ACCENT_SCALE_STEPS) vars[`--nx-accent-${theme}-${step}`] = preset[theme][step];
  }
  return vars;
}

export const ACCENT_VARIABLE_NAMES: readonly string[] = (["dark", "light"] as const).flatMap((theme) =>
  ACCENT_SCALE_STEPS.map((step) => `--nx-accent-${theme}-${step}`),
);
