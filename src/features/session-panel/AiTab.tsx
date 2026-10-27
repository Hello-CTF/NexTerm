import { useCallback } from "react";
import { useUi } from "../../app/store";
import { IconSparkles } from "../../ui/icons";
import { PanelEmpty } from "./PanelEmpty";
import { setAiPanelSlot } from "./aiSlot";

// AiTab shows the app's AI assistant inside the session panel. The assistant is a single instance: while the right AI
// dock is open it stays there, and the tab offers to move it here by closing the dock.
export function AiTab({ aiDocked }: { aiDocked: boolean }) {
  const slotRef = useCallback((element: HTMLDivElement | null) => setAiPanelSlot(element), []);
  if (aiDocked) {
    return (
      <PanelEmpty
        icon={IconSparkles}
        title="AI 助手在右侧栏中打开"
        hint="AI 助手同一时间只在一处显示。收起右侧 AI 栏后，它会显示在这里。"
        action={
          <button
            type="button"
            className="nx-btn nx-btn-outline nx-btn-sm"
            onClick={() => useUi.getState().setRightOpen(false)}
          >
            在会话侧栏中显示
          </button>
        }
      />
    );
  }
  return <div ref={slotRef} className="flex h-full min-h-0 flex-col" data-testid="session-panel-ai" />;
}
