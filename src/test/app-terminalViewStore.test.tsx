/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it } from "vitest";
import { act, createElement } from "react";
import {
  getTerminalLinesBelow,
  getTerminalTabTitle,
  resetTerminalView,
  setTerminalLinesBelow,
  setTerminalTabTitle,
  useTerminalLinesBelow,
  useTerminalTabTitle,
} from "../app/terminalViewStore";
import { mount, type MountedView } from "./features/reactTestUtils";

describe("terminal view store selectors", () => {
  let renders: string[];
  let mounted: MountedView | undefined;

  function Probe({ tabId }: { tabId: string }) {
    const linesBelow = useTerminalLinesBelow(tabId);
    const title = useTerminalTabTitle(tabId);
    renders.push(`${linesBelow}|${title ?? ""}`);
    return null;
  }

  beforeEach(() => {
    mounted?.unmount();
    act(() => resetTerminalView());
    renders = [];
    mounted = mount(createElement(Probe, { tabId: "t1" }));
  });

  it("renders the initial zero state without lines below or title", () => {
    expect(renders).toEqual(["0|"]);
    expect(getTerminalLinesBelow("t1")).toBe(0);
    expect(getTerminalTabTitle("t1")).toBeUndefined();
  });

  it("re-renders only when the subscribed tab values change", () => {
    act(() => setTerminalLinesBelow("t1", 5));
    expect(renders).toEqual(["0|", "5|"]);

    // Another tab's scrolling must not re-render this subscriber.
    act(() => {
      setTerminalLinesBelow("t2", 9);
      setTerminalTabTitle("t2", "htop");
    });
    expect(renders).toEqual(["0|", "5|"]);

    act(() => setTerminalTabTitle("t1", "vim"));
    expect(renders).toEqual(["0|", "5|", "5|vim"]);
    expect(getTerminalTabTitle("t1")).toBe("vim");
  });

  it("treats unchanged values as no-ops so output bursts do not re-render", () => {
    act(() => setTerminalLinesBelow("t1", 5));
    const before = renders.length;
    act(() => {
      setTerminalLinesBelow("t1", 5);
      setTerminalLinesBelow("t1", 5);
    });
    expect(renders).toHaveLength(before);
  });

  it("clears every tracked value on reset", () => {
    act(() => {
      setTerminalLinesBelow("t1", 5);
      setTerminalTabTitle("t1", "vim");
    });
    act(() => resetTerminalView());
    // React batches both sets inside one act() into a single re-render.
    expect(renders).toEqual(["0|", "5|vim", "0|"]);
    expect(getTerminalLinesBelow("t1")).toBe(0);
    expect(getTerminalTabTitle("t1")).toBeUndefined();
  });

  it("drops only the reset tab when a tab id is given", () => {
    act(() => {
      setTerminalLinesBelow("t1", 5);
      setTerminalLinesBelow("t2", 9);
    });
    act(() => resetTerminalView("t2"));
    expect(getTerminalLinesBelow("t1")).toBe(5);
    expect(getTerminalLinesBelow("t2")).toBe(0);
  });
});
