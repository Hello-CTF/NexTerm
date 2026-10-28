import type { AiChatBase } from "./aiSidebarShared";
import type { UseChatRunSessionOut } from "./useChatRunSession";
import type { UseChatPanelStateOut } from "./useChatPanelState";
import type { UseChatRunRecoveryOut } from "./useChatRunRecovery";
import type { UseChatActionsOut } from "./useChatActions";
import type { UseChatAgentsAndHistoryOut } from "./useChatAgentsAndHistory";
import type { UseBackgroundTasksOut } from "./useBackgroundTasks";

export type AiChatCtx = AiChatBase & UseChatRunSessionOut & UseChatPanelStateOut & UseChatRunRecoveryOut & UseChatActionsOut & UseChatAgentsAndHistoryOut & UseBackgroundTasksOut;
