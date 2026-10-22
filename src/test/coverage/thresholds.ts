import type { CoverageOptions } from "vitest/node";

// Starting thresholds for the frontend coverage gate (`pnpm test:coverage`).
// Measured 2026-10-20 from a full `vitest run --coverage` pass: per-file minima
// across the covered src tree, rounded down to the nearest 5 for stability.
// Raise them as coverage grows; do not lower them without a reviewed reason.
export const COVERAGE_THRESHOLDS = {
  lines: 45,
  functions: 30,
  branches: 35,
  statements: 45,
} as const;

// Single source of truth for the gate; vitest.config.ts spreads this into
// `test.coverage` and coverageGate.test.ts asserts on the same object.
export const COVERAGE_GATE: CoverageOptions = {
  provider: "v8",
  include: ["src/**/*.{ts,tsx}"],
  exclude: [
    "src/test/**",
    "src/main.tsx",
    "src/ipc/bindings/**",
    "src/ipc/generated/**",
    // Files with no real tests yet (measured 2026-10-20: 0% on at least one
    // metric). Kept visible here so the gap is explicit; remove each entry as
    // its tests land and the per-file thresholds above take over.
    "src/app/capabilities.ts",
    "src/features/files/DeviceSessionNotice.tsx",
    "src/features/logs/logTailTab.ts",
    "src/features/terminal/gridRuntimeAdapter.ts",
    "src/ipc/bundleFiles.ts",
    "src/ipc/forwardRuleApi.ts",
    "src/ipc/grantApi.ts",
    "src/ipc/inventoryApi.ts",
    "src/ipc/logTailApi.ts",
    "src/ipc/memory.ts",
    "src/ipc/remoteApi.ts",
    "src/ipc/runbookApi.ts",
    "src/ipc/servicesApi.ts",
  ],
  // Generate reports and enforce thresholds even when tests fail, so one run
  // surfaces both kinds of regressions instead of silently skipping the gate.
  reportOnFailure: true,
  thresholds: { ...COVERAGE_THRESHOLDS, perFile: true },
};

export type CoverageMetricKey = keyof typeof COVERAGE_THRESHOLDS;

export type CoverageMetric = { total: number; covered: number };

export type FileCoverageSummary = Record<CoverageMetricKey, CoverageMetric>;

// Mirrors vitest's threshold comparison: istanbul reports a metric with zero
// total as 100% covered, and percentages are floored to two decimals.
export function metricPct(metric: CoverageMetric): number {
  return metric.total > 0 ? Math.floor((1e4 * metric.covered) / metric.total) / 100 : 100;
}

// Lists one human-readable violation per file/metric below its threshold;
// an empty result means the report passes the per-file gate.
export function findCoverageViolations(
  files: Record<string, FileCoverageSummary>,
  thresholds: Record<CoverageMetricKey, number> = COVERAGE_THRESHOLDS,
): string[] {
  const violations: string[] = [];
  for (const [file, summary] of Object.entries(files)) {
    for (const key of Object.keys(thresholds) as CoverageMetricKey[]) {
      const pct = metricPct(summary[key]);
      if (pct < thresholds[key]) {
        violations.push(`${file}: ${key} ${pct}% < ${thresholds[key]}%`);
      }
    }
  }
  return violations;
}
