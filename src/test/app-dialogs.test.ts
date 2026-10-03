/** @vitest-environment jsdom */
//
// R42 round-1 P1 回归：共享 ask 的 kind → level 映射必须显式可钉。
//
// 事故背景：App 对**不传 kind** 的 ask 默认按 warning 渲染（应用里缺省的 ask
// 几乎都是删除/断开类确认）。这导致「保持 info」的调用点（MountPanel 断开挂载、
// AssetTree 软删除、FileEditor 大文件提示）一度实际渲染成警示浮层 —— 调用点
// 必须显式传 { kind: "info" }。这里直接 import 真实映射把两个方向都钉住：
// 显式 info → info（三处非 destructive 依赖它），缺省/warning/error → warning
// （其余 destructive 确认依赖缺省也是警示，不会被误改成普通信息框）。

import { describe, expect, it } from "vitest";
import { dialogLevelForKind } from "../app/App";

describe("dialogLevelForKind（App 共享 ask 映射）", () => {
  it("explicit info maps to info", () => {
    expect(dialogLevelForKind("info")).toBe("info");
  });

  it("missing kind defaults to warning", () => {
    expect(dialogLevelForKind(undefined)).toBe("warning");
  });

  it("warning and error map to warning", () => {
    expect(dialogLevelForKind("warning")).toBe("warning");
    expect(dialogLevelForKind("error")).toBe("warning");
  });
});
