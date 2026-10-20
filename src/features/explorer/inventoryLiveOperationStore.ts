import type { InventoryTargetExecRequest } from "../../ipc/generated/types";

// LiveOperation is the in-flight lifecycle of one inventory target exec,
// bound to the launching owner and target. The store owns the registry and
// the subscriber notifications; React components only adapt to it.

export type LiveOperationStatus = "running" | "ignored" | "cancel-pending" | "cancel-failed";
export type LiveOperationOutcome = "cancelled" | "result" | "failure";

export interface LiveOperation {
  snapshot: InventoryTargetExecRequest;
  ownerKey: string;
  identity: string;
  status: LiveOperationStatus;
  cancelError?: string;
  // cancelSeq is the operation-level cancel sequence: a late settle of an
  // earlier cancel never overwrites the state of a newer retry, no matter
  // which view instance issued either cancel.
  cancelSeq?: number;
  settled?: boolean;
  outcome?: LiveOperationOutcome;
  result?: { output: string; exitCode: number };
  failure?: { message: string; exitCode?: number; output?: string };
}

export type LiveOperationEventType = "settle" | "cancel";

export interface LiveOperationEvent {
  type: LiveOperationEventType;
  operation: LiveOperation;
}

export type Unsubscribe = () => void;

const LIVE_REGISTRY_LIMIT = 32;

// LiveOperationStore keeps live operations in a per-owner
// {ownerKey, operationId} namespace and notifies every current subscriber
// of settle and cancel-state changes. Settled outcomes are retained in the
// registry until a subscriber consumes them, so a notification is never
// lost between an unmount and a remount.
class LiveOperationStore {
  private registry = new Map<string, Map<string, LiveOperation>>();
  private subscribers = new Map<string, Map<number, (event: LiveOperationEvent) => void>>();
  private nextSubscriberId = 0;

  private key(ownerKey: string, operationId: string): string {
    return `${ownerKey}/${operationId}`;
  }

  // store registers an operation in its owner's namespace. At capacity
  // only settled entries are recycled; a new exec beyond capacity is
  // rejected with a visible error before any call is made.
  store(op: LiveOperation): Error | null {
    let ownerOps = this.registry.get(op.ownerKey);
    if (!ownerOps) {
      ownerOps = new Map();
      this.registry.set(op.ownerKey, ownerOps);
    }
    if (ownerOps.size >= LIVE_REGISTRY_LIMIT && !ownerOps.has(op.snapshot.operationId)) {
      for (const [id, existing] of ownerOps) {
        if (existing.settled) {
          ownerOps.delete(id);
          break;
        }
      }
      if (ownerOps.size >= LIVE_REGISTRY_LIMIT) {
        return new Error("该所有者的在途操作已达上限，请等待现有操作结束");
      }
    }
    ownerOps.set(op.snapshot.operationId, op);
    return null;
  }

  delete(ownerKey: string, operationId: string): void {
    const ownerOps = this.registry.get(ownerKey);
    if (ownerOps) {
      ownerOps.delete(operationId);
      if (ownerOps.size === 0) this.registry.delete(ownerKey);
    }
  }

  get(ownerKey: string, operationId: string): LiveOperation | null {
    return this.registry.get(ownerKey)?.get(operationId) ?? null;
  }

  // getOwnerLive exposes the owner's operation namespace for restore and
  // replay scans; callers only read it.
  getOwnerLive(ownerKey: string): Map<string, LiveOperation> | null {
    return this.registry.get(ownerKey) ?? null;
  }

  // subscribe reads the current operation state and atomically registers
  // the listener, so no notification is lost between the read and the
  // subscription. The returned operation may already be settled, in which
  // case the subscriber replays its outcome immediately.
  subscribe(
    ownerKey: string,
    operationId: string,
    listener: (event: LiveOperationEvent) => void,
  ): { operation: LiveOperation | null; unsubscribe: Unsubscribe } {
    const operation = this.get(ownerKey, operationId);
    if (!operation) {
      return { operation: null, unsubscribe: () => {} };
    }
    const key = this.key(ownerKey, operationId);
    let listeners = this.subscribers.get(key);
    if (!listeners) {
      listeners = new Map();
      this.subscribers.set(key, listeners);
    }
    const id = ++this.nextSubscriberId;
    listeners.set(id, listener);
    return {
      operation,
      unsubscribe: () => {
        const current = this.subscribers.get(key);
        if (current) {
          current.delete(id);
          if (current.size === 0) this.subscribers.delete(key);
        }
      },
    };
  }

  // emit delivers an event to every current subscriber of the operation.
  private emit(ownerKey: string, operationId: string, event: LiveOperationEvent): void {
    const listeners = this.subscribers.get(this.key(ownerKey, operationId));
    if (!listeners) return;
    for (const listener of [...listeners.values()]) {
      listener(event);
    }
  }

  // markSettled records the operation's outcome and notifies every
  // subscriber. The settled operation is retained in the registry until a
  // subscriber consumes it, so a notification is never lost while no view
  // is mounted.
  markSettled(op: LiveOperation): void {
    op.settled = true;
    this.emit(op.ownerKey, op.snapshot.operationId, { type: "settle", operation: op });
  }

  // notifyCancel delivers a cancel-state change (pending, timeout, or
  // failure) to every current subscriber, not just the instance that
  // initiated the cancel.
  notifyCancel(op: LiveOperation): void {
    this.emit(op.ownerKey, op.snapshot.operationId, { type: "cancel", operation: op });
  }

  // consumeSettled removes a settled operation once a subscriber has
  // consumed its outcome.
  consumeSettled(ownerKey: string, operationId: string): void {
    this.delete(ownerKey, operationId);
  }

  // reset clears the registry and all subscribers; it exists for tests.
  reset(): void {
    this.registry.clear();
    this.subscribers.clear();
  }
}

export const liveOperationStore = new LiveOperationStore();
