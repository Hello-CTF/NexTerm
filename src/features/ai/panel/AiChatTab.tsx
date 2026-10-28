import { useChatRunSession } from "./useChatRunSession";
import { useChatPanelState } from "./useChatPanelState";
import { useChatRunRecovery } from "./useChatRunRecovery";
import { useChatActions } from "./useChatActions";
import { useChatAgentsAndHistory } from "./useChatAgentsAndHistory";
import { AiChatTopBar } from "./AiChatTopBar";
import { AiChatPanels } from "./AiChatPanels";
import { AiChatMessageStream } from "./AiChatMessageStream";
import { AiChatComposer } from "./AiChatComposer";
import type { AiChatBase, AiChatTabProps } from "./aiSidebarShared";
import type { AiChatCtx } from "./aiChatCtx";

export function AiChatTab(props: AiChatTabProps) {
  const { conversationId: boundConversationId, ...rest } = props;
  const base: AiChatBase = { ...rest, boundConversationId };
  const useChatRunSessionOut = useChatRunSession(base);
  const useChatPanelStateOut = useChatPanelState({ ...base, ...useChatRunSessionOut });
  const useChatRunRecoveryOut = useChatRunRecovery({ ...base, ...useChatRunSessionOut, ...useChatPanelStateOut });
  const useChatActionsOut = useChatActions({ ...base, ...useChatRunSessionOut, ...useChatPanelStateOut, ...useChatRunRecoveryOut });
  const useChatAgentsAndHistoryOut = useChatAgentsAndHistory({ ...base, ...useChatRunSessionOut, ...useChatPanelStateOut, ...useChatRunRecoveryOut, ...useChatActionsOut });
  const ctx: AiChatCtx = { ...base, ...useChatRunSessionOut, ...useChatPanelStateOut, ...useChatRunRecoveryOut, ...useChatActionsOut, ...useChatAgentsAndHistoryOut };
  if (!ctx.rightOpen || !ctx.active) return null;
  return (
    <>
      <AiChatTopBar ctx={ctx} />
      <AiChatPanels ctx={ctx} />
      <AiChatMessageStream ctx={ctx} />
      <AiChatComposer ctx={ctx} />
    </>
  );
}
