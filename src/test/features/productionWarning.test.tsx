/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import { createElement } from "react";
import {
  ProductionEnvironmentBadge,
  ProductionWriteRiskNotice,
  isProductionEnvironmentName,
} from "../../features/production/ProductionWarning";
import { mount } from "./reactTestUtils";

describe("isProductionEnvironmentName", () => {
  it("always treats built-in names as production", () => {
    expect(isProductionEnvironmentName("production")).toBe(true);
    expect(isProductionEnvironmentName("prod")).toBe(true);
    expect(isProductionEnvironmentName("Production")).toBe(true);
  });

  it("treats configured names as production and others as not", () => {
    expect(isProductionEnvironmentName("live", ["live"])).toBe(true);
    expect(isProductionEnvironmentName("staging", ["live"])).toBe(false);
    expect(isProductionEnvironmentName("live", [])).toBe(false);
  });
});

describe("ProductionEnvironmentBadge", () => {
  it("shows the environment name", () => {
    const view = mount(createElement(ProductionEnvironmentBadge, { environment: "production" }));
    expect(view.container.textContent).toContain("production");
    view.unmount();
  });

  it("marks built-in production with a warning label", () => {
    const view = mount(createElement(ProductionEnvironmentBadge, { environment: "production" }));
    expect(view.container.textContent).toContain("生产");
    view.unmount();
  });

  it("marks configured production with a warning label", () => {
    const view = mount(createElement(ProductionEnvironmentBadge, { environment: "live", configuredNames: ["live"] }));
    expect(view.container.textContent).toContain("生产");
    view.unmount();
  });

  it("does not warn for non-production environments", () => {
    const view = mount(createElement(ProductionEnvironmentBadge, { environment: "staging" }));
    expect(view.container.textContent).toContain("staging");
    expect(view.container.textContent).not.toContain("生产");
    view.unmount();
  });
});

describe("ProductionWriteRiskNotice", () => {
  it("names the environment and the write risk", () => {
    const view = mount(createElement(ProductionWriteRiskNotice, { environment: "production", operation: "广播" }));
    const text = view.container.textContent ?? "";
    expect(text).toContain("production");
    expect(text).toContain("广播");
    expect(text).toContain("写入");
    view.unmount();
  });

  it("states that session memory never authorizes production writes", () => {
    const view = mount(createElement(ProductionWriteRiskNotice, { environment: "production" }));
    expect(view.container.textContent).toContain("不会记住授权");
    view.unmount();
  });

  it("names a configured production environment", () => {
    const view = mount(createElement(ProductionWriteRiskNotice, { environment: "live", configuredNames: ["live"] }));
    const text = view.container.textContent ?? "";
    expect(text).toContain("live");
    expect(text).toContain("生产");
    view.unmount();
  });

  it("renders nothing for non-production environments", () => {
    const view = mount(createElement(ProductionWriteRiskNotice, { environment: "staging" }));
    expect(view.container.innerHTML).toBe("");
    view.unmount();
  });
});
