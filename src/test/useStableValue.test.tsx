/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import { act, createElement } from "react";
import { flush, mount } from "./features/reactTestUtils";
import { useStableValue } from "../ui/useStableValue";

describe("useStableValue", () => {
  it("keeps the first object while later ones are structurally equal and switches when content changes", async () => {
    const seen: object[] = [];
    function Probe({ value }: { value: { scope: string; list: number[] } }) {
      seen.push(useStableValue(value));
      return null;
    }
    const first = { scope: "a", list: [1, 2] };
    const view = mount(createElement(Probe, { value: first }));
    await flush();
    act(() => view.root.render(createElement(Probe, { value: { scope: "a", list: [1, 2] } })));
    await flush();
    act(() => view.root.render(createElement(Probe, { value: { scope: "b", list: [1, 2] } })));
    await flush();
    expect(seen[0]).toBe(first);
    expect(seen[1]).toBe(first);
    expect(seen[seen.length - 1]).not.toBe(first);
    expect((seen[seen.length - 1] as { scope: string }).scope).toBe("b");
    view.unmount();
  });
});
