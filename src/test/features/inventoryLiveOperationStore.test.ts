import { beforeEach, describe, expect, it } from "vitest";
import { liveOperationStore, type LiveOperation } from "../../features/explorer/inventoryLiveOperationStore";

function op(ownerKey: string, operationId: string, identity: string): LiveOperation {
  return {
    snapshot: { kind: "docker-context", target: {}, action: "exec", command: ["true"], operationId },
    ownerKey,
    identity,
    status: "running",
  };
}

beforeEach(() => {
  liveOperationStore.reset();
});

describe("live operation store", () => {
  it("rejects a second unsettled operation for the same owner and identity", () => {
    expect(liveOperationStore.store(op("owner-a", "op-1", "id-x"))).toBeNull();
    const error = liveOperationStore.store(op("owner-a", "op-2", "id-x"));
    expect(error?.message).toContain("该目标已有在途操作");
    expect(liveOperationStore.getUnsettled("owner-a", "id-x")?.snapshot.operationId).toBe("op-1");
  });

  it("allows the same identity under different owners and different identities under one owner", () => {
    expect(liveOperationStore.store(op("owner-a", "op-1", "id-x"))).toBeNull();
    expect(liveOperationStore.store(op("owner-b", "op-2", "id-x"))).toBeNull();
    expect(liveOperationStore.store(op("owner-a", "op-3", "id-y"))).toBeNull();
  });

  it("rejects visibly at capacity without evicting live operations or outcomes", () => {
    for (let index = 0; index < 31; index++) {
      expect(liveOperationStore.store(op("owner-a", `live-${index}`, `id-${index}`))).toBeNull();
    }
    const settled = op("owner-a", "settled-1", "id-settled");
    expect(liveOperationStore.store(settled)).toBeNull();
    liveOperationStore.markSettled(settled);

    const error = liveOperationStore.store(op("owner-a", "op-33", "id-other"));
    expect(error?.message).toContain("操作记录已达上限");
    // The rejected launch cleared nothing: the outcome and every live
    // operation are still registered.
    expect(liveOperationStore.getOutcome("owner-a", "id-settled")?.snapshot.operationId).toBe("settled-1");
    expect(liveOperationStore.getUnsettled("owner-a", "id-0")?.snapshot.operationId).toBe("live-0");
  });

  it("acks the current outcome on a new launch and never revives it", () => {
    const first = op("owner-a", "op-1", "id-x");
    expect(liveOperationStore.store(first)).toBeNull();
    liveOperationStore.markSettled(first);
    expect(liveOperationStore.getOutcome("owner-a", "id-x")?.snapshot.operationId).toBe("op-1");

    const events: string[] = [];
    const { unsubscribe } = liveOperationStore.subscribeOutcome("owner-a", "id-x", (event) => {
      events.push(`${event.type}:${event.operation.snapshot.operationId}`);
    });
    const second = op("owner-a", "op-2", "id-x");
    expect(liveOperationStore.store(second)).toBeNull();
    expect(events).toEqual(["clear:op-1", "start:op-2"]);
    expect(liveOperationStore.getOutcome("owner-a", "id-x")).toBeNull();

    liveOperationStore.markSettled(second);
    expect(liveOperationStore.getOutcome("owner-a", "id-x")?.snapshot.operationId).toBe("op-2");
    unsubscribe();
  });

  it("notifies every subscriber and isolates out-of-order unsubscribe", () => {
    const operation = op("owner-a", "op-1", "id-x");
    expect(liveOperationStore.store(operation)).toBeNull();
    const seen: string[] = [];
    const first = liveOperationStore.subscribeOp("owner-a", "op-1", (event) => seen.push(`first:${event.type}`));
    const second = liveOperationStore.subscribeOp("owner-a", "op-1", (event) => seen.push(`second:${event.type}`));
    expect(first.operation).toBe(operation);

    operation.ignored = true;
    liveOperationStore.notifyIgnore(operation);
    expect(seen).toEqual(["first:ignore", "second:ignore"]);
    first.unsubscribe();
    liveOperationStore.notifyCancel(operation);
    expect(seen).toEqual(["first:ignore", "second:ignore", "second:cancel"]);
    second.unsubscribe();
  });

  it("retains a settled outcome until cleared and replays it to every subscriber", () => {
    const operation = op("owner-a", "op-1", "id-x");
    expect(liveOperationStore.store(operation)).toBeNull();
    liveOperationStore.markSettled(operation);
    expect(liveOperationStore.getOutcome("owner-a", "id-x")).toBe(operation);

    const events: string[] = [];
    const subscription = liveOperationStore.subscribeOutcome("owner-a", "id-x", (event) => events.push(event.type));
    expect(subscription.outcome).toBe(operation);
    liveOperationStore.remove("owner-a", "op-1");
    expect(events).toEqual(["clear"]);
    expect(liveOperationStore.getOutcome("owner-a", "id-x")).toBeNull();
    subscription.unsubscribe();
  });
});
