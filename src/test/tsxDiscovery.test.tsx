/** @vitest-environment jsdom */

import { afterEach, describe, expect, it } from "vitest";
import viteConfigSource from "../../vite.config.ts?raw";
import { mount, type MountedView } from "./features/reactTestUtils";

// M63 回归测试：共享 include 曾只收集 .test.ts，M59–M62 的 .test.tsx 被静默跳过。
// 本文件本身就是活证据 —— 只有标准门禁真实收集 TSX 时它才会运行；
// vite.config.ts 末尾的加载期守卫负责在 include 被收窄时直接报配置错误。
// 注意：vite.config.ts 无法作为普通模块被测试 import（vitest 的 import-analysis
// 拒绝解析配置文件本身），所以契约断言走 `?raw` 文本导入；tsconfig 只含
// vite/client 类型（没有 @types/node），不能用 node:fs。

function DiscoveryProbe({ label }: { label: string }) {
  return <span>{label}</span>;
}

const expectedInclude = ["src/test/**/*.test.ts", "src/test/**/*.test.tsx"];

function sharedIncludePatterns(): string[] {
  const match = /include\s*:\s*\[([^\]]*)\]/.exec(viteConfigSource);
  return match ? [...match[1].matchAll(/"([^"]+)"/g)].map((entry) => entry[1]) : [];
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

  it("共享 include 钉住 .test.ts 与 .test.tsx 双覆盖", () => {
    expect(sharedIncludePatterns()).toEqual(expectedInclude);
  });
});
