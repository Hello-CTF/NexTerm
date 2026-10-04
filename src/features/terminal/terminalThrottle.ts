import type { TerminalThrottledEvent } from "../../ipc/events";

export const THROTTLE_ACTIVE_MS = 2000;
export const THROTTLE_RECOVERED_MS = 4000;

export interface ThrottleState {
  lastEventAt: number;
  inflightBytes: number;
}

export interface ThrottleView {
  active: boolean;
  recovered: boolean;
  inflightBytes: number;
}

export function throttleStateFrom(event: TerminalThrottledEvent, now: number): ThrottleState {
  return { lastEventAt: now, inflightBytes: event.inflightBytes };
}

export function throttleView(state: ThrottleState | null, now: number): ThrottleView | null {
  if (!state) return null;
  const since = now - state.lastEventAt;
  if (since <= THROTTLE_ACTIVE_MS) {
    return { active: true, recovered: false, inflightBytes: state.inflightBytes };
  }
  if (since <= THROTTLE_ACTIVE_MS + THROTTLE_RECOVERED_MS) {
    return { active: false, recovered: true, inflightBytes: 0 };
  }
  return null;
}
