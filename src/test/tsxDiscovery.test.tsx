/** @vitest-environment jsdom */

import { afterEach, describe, expect, it } from "vitest";
import { mount, type MountedView } from "./features/reactTestUtils";

// M63 回归测试：共享 include 曾只收集 .test.ts，.test.tsx 会被静默跳过。
// 本文件就是活证据 —— 只有标准门禁真实收集 TSX 时它才会运行；
// include 被收窄的硬失败由 vite.config.ts 末尾的加载期守卫负责。

function DiscoveryProbe({ label }: { label: string }) {
  return <span>{label}</span>;
}

describe("TSX 测试发现回归 (M63)", () => {
  let mounted: MountedView | undefined;

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("标准门禁能编译并渲染 JSX", () => {
    mounted = mount(<DiscoveryProbe label="tsx-discovery-ok" />);
    expect(mounted.container.textContent).toBe("tsx-discovery-ok");
  });
});
