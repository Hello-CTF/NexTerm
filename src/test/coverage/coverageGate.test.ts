import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import {
  COVERAGE_GATE,
  COVERAGE_THRESHOLDS,
  findCoverageViolations,
  metricPct,
  type CoverageMetricKey,
  type FileCoverageSummary,
} from "./thresholds";

const METRIC_KEYS = Object.keys(COVERAGE_THRESHOLDS) as CoverageMetricKey[];

function passingSummary(): FileCoverageSummary {
  const metric = () => ({ total: 100, covered: 100 });
  return { lines: metric(), functions: metric(), branches: metric(), statements: metric() };
}

describe("coverage thresholds", () => {
  it("stay nonzero so the gate cannot pass trivially", () => {
    for (const key of METRIC_KEYS) {
      expect(COVERAGE_THRESHOLDS[key], `${key} threshold`).toBeGreaterThan(0);
    }
  });

  it("floor percentages to two decimals like istanbul", () => {
    expect(metricPct({ total: 3, covered: 1 })).toBe(33.33);
    expect(metricPct({ total: 3, covered: 2 })).toBe(66.66);
  });

  it("treat metrics with zero total as fully covered, matching vitest", () => {
    expect(metricPct({ total: 0, covered: 0 })).toBe(100);
    const files = { "src/empty.ts": passingSummary() };
    files["src/empty.ts"].branches = { total: 0, covered: 0 };
    expect(findCoverageViolations(files)).toEqual([]);
  });

  it("accept an unmutated report", () => {
    expect(findCoverageViolations({ "src/ok.ts": passingSummary() })).toEqual([]);
  });

  it("accept coverage exactly at the threshold", () => {
    const files = { "src/at-limit.ts": passingSummary() };
    for (const key of METRIC_KEYS) files["src/at-limit.ts"][key].covered = COVERAGE_THRESHOLDS[key];
    expect(findCoverageViolations(files)).toEqual([]);
  });

  for (const key of METRIC_KEYS) {
    it(`reject the mutation that drops ${key} below the threshold`, () => {
      const files = { "src/mutated.ts": passingSummary() };
      files["src/mutated.ts"][key].covered = COVERAGE_THRESHOLDS[key] - 1;
      expect(findCoverageViolations(files)).toEqual([
        `src/mutated.ts: ${key} ${COVERAGE_THRESHOLDS[key] - 1}% < ${COVERAGE_THRESHOLDS[key]}%`,
      ]);
    });
  }

  it("only flag the mutated file in a multi-file report", () => {
    const files = { "src/ok.ts": passingSummary(), "src/mutated.ts": passingSummary() };
    files["src/mutated.ts"].functions.covered = COVERAGE_THRESHOLDS.functions - 1;
    const violations = findCoverageViolations(files);
    expect(violations).toHaveLength(1);
    expect(violations[0]).toContain("src/mutated.ts");
  });
});

describe("shipped coverage gate", () => {
  it("enforces the measured thresholds per file with the v8 provider", () => {
    expect(COVERAGE_GATE.provider).toBe("v8");
    expect(COVERAGE_GATE.reportOnFailure).toBe(true);
    expect(COVERAGE_GATE.thresholds).toMatchObject({ ...COVERAGE_THRESHOLDS, perFile: true });
    expect(COVERAGE_GATE.include).toContain("src/**/*.{ts,tsx}");
    expect(COVERAGE_GATE.exclude).toEqual(
      expect.arrayContaining(["src/test/**", "src/ipc/bindings/**", "src/ipc/generated/**"]),
    );
  });

  it("pins @vitest/coverage-v8 to the exact vitest version", () => {
    const pkg = JSON.parse(readFileSync(new URL("../../../package.json", import.meta.url), "utf8")) as {
      devDependencies: Record<string, string>;
    };
    expect(pkg.devDependencies["@vitest/coverage-v8"]).toBe(pkg.devDependencies.vitest);
    expect(pkg.devDependencies.vitest).toMatch(/^\d+\.\d+\.\d+$/);
  });
});
