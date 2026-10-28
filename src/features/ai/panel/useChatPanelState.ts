import { useCallback, useRef, useState, useMemo, useEffect } from "react";
import type { MessageDto, AiRunDto } from "../../../ipc/types";
import { messageHistoryStart, MESSAGE_HISTORY_PAGE_SIZE, earlierMessageHistoryStart } from "../aiWire";
import { replayableJobIds } from "../runRestore";
import { historyToItems } from "../conversation";
import { type AiPermissionConfig, aiApi, modelApi } from "../../../ipc/commands";
import { useConversationFollow } from "../conversationFollow";
import { useVirtualWindow } from "../conversationVirtual";
import { findItemMatches, stepMatch } from "../conversationSearch";
import { describeError } from "../../../ui/errorText";
import { productionSkillsApi } from "../../settings/skillsApi";
import { useUi } from "../../../app/store";
import { type RefChip, type AiChatBase } from "./aiSidebarShared";
import type { UseChatRunSessionOut } from "./useChatRunSession";

export function useChatPanelState(p: AiChatBase & UseChatRunSessionOut) {
  const { isLifecycleActive, conversationIdRef, toolHandlesRef, setToolHandleVersion, lastRefreshedHandleVersionRef, restoreBindingRef, restoreGenerationRef, setConversationId, onConversationChangeRef, stream, conv, permScope, pushToast, input, sessionId, workspaces } = p;

  const updateConversationId = useCallback((id: string | undefined, title?: string) => {
    if (!isLifecycleActive()) return;
    if (conversationIdRef.current !== id) {
      // A real conversation switch invalidates the tool-handle tracking of
      // the previous conversation; replayed events of the new one re-record.
      toolHandlesRef.current?.clear();
      setToolHandleVersion(0);
      lastRefreshedHandleVersionRef.current = 0;
    }
    conversationIdRef.current = id;
    restoreBindingRef.current = id === undefined
      ? null
      : { id, generation: restoreGenerationRef.current };
    setConversationId(id);
    onConversationChangeRef.current(id, title);
  }, [conversationIdRef, isLifecycleActive, lastRefreshedHandleVersionRef, onConversationChangeRef, restoreBindingRef, restoreGenerationRef, setConversationId, setToolHandleVersion, toolHandlesRef]);
  const isRestoreActive = useCallback(
    (generation: number, id?: string) =>
      isLifecycleActive() &&
      restoreGenerationRef.current === generation &&
      (id === undefined || conversationIdRef.current === id),
    [conversationIdRef, isLifecycleActive, restoreGenerationRef],
  );
  const messageHistoryRef = useRef<{
    id: string;
    messages: MessageDto[];
    skipJobIds: Set<string>;
  } | null>(null);
  const messageWindowStartRef = useRef(0);
  const [messageWindowStart, setMessageWindowStart] = useState(0);
  const resetMessageHistory = useCallback(
    (id: string, messages: MessageDto[], runs: AiRunDto[]) => {
      const start = messageHistoryStart(messages.length, MESSAGE_HISTORY_PAGE_SIZE);
      const skipJobIds = replayableJobIds(runs);
      messageHistoryRef.current = { id, messages, skipJobIds };
      messageWindowStartRef.current = start;
      setMessageWindowStart(start);
      stream.reset(historyToItems(stream.getState(), messages.slice(start), skipJobIds));
    },
    [stream],
  );
  const clearMessageHistory = useCallback(() => {
    messageHistoryRef.current = null;
    messageWindowStartRef.current = 0;
    setMessageWindowStart(0);
  }, []);
  const loadOlderMessages = () => {
    const history = messageHistoryRef.current;
    if (!isLifecycleActive() || !history || history.id !== conversationIdRef.current) return;
    const next = earlierMessageHistoryStart(messageWindowStartRef.current, MESSAGE_HISTORY_PAGE_SIZE);
    if (next === messageWindowStartRef.current) return;
    messageWindowStartRef.current = next;
    setMessageWindowStart(next);
    stream.replaceHistory(
      historyToItems(stream.getState(), history.messages.slice(next), history.skipJobIds),
    );
  };
  const deletedConversationIdsRef = useRef<Set<string>>(new Set());
  const [perm, setPerm] = useState<AiPermissionConfig | null>(null);
  const [permError, setPermError] = useState<string | null>(null);
  const [permOpen, setPermOpen] = useState(false);
  const [atOpen, setAtOpen] = useState(false);
  // Typing "/" at the start of the message opens the command menu: built-in
  // commands first, then the user's enabled skills. Lists load when the menu
  // opens and reload the next time.
  const [skillOptions, setSkillOptions] = useState<{ name: string; description: string }[] | null>(null);
  const [modelOptions, setModelOptions] = useState<{ id: string; name: string; model: string; active: boolean }[] | null>(null);
  const [slashIndex, setSlashIndex] = useState(0);
  const [compacting, setCompacting] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [conversations, setConversations] = useState<{ id: string; title: string; updatedAt: number }[]>([]);
  const [runStatusByConv, setRunStatusByConv] = useState<Record<string, "running" | "interrupted">>({});
  const [historyStatus, setHistoryStatus] = useState<"loading" | "error" | "ready">("ready");
  const [historyError, setHistoryError] = useState<string | null>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const imageInputRef = useRef<HTMLInputElement>(null);
  const follow = useConversationFollow(conv.items);
  const virtual = useVirtualWindow(conv.items.length);
  const { scrollRef: followScrollRef } = follow;
  const { scrollRef: virtualScrollRef, revealIndex: virtualRevealIndex, clearReveal: virtualClearReveal } = virtual;
  const setScrollEl = useCallback(
    (el: HTMLElement | null) => {
      followScrollRef(el);
      virtualScrollRef(el);
    },
    [followScrollRef, virtualScrollRef],
  );
  const [conversationRuns, setConversationRuns] = useState<AiRunDto[]>([]);
  const [searchOpen, setSearchOpen] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [matchCursor, setMatchCursor] = useState(-1);
  const searchMatches = useMemo(
    () => findItemMatches(conv.items, searchQuery),
    [conv.items, searchQuery],
  );
  const activeMatchIndex = matchCursor >= 0 ? (searchMatches[matchCursor] ?? -1) : -1;
  const jumpToMatch = (direction: 1 | -1) => {
    const next = stepMatch(matchCursor, searchMatches.length, direction);
    setMatchCursor(next);
    const target = searchMatches[next];
    if (target !== undefined) virtual.revealIndex(target);
  };
  useEffect(() => {
    const next = searchMatches.length > 0 ? 0 : -1;
    setMatchCursor(next);
    const target = next >= 0 ? searchMatches[next] : undefined;
    if (target !== undefined) virtualRevealIndex(target);
    else virtualClearReveal();
  }, [searchMatches, virtualRevealIndex, virtualClearReveal]);
  useEffect(() => {
    if (!searchOpen) virtualClearReveal();
  }, [searchOpen, virtualClearReveal]);

  const [permDeviceOverride, setPermDeviceOverride] = useState(false);
  const loadPermission = useCallback(async (): Promise<AiPermissionConfig | null> => {
    try {
      const effective = await aiApi.getPermission(permScope);
      if (permScope) {
        const global = await aiApi.getPermission();
        setPermDeviceOverride(global.mode !== effective.mode);
      } else {
        setPermDeviceOverride(false);
      }
      setPerm(effective);
      setPermError(null);
      return effective;
    } catch (e) {
      setPermError(describeError(e));
      return null;
    }
  }, [permScope]);

  useEffect(() => {
    void loadPermission();
  }, [loadPermission]);

  useEffect(() => {
    if (permOpen) void loadPermission();
  }, [permOpen, loadPermission]);

  const savePerm = async (next: AiPermissionConfig) => {
    const prev = perm;
    setPerm(next);
    try {
      await aiApi.setPermission(next, permScope);
      if (permScope) setPermDeviceOverride(true);
    } catch (e) {
      setPerm(prev);
      pushToast("error", `无法保存权限设置：${describeError(e)}`);
    }
  };

  const switchMode = (mode: AiPermissionConfig["mode"]) => {
    void (async () => {
      const latest = await loadPermission();
      if (latest) await savePerm({ ...latest, mode });
    })();
  };

  const followGlobalMode = () => {
    void (async () => {
      if (!permScope) return;
      const prev = perm;
      try {
        await aiApi.setPermission(prev ?? { mode: "read_write", dangerRules: [] }, permScope, true);
        setPermDeviceOverride(false);
        await loadPermission();
      } catch (e) {
        setPerm(prev);
        pushToast("error", `无法保存权限设置：${describeError(e)}`);
      }
    })();
  };

  const slashQuery = /^\/([a-z0-9-]*)$/.exec(input)?.[1] ?? null;
  const modelQuery = /^\/model\s+(.*)$/i.exec(input)?.[1] ?? null;
  useEffect(() => {
    if (slashQuery === null) {
      setSkillOptions(null);
      return;
    }
    if (skillOptions !== null) return;
    let alive = true;
    productionSkillsApi.list().then(
      (entries) => {
        if (alive) setSkillOptions(entries.filter((entry) => !entry.disabled).map(({ name, description }) => ({ name, description })));
      },
      () => {
        if (alive) setSkillOptions([]);
      },
    );
    return () => {
      alive = false;
    };
  }, [slashQuery, skillOptions]);
  useEffect(() => {
    if (modelQuery === null) {
      setModelOptions(null);
      return;
    }
    if (modelOptions !== null) return;
    let alive = true;
    modelApi.overview().then(
      (view) => {
        if (alive) setModelOptions(view.profiles.map((profile) => ({ id: profile.id, name: profile.name, model: profile.model, active: profile.id === view.activeId })));
      },
      () => {
        if (alive) setModelOptions([]);
      },
    );
    return () => {
      alive = false;
    };
  }, [modelQuery, modelOptions]);
  useEffect(() => {
    setSlashIndex(0);
  }, [slashQuery, modelQuery]);

  const refCandidates = useMemo<RefChip[]>(() => {
    const out: RefChip[] = [];
    const sess = useUi.getState().sessions.find((s) => s.id === sessionId);
    if (sess) {
      out.push({
        id: `asset:${sess.id}`,
        kind: "asset",
        label: sess.name,
        detail: `${sess.kind} 会话`,
        refId: sess.id,
      });
    }
    for (const w of workspaces) {
      for (const p of w.panes) {
        for (const t of p.tabs) {
          if (t.kind === "terminal" && t.tabId) {
            out.push({
              id: `tab:${t.id}`,
              kind: "tab",
              label: t.title,
              detail: `${w.title} 的终端`,
              refId: t.tabId,
            });
            continue;
          }
          if ((t.kind === "files" || t.kind === "log") && t.path && t.sessionId) {
            out.push({
              id: `file:${t.id}`,
              kind: "file",
              label: t.title,
              detail: t.path,
              refId: t.id,
              path: t.path,
              sessionId: t.sessionId,
            });
          }
        }
      }
    }
    return out;
  }, [sessionId, workspaces]);

  const composeMessage = (text: string, refList: RefChip[]): string => {
    if (refList.length === 0) return text;
    const list = refList.map((r) => `- ${r.label}（${r.detail}）`).join("\n");
    return `[引用]\n${list}\n\n${text}`;
  };

  return { isRestoreActive, setConversationRuns, updateConversationId, clearMessageHistory, resetMessageHistory, permOpen, setPermOpen, setAtOpen, composeMessage, deletedConversationIdsRef, conversationRuns, inputRef, setRunStatusByConv, setHistoryStatus, setHistoryError, setConversations, historyOpen, setHistoryOpen, setCompacting, compacting, slashQuery, modelQuery, modelOptions, skillOptions, searchOpen, setSearchOpen, searchQuery, setSearchQuery, jumpToMatch, searchMatches, matchCursor, historyStatus, historyError, conversations, runStatusByConv, permError, loadPermission, perm, permDeviceOverride, followGlobalMode, switchMode, setScrollEl, messageWindowStart, loadOlderMessages, virtual, activeMatchIndex, follow, slashIndex, setSlashIndex, atOpen, refCandidates, imageInputRef };
}

export type UseChatPanelStateOut = ReturnType<typeof useChatPanelState>;
