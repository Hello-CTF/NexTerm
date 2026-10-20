// Explicit contract between the agent UI components and the backend roster
// service. The P5 components receive an implementation of this interface via
// props, so component tests can pass a fully mocked object and the production
// wiring (generated IPC from the shared owner) can be attached later without
// touching the components.

import type {
  AgentApprovalDecision,
  AgentRosterEntry,
} from "./agentTypes";

export interface AgentRosterApi {
  list(): Promise<AgentRosterEntry[]>;
  open(handle: string): Promise<{ tabId: string }>;
  attach(handle: string): Promise<{ tabId: string }>;
  rename(handle: string, nextHandle: string): Promise<AgentRosterEntry>;
  release(handle: string): Promise<void>;
  resolveApproval(requestId: string, decision: AgentApprovalDecision): Promise<void>;
}
