/** @vitest-environment jsdom */

import { afterEach, describe, expect, it } from "vitest";
import { mount, type MountedView } from "./features/reactTestUtils";

function DiscoveryProbe({ label }: { label: string }) {
  return <span>{label}</span>;
}

describe("TSX test discovery regression (M63)", () => {
  let mounted: MountedView | undefined;

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("compiles and renders JSX under the standard quality gate", () => {
    mounted = mount(<DiscoveryProbe label="tsx-discovery-ok" />);
    expect(mounted.container.textContent).toBe("tsx-discovery-ok");
  });
});
