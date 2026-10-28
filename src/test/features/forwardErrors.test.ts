import { describe, expect, it } from "vitest";
import { describeForwardError } from "../../features/forward/forwardErrors";
import { regexErrorNotice } from "../../features/logs/mergedLog";

describe("describeForwardError", () => {
  it("explains an occupied port and keeps the original text", () => {
    const text = describeForwardError(new Error("listen tcp 127.0.0.1:8080: bind: address already in use"), 8080);
    expect(text).toContain("端口 8080 已被其他程序占用，请更换端口");
    expect(text).toContain("address already in use");
  });
  it("explains a permission problem", () => {
    expect(describeForwardError(new Error("bind: permission denied"), 80)).toContain("无权使用端口 80");
  });
  it("passes other errors through", () => {
    expect(describeForwardError(new Error("boom"), 1)).toBe("boom");
  });
});

describe("regexErrorNotice", () => {
  it("leads with plain words and ends with the detail", () => {
    const text = regexErrorNotice("Unterminated group");
    expect(text.startsWith("正则表达式写法有误")).toBe(true);
    expect(text).toContain("Unterminated group");
  });
});
