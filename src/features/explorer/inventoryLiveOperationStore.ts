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

export type LiveOperationEventType = "settle" | "cancel" | "ignore" | "clear";

export interface LiveOperationEvent {
  type: LiveOperationEventType;
  operation: LiveOperation;
}

export type Unsubscribe = () => void;

const LIVE_REGISTRY_LIMIT = 32;

// LiveOperationStore keeps operations in a per-owner {ownerKey,
// operationId} namespace and notifies subscribers of settle, cancel,
// ignore, and clear events. Per owner and target identity at most one
// unsettled operation exists: a second exec is rejected visibly before any
// call. Settled outcomes are retained in the namespace until a user
// explicitly clears them and are replayed idempotently to every current and
// future view of that owner and target. At capacity new operations are
// rejected visibly — live operations and retained outcomes are never
// silently evicted.
class LiveOperationStore {
  private registry = new Map<string, Map<string, LiveOperation>>();
  private opSubscribers = new Map<string, Map<number, (event: LiveOperationEvent) => void>>();
  private outcomeSubscribers = new Map<string, Map<number, (event: LiveOperationEvent) => void>>();
  private nextSubscriberId = 0;

  private opKey(ownerKey: string, operationId: string): string {
    return `${ownerKey}/${operationId}`;
  }

  private identityKey(ownerKey: string, identity: string): string {
    return `${ownerKey}/${identity}`;
  }

  // store registers an operation. An unsettled operation with the same
  // owner and target identity blocks a replacement with a visible error
  // before any call is made; at capacity the same happens. Entries leave
  // the registry only through an explicit removal, never through eviction.
  store(op: LiveOperation): Error | null {
    let ownerOps = this.registry.get(op.ownerKey);
    if (!ownerOps) {
      ownerOps = new Map();
      this.registry.set(op.ownerKey, ownerOps);
    }
    for (const existing of ownerOps.values()) {
      if (!existing.settled && existing.identity === op.identity) {
        return new Error("该目标已有在途操作，请等待其结束");
      }
    }
    if (ownerOps.size >= LIVE_REGISTRY_LIMIT && !ownerOps.has(op.snapshot.operationId)) {
      return new Error("该所有者的操作记录已达上限，请清除已完成的结果");
    }
    ownerOps.set(op.snapshot.operationId, op);
    return null;
  }

  // remove deletes an operation and notifies the subscribers of both
  // channels. It ends a lifecycle without an outcome (production
  // challenge) or clears a retained outcome (user action); either way the
  // record is gone for every current and future view.
  remove(ownerKey: string, operationId: string): void {
    const ownerOps = this.registry.get(ownerKey);
    const existing = ownerOps?.get(operationId);
    if (!ownerOps || !existing) return;
    ownerOps.delete(operationId);
    if (ownerOps.size === 0) this.registry.delete(ownerKey);
    const event: LiveOperationEvent = { type: "clear", operation: existing };
    this.emitOp(ownerKey, operationId, event);
    this.emitOutcome(existing.ownerKey, existing.identity, event);
  }

  // getUnsettled returns the owner's single unsettled operation for the
  // identity; the store invariant guarantees there is at most one.
  getUnsettled(ownerKey: string, identity: string): LiveOperation | null {
    const ownerOps = this.registry.get(ownerKey);
    if (!ownerOps) return null;
    for (const op of ownerOps.values()) {
      if (!op.settled && op.identity === identity) return op;
    }
    return null;
  }

  // getOutcome returns the latest settled outcome retained for the owner
  // and identity; outcomes stay until a user explicitly clears them.
  getOutcome(ownerKey: string, identity: string): LiveOperation | null {
    const ownerOps = this.registry.get(ownerKey);
    if (!ownerOps) return null;
    let latest: LiveOperation | null = null;
    for (const op of ownerOps.values()) {
      if (op.settled && op.identity === identity) latest = op;
    }
    return latest;
  }

  // subscribeOp reads the operation and atomically registers the listener,
  // so no notification is lost between the read and the subscription.
  subscribeOp(
    ownerKey: string,
    operationId: string,
    listener: (event: LiveOperationEvent) => void,
  ): { operation: LiveOperation | null; unsubscribe: Unsubscribe } {
    const operation = this.registry.get(ownerKey)?.get(operationId) ?? null;
    const key = this.opKey(ownerKey, operationId);
    let listeners = this.opSubscribers.get(key);
    if (!listeners) {
      listeners = new Map();
      this.opSubscribers.set(key, listeners);
    }
    const id = ++this.nextSubscriberId;
    listeners.set(id, listener);
    return {
      operation,
      unsubscribe: () => {
        const current = this.opSubscribers.get(key);
        if (current) {
          current.delete(id);
          if (current.size === 0) this.opSubscribers.delete(key);
        }
      },
    };
  }

  // subscribeOutcome reads the identity's current unsettled operation and
  // retained outcome and atomically registers the listener for settle and
  // clear notifications of that identity.
  subscribeOutcome(
    ownerKey: string,
    identity: string,
    listener: (event: LiveOperationEvent) => void,
  ): { operation: LiveOperation | null; outcome: LiveOperation | null; unsubscribe: Unsubscribe } {
    const key = this.identityKey(ownerKey, identity);
    let listeners = this.outcomeSubscribers.get(key);
    if (!listeners) {
      listeners = new Map();
      this.outcomeSubscribers.set(key, listeners);
    }
    const id = ++this.nextSubscriberId;
    listeners.set(id, listener);
    return {
      operation: this.getUnsettled(ownerKey, identity),
      outcome: this.getOutcome(ownerKey, identity),
      unsubscribe: () => {
        const current = this.outcomeSubscribers.get(key);
        if (current) {
          current.delete(id);
          if (current.size === 0) this.outcomeSubscribers.delete(key);
        }
      },
    };
  }

  private emitOp(ownerKey: string, operationId: string, event: LiveOperationEvent): void {
    const listeners = this.opSubscribers.get(this.opKey(ownerKey, operationId));
    if (!listeners) return;
    for (const listener of [...listeners.values()]) {
      listener(event);
    }
  }

  private emitOutcome(ownerKey: string, identity: string, event: LiveOperationEvent): void {
    const listeners = this.outcomeSubscribers.get(this.identityKey(ownerKey, identity));
    if (!listeners) return;
    for (const listener of [...listeners.values()]) {
      listener(event);
    }
  }

  // markSettled records the operation's outcome and notifies every
  // subscriber. The settled operation is retained in the registry until a
  // user explicitly clears it, so the outcome is replayed idempotently to
  // every current and future view of the owner and target.
  markSettled(op: LiveOperation): void {
    op.settled = true;
    const event: LiveOperationEvent = { type: "settle", operation: op };
    this.emitOp(op.ownerKey, op.snapshot.operationId, event);
    this.emitOutcome(op.ownerKey, op.identity, event);
  }

  // notifyCancel delivers a cancel-state change (pending, timeout, or
  // failure) to every current subscriber of the operation.
  notifyCancel(op: LiveOperation): void {
    this.emitOp(op.ownerKey, op.snapshot.operationId, { type: "cancel", operation: op });
  }

  // notifyIgnore delivers the operation-level ignore to every current
  // subscriber, so all views show the same ignored state.
  notifyIgnore(op: LiveOperation): void {
    this.emitOp(op.ownerKey, op.snapshot.operationId, { type: "ignore", operation: op });
  }

  // reset clears the registry and all subscribers; it exists for tests.
  reset(): void {
    this.registry.clear();
    this.opSubscribers.clear();
    this.outcomeSubscribers.clear();
  }
}

export const liveOperationStore = new LiveOperationStore();
