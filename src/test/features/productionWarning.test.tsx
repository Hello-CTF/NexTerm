/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import { createElement } from "react";
import { ProductionEnvironmentBadge, ProductionWriteRiskNotice } from "../../features/production/ProductionWarning";
import { mount } from "./reactTestUtils";

describe("ProductionEnvironmentBadge", () => {
  it("shows the environment name", () => {
    const view = mount(createElement(ProductionEnvironmentBadge, { environment: "production", production: true }));
    expect(view.container.textContent).toContain("production");
    view.unmount();
  });

  it("marks a backend-classified production environment with a warning label", () => {
    const view = mount(createElement(ProductionEnvironmentBadge, { environment: "production", production: true }));
    expect(view.container.textContent).toContain("生产");
    view.unmount();
  });

  it("marks a backend-classified custom production environment with a warning label", () => {
    const view = mount(createElement(ProductionEnvironmentBadge, { environment: "live", production: true }));
    expect(view.container.textContent).toContain("生产");
    view.unmount();
  });

  it("does not warn when the backend did not classify the environment as production", () => {
    const view = mount(createElement(ProductionEnvironmentBadge, { environment: "staging", production: false }));
    expect(view.container.textContent).toContain("staging");
    expect(view.container.textContent).not.toContain("生产");
    view.unmount();
  });

  it("does not classify locally: a custom name without the backend flag stays unmarked", () => {
    const view = mount(createElement(ProductionEnvironmentBadge, { environment: "live", production: false }));
    expect(view.container.textContent).not.toContain("生产");
    view.unmount();
  });
});

describe("ProductionWriteRiskNotice", () => {
  it("names the environment and the write risk", () => {
    const view = mount(
      createElement(ProductionWriteRiskNotice, { environment: "production", production: true, operation: "广播" }),
    );
    const text = view.container.textContent ?? "";
    expect(text).toContain("production");
    expect(text).toContain("广播");
    expect(text).toContain("写入");
    view.unmount();
  });

  it("states that session memory never authorizes production writes", () => {
    const view = mount(createElement(ProductionWriteRiskNotice, { environment: "production", production: true }));
    expect(view.container.textContent).toContain("不会记住授权");
    view.unmount();
  });

  it("names a backend-classified custom production environment", () => {
    const view = mount(createElement(ProductionWriteRiskNotice, { environment: "live", production: true }));
    const text = view.container.textContent ?? "";
    expect(text).toContain("live");
    expect(text).toContain("生产");
    view.unmount();
  });

  it("renders nothing when the backend did not classify the environment as production", () => {
    const view = mount(createElement(ProductionWriteRiskNotice, { environment: "staging", production: false }));
    expect(view.container.innerHTML).toBe("");
    view.unmount();
  });

  it("renders nothing for a custom name without the backend classification flag", () => {
    const view = mount(createElement(ProductionWriteRiskNotice, { environment: "live", production: false }));
    expect(view.container.innerHTML).toBe("");
    view.unmount();
  });
});
