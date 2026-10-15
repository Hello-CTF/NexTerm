/** @vitest-environment jsdom */
// Verifies byte-for-byte alignment between frontend payload construction and Go json.Marshal. Fixtures are real Go encoding/json output
// (structs copied field by field from internal/sync/object.go), covering sessionId position, content omitted for zero chunks,
// <>& HTML escaping, U+2028/U+2029 and multilingual text; not self-proven with JS logic.
import { describe, expect, it } from "vitest";
import type { TranscriptChunk, TranscriptSummary } from "../ipc/commands";
import type { SyncCollectAIGrantDevice, SyncCollectAIGrantMode, SyncCollectAIGrantRule, SyncCollectAIProfile, SyncCollectAsset, SyncCollectCredential, SyncCollectKnownHost, SyncCollectTombstone } from "../ipc/types";
import {
  aiGrantDevicePayload,
  aiGrantModePayload,
  aiGrantRulePayload,
  aiProfilePayload,
  collectAssetPayload,
  credentialPayload,
  groupPayload,
  knownHostPayload,
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

describe("transcriptPayload matches Go transcriptObject byte for byte", () => {
  it("places sessionId immediately after id and emits content for a basic nonempty record", () => {
    const payload = transcriptPayload(summary({ id: "t1", sessionId: "s1", assetId: "a1", bytes: 3, chunks: 1 }), [CHUNK_AQID]);
    expect(marshalSyncPayload(payload)).toBe(fixtureText("transcriptBasic"));
  });

  it("distinguishes an otherwise identical layout by a larger endedAt", () => {
    const payload = transcriptPayload(summary({ id: "t1", sessionId: "s1", assetId: "a1", startedAt: 9, endedAt: 10, bytes: 3, chunks: 1 }), [CHUNK_AQID]);
    expect(marshalSyncPayload(payload)).toBe(fixtureText("transcriptEnded10"));
  });

  it("omits content for contentOmitted and places contentOmitted after truncated", () => {
    const payload = transcriptPayload(
      summary({ id: "t1", sessionId: "s1", assetId: "a1", bytes: 134217728, chunks: 9, truncated: true, contentOmitted: true }),
      null,
    );
    expect(marshalSyncPayload(payload)).toBe(fixtureText("transcriptOmitted"));
  });

  it("escapes multilingual text, <>&, and U+2028/U+2029 exactly like Go", () => {
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

  it("omits content for zero chunks using Go empty-slice omitempty semantics and has no sessionId", () => {
    const payload = transcriptPayload(summary({ id: "t-2" }), []);
    expect(marshalSyncPayload(payload)).toBe(fixtureText("transcriptZeroChunks"));
  });

  it("keeps the local compressed size out of the deterministic sync payload", () => {
    const payload = transcriptPayload(summary({ id: "t-2", storedBytes: 40 }), []);
    expect(marshalSyncPayload(payload)).toBe(fixtureText("transcriptZeroChunks"));
  });

  it("omits kind for output chunks and places kind between ts and data for input/resize chunks", () => {
    const payload = transcriptPayload(
      summary({ id: "t1", sessionId: "s1", assetId: "a1", bytes: 26, chunks: 3 }),
      [
        { seq: 1, tabId: "tab-1", ts: 10, kind: 0, dataBase64: "b3V0" },
        { seq: 2, tabId: "tab-1", ts: 11, kind: 1, dataBase64: "aW4=" },
        { seq: 3, tabId: "tab-1", ts: 12, kind: 2, dataBase64: "eyJjb2xzIjo4MCwicm93cyI6MjR9" },
      ],
    );
    expect(marshalSyncPayload(payload)).toBe(fixtureText("transcriptKinds"));
  });
});

describe("remaining object payloads match Go byte for byte", () => {
  it("omits parentId for a top-level group and escapes it consistently", () => {
    expect(marshalSyncPayload(groupPayload({ id: "g-1", parentId: null, name: "生产 <Root> & 组", sort: 1, createdAt: 1, updatedAt: 2 }))).toBe(fixtureText("groupRoot"));
  });

  it("includes parentId for a child group", () => {
    expect(marshalSyncPayload(groupPayload({ id: "g-2", parentId: "g-parent", name: "子组", sort: 2, createdAt: 3, updatedAt: 4 }))).toBe(fixtureText("groupChild"));
  });

  it("omits absent optional fields for an asset", () => {
    const a: SyncCollectAsset = {
      id: "a-1", kind: "ssh", name: "web-01", optionsJson: "{}", tags: "", note: "",
      sort: 0, createdAt: 1, updatedAt: 200,
    };
    expect(marshalSyncPayload(collectAssetPayload(a))).toBe(fixtureText("assetMinimal"));
  });

  it("includes deletedAt for a soft-deleted asset and preserves Go field order", () => {
    const a: SyncCollectAsset = {
      id: "a-del", groupId: "g1", kind: "ssh", name: "old <&> 01", host: "10.0.0.8", port: 22,
      username: "root", authKind: "password", keyPath: "/home/u/.ssh/id_ed25519", credId: "c-1",
      optionsJson: "{}", tags: "x,y", note: "note <b>&", sort: 0, createdAt: 1, updatedAt: 100, deletedAt: 150,
    };
    expect(marshalSyncPayload(collectAssetPayload(a))).toBe(fixtureText("assetDeleted"));
  });

  it("escapes a credential secret containing <>& and U+2028 consistently", () => {
    const c: SyncCollectCredential = {
      id: "c-1", name: "生产 <口令> & more", kind: "password", updatedAt: 50,
      secret: "p@ss<>&\u2028w0rd", secretState: "revealed",
    };
    expect(marshalSyncPayload(credentialPayload(c))).toBe(fixtureText("credential"));
  });

  it("snippet", () => {
    expect(marshalSyncPayload(snippetPayload({ id: "s-1", groupId: "g1", name: "常用", body: "ls -la <&>", sort: 0, createdAt: 1, updatedAt: 2 }))).toBe(fixtureText("snippet"));
  });

  it("supports group and credential tombstones", () => {
    expect(marshalSyncPayload(tombstonePayload({ id: "g-del", targetKind: "group", deletedAt: 500 } satisfies SyncCollectTombstone))).toBe(fixtureText("tombstoneGroup"));
    expect(marshalSyncPayload(tombstonePayload({ id: "c-del", targetKind: "credential", deletedAt: 400 } satisfies SyncCollectTombstone))).toBe(fixtureText("tombstoneCredential"));
  });

  it("preserves the loser triple in a known_host conflict tombstone (M163 R4 semantics)", () => {
    expect(
      marshalSyncPayload(
        tombstonePayload({ id: "kh-1", targetKind: "known_host", deletedAt: 700, host: "10.0.0.9", port: 22, keyType: "ssh-ed25519" } satisfies SyncCollectTombstone),
      ),
    ).toBe(fixtureText("tombstoneKnownHostConflict"));
  });
});

describe("M165 known_host and AI profile payloads match Go byte for byte", () => {
  it("includes every knownHost field without omitempty", () => {
    const k: SyncCollectKnownHost = {
      id: "kh-1", host: "10.0.0.9", port: 22, keyType: "ssh-ed25519",
      fingerprint: "SHA256:5Va5w2K7bh2+5Nm4EwvX0n8QV2c7CXWq3R6e3d3w4Xk", addedAt: 1700000000000,
    };
    expect(marshalSyncPayload(knownHostPayload(k))).toBe(fixtureText("knownHostBasic"));
  });

  it("includes every aiProfile field, including fallbackModel/maxTokens/proxy/timeouts/circuit breaker, with <>& and U+2028 in the plaintext key", () => {
    const p: SyncCollectAIProfile = {
      id: "p-1", name: "生产 <档案> & more", baseUrl: "https://api.example.com/v1",
      apiKey: "sk-live<>&\u2028key", model: "gpt-4o", fallbackModel: "gpt-4o-mini",
      temperature: 0.7, contextWindow: 128000, maxTokens: 4096, proxy: "http://127.0.0.1:7890",
      stream: true, requestTimeoutSeconds: 60, idleTimeoutSeconds: 300,
      circuitFailureThreshold: 5, circuitCooldownSeconds: 30,
      updatedAt: 1700000001000, apiKeySet: true, apiKeyState: "revealed",
    };
    expect(marshalSyncPayload(aiProfilePayload(p))).toBe(fixtureText("aiProfileFull"));
  });

  it("omits default aiProfile fields, emits null proxy, and emits an empty string for an empty key", () => {
    const p: SyncCollectAIProfile = {
      id: "p-2", name: "minimal", baseUrl: "https://api.example.com/v1", model: "m",
      temperature: 0, contextWindow: 8192, proxy: null, stream: false,
      updatedAt: 1700000002000, apiKeySet: false, apiKeyState: "revealed",
    };
    expect(marshalSyncPayload(aiProfilePayload(p))).toBe(fixtureText("aiProfileMinimal"));
  });
});

describe("ai_grant payloads match Go byte for byte", () => {
  it("includes every device grant field without omitempty", () => {
    const g: SyncCollectAIGrantDevice = {
      id: "device/dev-1", deviceId: "dev-1", kinds: ["terminal_write", "session_exec"], updatedAt: 1700000000000,
    };
    expect(marshalSyncPayload(aiGrantDevicePayload(g))).toBe(fixtureText("aiGrantDevice"));
  });

  it("includes every rule field, including path/expiresAt, and HTML-escapes <>& in the path", () => {
    const r: SyncCollectAIGrantRule = {
      id: "rule/r-1", deviceId: "dev-1", action: "write_file", path: "/var/log/<app>&/**",
      expiresAt: 1800000000000, createdAt: 1700000000000, updatedAt: 1700000001000,
    };
    expect(marshalSyncPayload(aiGrantRulePayload(r))).toBe(fixtureText("aiGrantRuleFull"));
  });

  it("omits default rule fields for an empty path and zero expiresAt", () => {
    const r: SyncCollectAIGrantRule = {
      id: "rule/r-2", deviceId: "dev-2", action: "exec_commands", createdAt: 1700000000000, updatedAt: 1700000002000,
    };
    expect(marshalSyncPayload(aiGrantRulePayload(r))).toBe(fixtureText("aiGrantRuleMinimal"));
  });

  it("includes every device permission-mode field without omitempty", () => {
    const m: SyncCollectAIGrantMode = { id: "mode/dev-1", deviceId: "dev-1", mode: "read_only", updatedAt: 1700000000000 };
    expect(marshalSyncPayload(aiGrantModePayload(m))).toBe(fixtureText("aiGrantMode"));
  });
});
