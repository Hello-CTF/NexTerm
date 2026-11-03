import { describe, expect, it } from "vitest";
import {
  analyzeSnippetSteps,
  analyzeSnippetTemplate,
  renderSnippetForTargets,
  renderSnippetTemplate,
  type SnippetAsset,
} from "../../features/explorer/snippetVariables";

const assetA: SnippetAsset = { name: "api-a", host: "10.0.0.1", tags: "production, web" };
const assetB: SnippetAsset = { name: "api-b", host: "10.0.0.2", tags: "staging，worker" };

describe("snippet variable templates", () => {
  it("parses required, default, optional, and multiline fields without treating Go templates as variables", () => {
    const analysis = analyzeSnippetTemplate(
      "echo {{ service }} {{region=cn}} {{remark | optional}} {{script|multiline}} {{.Names}}",
    );
    expect(analysis.hasVariables).toBe(true);
    expect(analysis.variables).toEqual([
      { name: "service", required: true, defaultValue: undefined, multiline: false },
      { name: "region", required: true, defaultValue: "cn", multiline: false },
      { name: "remark", required: false, defaultValue: undefined, multiline: false },
      { name: "script", required: true, defaultValue: undefined, multiline: true },
    ]);
    expect(analyzeSnippetTemplate("docker ps --format '{{.Names}}'").hasVariables).toBe(false);
  });

  it("uses defaults and preserves multiline and shell metacharacters verbatim", () => {
    expect(
      renderSnippetTemplate("run {{service=nginx}} {{script|multiline}}", {
        script: "printf one\nprintf '%s' \"$HOME\"; # intentional",
      }),
    ).toBe("run nginx printf one\nprintf '%s' \"$HOME\"; # intentional");
    expect(() => renderSnippetTemplate("echo {{value}}", { value: "two\nlines" })).toThrow(/单行变量/);
  });

  it("reports missing required values and unknown variables instead of inserting an empty string", () => {
    expect(() => renderSnippetTemplate("echo {{missing}}", {})).toThrow(/缺少必填变量「missing」/);
    expect(() => renderSnippetTemplate("echo {{service}}", { service: "" })).toThrow(/必填变量「service」不能为空/);
    expect(() => renderSnippetTemplate("echo {{asset.environment}}", {}, assetA)).toThrow(/未知变量/);
    expect(() => renderSnippetTemplate("echo {{asset.name}}")).toThrow(/缺少目标资产信息/);
    expect(() => renderSnippetTemplate("echo {{asset.host}}", {}, { ...assetA, host: null })).toThrow(/缺少主机地址/);
  });

  it("trims token whitespace and gives case-insensitive built-ins priority over custom values", () => {
    expect(
      renderSnippetTemplate("echo {{ asset.name }} {{ASSET.HOST}} {{asset.tags}}", {
        "asset.name": "not-the-asset",
      }, assetA),
    ).toBe("echo api-a 10.0.0.1 production, web");
  });

  it("renders each target with its own asset name, host, and tags", () => {
    const rendered = renderSnippetForTargets(
      "echo {{asset.name}} {{asset.host}} {{asset.tags}} {{service=nginx}}",
      {},
      [
        { id: "s1", label: "one", asset: assetA },
        { id: "s2", label: "two", asset: assetB },
      ],
    );
    expect(rendered).toEqual([
      { id: "s1", command: "echo api-a 10.0.0.1 production, web nginx" },
      { id: "s2", command: "echo api-b 10.0.0.2 staging, worker nginx" },
    ]);
  });

  it("returns templates without variables byte-for-byte unchanged", () => {
    const template = "docker ps --format '{{.Names}}'\t\n";
    expect(renderSnippetTemplate(template)).toBe(template);
  });

  it("accepts a colon default and splits at the first colon or equals sign", () => {
    expect(analyzeSnippetTemplate("{{name:web}}").variables).toEqual([
      { name: "name", required: true, defaultValue: "web", multiline: false },
    ]);
    expect(analyzeSnippetTemplate("{{a=x:y}}").variables[0].defaultValue).toBe("x:y");
    expect(analyzeSnippetTemplate("{{a:x=y}}").variables[0].defaultValue).toBe("x=y");
    expect(renderSnippetTemplate("echo {{port:8080}}")).toBe("echo 8080");
    expect(renderSnippetTemplate("echo {{port:8080}}", { port: "9" })).toBe("echo 9");
  });

  it("still rejects defaults on built-in asset variables", () => {
    expect(() => analyzeSnippetTemplate("{{asset.name:x}}")).toThrow(/内建变量/);
    expect(() => analyzeSnippetTemplate("{{asset.host=x}}")).toThrow(/内建变量/);
  });

  it("merges variables across steps and rejects conflicting definitions", () => {
    const merged = analyzeSnippetSteps(["echo {{svc:nginx}} {{asset.name}}", "restart {{svc=nginx}} {{env}}"]);
    expect(merged.variables.map((v) => v.name)).toEqual(["svc", "env"]);
    expect(merged.usesAsset).toBe(true);
    expect(merged.hasVariables).toBe(true);
    expect(() => analyzeSnippetSteps(["a {{svc=one}}", "b {{svc=two}}"])).toThrow(/不一致/);
    expect(() => analyzeSnippetSteps(["a {{svc}}", "b {{svc|optional}}"])).toThrow(/不一致/);
    expect(analyzeSnippetSteps(["ls", "pwd"]).hasVariables).toBe(false);
  });
});
