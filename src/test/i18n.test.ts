import { afterEach, describe, expect, it } from "vitest";
import { applyLocale, formatNumber, getLocale, t, translateMessage } from "../i18n";
import { matchLocale, resolveLocale, systemLocale } from "../i18n/locales";

afterEach(() => applyLocale("zh-CN", {}, {}));

describe("locale detection", () => {
  it("maps language tags to the supported languages", () => {
    expect(matchLocale("zh-CN")).toBe("zh-CN");
    expect(matchLocale("zh-Hans-SG")).toBe("zh-CN");
    expect(matchLocale("zh-TW")).toBe("zh-TW");
    expect(matchLocale("zh-HK")).toBe("zh-TW");
    expect(matchLocale("zh-Hant")).toBe("zh-TW");
    expect(matchLocale("en-GB")).toBe("en");
    expect(matchLocale("ja_JP")).toBe("ja");
    expect(matchLocale("pt-BR")).toBeNull();
    expect(matchLocale("")).toBeNull();
  });

  it("takes the first supported system language, English for an unsupported one", () => {
    expect(systemLocale(["pt-BR", "de-AT", "en"])).toBe("de");
    expect(systemLocale(["pt-BR"])).toBe("en");
    expect(systemLocale([])).toBe("zh-CN");
  });

  it("runs tests in the source language", () => {
    expect(resolveLocale()).toBe("zh-CN");
  });
});

describe("t", () => {
  it("shows the source text and fills placeholders without a catalog", () => {
    expect(t("保存")).toBe("保存");
    expect(t("共 {count} 台主机，{name} 在线", { count: 3, name: "web" })).toBe("共 3 台主机，web 在线");
    expect(t("缺少 {value}")).toBe("缺少 {value}");
  });

  it("uses the catalog and the language's plural forms", () => {
    applyLocale("en", { 保存: "Save", "{count} 台主机": { one: "{count} host", other: "{count} hosts" } }, {});
    expect(getLocale()).toBe("en");
    expect(t("保存")).toBe("Save");
    expect(t("{count} 台主机", { count: 1 })).toBe("1 host");
    expect(t("{count} 台主机", { count: 4 })).toBe("4 hosts");
    expect(t("未翻译")).toBe("未翻译");
  });

  it("chooses plural forms by the first numeric parameter when there is no count", () => {
    applyLocale("en", { "已恢复 {successCount} 个工作区": { one: "Restored {successCount} workspace", other: "Restored {successCount} workspaces" } }, {});
    expect(t("已恢复 {successCount} 个工作区", { successCount: 1 })).toBe("Restored 1 workspace");
    expect(t("已恢复 {successCount} 个工作区", { successCount: 3 })).toBe("Restored 3 workspaces");
  });

  it("formats numbers for the interface language", () => {
    applyLocale("de", {}, {});
    expect(formatNumber(1234.5)).toBe("1.234,5");
  });
});

describe("translateMessage", () => {
  const server = {
    "连接已断开": "Disconnected",
    "连接 %s 失败: %v": "Could not connect to %s: %v",
    "第 %d 条命令未执行": "Command %d did not run",
    "权限不足": "Permission denied",
  };

  it("leaves text alone in the source language", () => {
    expect(translateMessage("连接已断开")).toBe("连接已断开");
  });

  it("translates exact messages and Go format templates, nested errors included", () => {
    applyLocale("en", {}, server);
    expect(translateMessage("连接已断开")).toBe("Disconnected");
    expect(translateMessage("第 12 条命令未执行")).toBe("Command 12 did not run");
    expect(translateMessage("连接 web-01 失败: 权限不足")).toBe("Could not connect to web-01: Permission denied");
    expect(translateMessage("连接 web-01 失败: dial tcp: refused")).toBe("Could not connect to web-01: dial tcp: refused");
    expect(translateMessage("未知的消息")).toBe("未知的消息");
    expect(translateMessage("plain english")).toBe("plain english");
  });

  it("matches interface text that arrives as a message", () => {
    applyLocale("en", { 操作已取消: "Cancelled" }, {});
    expect(translateMessage("操作已取消")).toBe("Cancelled");
  });
});
