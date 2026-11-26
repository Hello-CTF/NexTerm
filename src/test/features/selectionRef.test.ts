import { beforeEach, describe, expect, it, vi } from "vitest";
import { useUi } from "../../app/store";
import { askAiAboutSelection, revealSelectionSource, SELECTION_REF_RUNES } from "../../features/terminal/selectionRef";

const source = { storeTabId: "st-1", title: "deploy", host: "web-01" };

describe("terminal selection references", () => {
  const pushToast = vi.fn();
  const setActiveTab = vi.fn();
  const setRightOpen = vi.fn();

  beforeEach(() => {
    pushToast.mockReset();
    setActiveTab.mockReset();
    setRightOpen.mockReset();
    useUi.setState({ aiPendingRefs: [], workspaces: [], pushToast, setActiveTab, setRightOpen });
  });

  it("queues the selection as a text reference and opens the AI sidebar", () => {
    expect(askAiAboutSelection("ls: cannot access", source)).toBe(true);
    const [ref] = useUi.getState().aiPendingRefs;
    expect(ref).toMatchObject({ kind: "text", text: "ls: cannot access", selection: true, sourceTabId: "st-1", detail: "deploy · web-01" });
    expect(ref.refId).toBe(ref.id);
    expect(setRightOpen).toHaveBeenCalledWith(true);
  });

  it("refuses an empty selection visibly", () => {
    expect(askAiAboutSelection("  \n", source)).toBe(false);
    expect(useUi.getState().aiPendingRefs).toEqual([]);
    expect(pushToast).toHaveBeenCalledWith("info", "请先在终端中选中要询问的内容");
  });

  it("keeps the tail of a long selection within the reference budget and says so", () => {
    const text = "a".repeat(10) + "b".repeat(SELECTION_REF_RUNES);
    askAiAboutSelection(text, source);
    expect(useUi.getState().aiPendingRefs[0].text).toBe("b".repeat(SELECTION_REF_RUNES));
    expect(pushToast).toHaveBeenCalledWith("info", `选中内容较长，AI 只会看到最后 ${SELECTION_REF_RUNES} 个字符`);
  });

  it("brings an open source tab to the front", () => {
    useUi.setState({ workspaces: [{ id: "w1", panes: [{ id: "p1", tabs: [{ id: "st-1" }] }] }] as never });
    expect(revealSelectionSource("st-1")).toBe(true);
    expect(setActiveTab).toHaveBeenCalledWith("st-1");
    expect(pushToast).not.toHaveBeenCalled();
  });
});
