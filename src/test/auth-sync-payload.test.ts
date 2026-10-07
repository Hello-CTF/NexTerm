/** @vitest-environment jsdom */
// 前端载荷构造与 Go json.Marshal 的逐字节对齐验证。fixtures 是真实 Go encoding/json 输出
// (结构体逐字段拷贝自 internal/sync/object.go),覆盖 sessionId 位置、零 chunk 省略 content、
// <>& HTML 转义、U+2028/U+2029 与多语言文本;不是用 JS 逻辑自证。
import { describe, expect, it } from "vitest";
import type { TranscriptChunk, TranscriptSummary } from "../ipc/commands";
import type { SyncCollectAsset, SyncCollectCredential, SyncCollectTombstone } from "../ipc/types";
import {
  collectAssetPayload,
  credentialPayload,
  groupPayload,
  marshalSyncPayload,
  snippetPayload,
  tombstonePayload,
  transcriptPayload,
} from "../features/auth/syncPayload";
import { GO_SYNC_FIXTURES } from "./auth-sync-fixtures";

function fixtureText(name: string): string {
  const bin = atob(GO_SYNC_FIXTURES[name]);
  const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0));
  return new TextDecoder().decode(bytes);
}

function summary(partial: Partial<TranscriptSummary> & { id: string }): TranscriptSummary {
  return {
    sessionId: "",
    assetId: "a-1",
    assetName: "web-01",
    assetKind: "ssh",
    assetDeleted: false,
    startedAt: 1,
    endedAt: 2,
    bytes: 0,
    chunks: 0,
    truncated: false,
    active: false,
    ...partial,
  };
}

const CHUNK_AQID: TranscriptChunk = { seq: 1, tabId: "tab-1", ts: 1, dataBase64: "AQID" };

describe("transcriptPayload 与 Go transcriptObject 逐字节一致", () => {
  it("基础记录: sessionId 紧随 id,内容非空输出 content", () => {
    const payload = transcriptPayload(summary({ id: "t1", sessionId: "s1", assetId: "a1", bytes: 3, chunks: 1 }), [CHUNK_AQID]);
    expect(marshalSyncPayload(payload)).toBe(fixtureText("transcriptBasic"));
  });

  it("endedAt 更大: 同一布局按 endedAt 区分", () => {
    const payload = transcriptPayload(summary({ id: "t1", sessionId: "s1", assetId: "a1", startedAt: 9, endedAt: 10, bytes: 3, chunks: 1 }), [CHUNK_AQID]);
    expect(marshalSyncPayload(payload)).toBe(fixtureText("transcriptEnded10"));
  });

  it("contentOmitted: 不输出 content,contentOmitted 在 truncated 之后", () => {
    const payload = transcriptPayload(
      summary({ id: "t1", sessionId: "s1", assetId: "a1", bytes: 134217728, chunks: 9, truncated: true, contentOmitted: true }),
      null,
    );
    expect(marshalSyncPayload(payload)).toBe(fixtureText("transcriptOmitted"));
  });

  it("多语言与 <>&、U+2028/U+2029: 转义与 Go 完全一致", () => {
    const payload = transcriptPayload(
      summary({
        id: "t-1",
        sessionId: "s-1",
        assetName: "web-日本語-<prod>&-01\u2028line\u2029sep",
        bytes: 6,
        chunks: 2,
      }),
      [
        { seq: 1, tabId: "tab-1", ts: 10, dataBase64: "AQID" },
        { seq: 2, tabId: "tab-2", ts: 11, dataBase64: "+/8A" },
      ],
    );
    expect(marshalSyncPayload(payload)).toBe(fixtureText("transcriptFull"));
  });

  it("零 chunk: 空内容省略 content 字段(Go 空切片 omitempty),无 sessionId", () => {
    const payload = transcriptPayload(summary({ id: "t-2" }), []);
    expect(marshalSyncPayload(payload)).toBe(fixtureText("transcriptZeroChunks"));
  });
});

describe("其余对象载荷与 Go 逐字节一致", () => {
  it("group: 顶级省略 parentId,转义一致", () => {
    expect(marshalSyncPayload(groupPayload({ id: "g-1", parentId: null, name: "生产 <Root> & 组", sort: 1, createdAt: 1, updatedAt: 2 }))).toBe(fixtureText("groupRoot"));
  });

  it("group: 子级带 parentId", () => {
    expect(marshalSyncPayload(groupPayload({ id: "g-2", parentId: "g-parent", name: "子组", sort: 2, createdAt: 3, updatedAt: 4 }))).toBe(fixtureText("groupChild"));
  });

  it("asset: 可选字段缺省省略", () => {
    const a: SyncCollectAsset = {
      id: "a-1", kind: "ssh", name: "web-01", optionsJson: "{}", tags: "", note: "",
      sort: 0, createdAt: 1, updatedAt: 200,
    };
    expect(marshalSyncPayload(collectAssetPayload(a))).toBe(fixtureText("assetMinimal"));
  });

  it("asset: 软删带 deletedAt 且字段顺序与 Go 一致", () => {
    const a: SyncCollectAsset = {
      id: "a-del", groupId: "g1", kind: "ssh", name: "old <&> 01", host: "10.0.0.8", port: 22,
      username: "root", authKind: "password", keyPath: "/home/u/.ssh/id_ed25519", credId: "c-1",
      optionsJson: "{}", tags: "x,y", note: "note <b>&", sort: 0, createdAt: 1, updatedAt: 100, deletedAt: 150,
    };
    expect(marshalSyncPayload(collectAssetPayload(a))).toBe(fixtureText("assetDeleted"));
  });

  it("credential: secret 含 <>& 与 U+2028 时转义一致", () => {
    const c: SyncCollectCredential = {
      id: "c-1", name: "生产 <口令> & more", kind: "password", updatedAt: 50,
      secret: "p@ss<>&\u2028w0rd", secretState: "revealed",
    };
    expect(marshalSyncPayload(credentialPayload(c))).toBe(fixtureText("credential"));
  });

  it("snippet", () => {
    expect(marshalSyncPayload(snippetPayload({ id: "s-1", groupId: "g1", name: "常用", body: "ls -la <&>", sort: 0, createdAt: 1, updatedAt: 2 }))).toBe(fixtureText("snippet"));
  });

  it("tombstone: group 与 credential 两类", () => {
    expect(marshalSyncPayload(tombstonePayload({ id: "g-del", targetKind: "group", deletedAt: 500 } satisfies SyncCollectTombstone))).toBe(fixtureText("tombstoneGroup"));
    expect(marshalSyncPayload(tombstonePayload({ id: "c-del", targetKind: "credential", deletedAt: 400 } satisfies SyncCollectTombstone))).toBe(fixtureText("tombstoneCredential"));
  });
});
