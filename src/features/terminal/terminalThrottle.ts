import type { TerminalThrottledEvent } from "../../ipc/events";

export const THROTTLE_RECOVERED_MS = 4000;

export type ThrottlePhase = "active" | "recovered";

export interface ThrottleState {
  phase: ThrottlePhase;
  inflightBytes: number;
  at: number;
}

export interface ThrottleView {
  active: boolean;
  recovered: boolean;
  inflightBytes: number;
}

export function throttleStateFrom(event: TerminalThrottledEvent, now: number): ThrottleState {
  return {
    phase: event.recovered === true ? "recovered" : "active",
    inflightBytes: event.inflightBytes,
    at: now,
  };
}

export function throttleView(state: ThrottleState | null, now: number): ThrottleView | null {
  if (!state) return null;
  if (state.phase === "active") {
    return { active: true, recovered: false, inflightBytes: state.inflightBytes };
  }
  if (now - state.at <= THROTTLE_RECOVERED_MS) {
    return { active: false, recovered: true, inflightBytes: 0 };
  }
  return null;
}
