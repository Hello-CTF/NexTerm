/** @vitest-environment jsdom */

import { afterEach, describe, expect, it } from "vitest";
import { mount, type MountedView } from "./features/reactTestUtils";

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
