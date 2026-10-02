// 终端标签页：xterm + 工具条（搜索 / 编码 / 录制 / 命令块）。
import { useCallback, useEffect, useRef, useState, type MouseEvent as ReactMouseEvent } from "react";
import { XtermView, type TerminalHandle } from "./XtermView";
import { CommandBlockPanel } from "./CommandBlockPanel";
import { TerminalKeysBar } from "./TerminalKeysBar";
import { resolveWinrmMode } from "./terminalPolicy";
import type { CommandBlock } from "./commandBlocks";
import { sessionApi, terminalApi } from "../../ipc/commands";
import { listenEvent, EVENTS, type TerminalControlEvent } from "../../ipc/events";
import { clientId } from "../../ipc/env";
import { takePendingCommand, useUi } from "../../app/store";
import { describeTarget, finishSave, pickSavePath, promptText } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { ContextMenu, type ContextMenuState, type MenuItem } from "../../ui/ContextMenu";
import {
  IconArrowDown,
  IconArrowUp,
  IconClose,
  IconCommand,
  IconCopy,
  IconEdit,
  IconEye,
  IconGamepad,
  IconList,
  IconMergeH,
  IconPlug,
  IconRefresh,
  IconSave,
  IconSearch,
  IconSplitH,
  IconStop,
} from "../../ui/icons";

export interface TerminalPaneProps {
  sessionId: string;
  title: string;
  winrm?: boolean;
  /** 非空表示「容器内终端」，attach 走 docker exec。 */
  containerId?: string;
  /** store 里的标签 id：attach 成功后要把内核 tabId 写回该标签，关闭时才能回收 PTY。 */
  storeTabId: string;
  /**
   * 要**接管**的内核标签 id（从服务端恢复 / 从后台会话面板接回来时带上）。
   *
   * 非空 ⇒ XtermView 走 `terminal_attach_tab`（不新开 shell，回放已有输出）。
   * ⚠️ 只在**挂载那一刻**取一次值，见下方 `resumeRef`。
   */
  resumeTabId?: string;
  /** 所在标签是否激活（切标签不再卸载组件，靠它同步可见性）。 */
  visible?: boolean;
}

const ENCODINGS = ["utf-8", "gbk", "gb18030", "big5", "latin1"];

type GridControlEvent = TerminalControlEvent & {
  cols?: number;
  rows?: number;
  gridRevision?: number;
};

/** 命令块数量达到这个值就自动展开侧栏（3 条以上已经值得一眼看到）。 */
const AUTO_OPEN_BLOCKS = 3;

/** 当前 store 里该标签是否处于「连接已失效」态（找不到标签返回 false）。 */
function isStoreTabDead(id: string): boolean {
  return useUi
    .getState()
    .workspaces.some((w) => w.panes.some((p) => p.tabs.some((t) => t.id === id && t.dead === true)));
}

export function TerminalPane({
  sessionId,
  title,
  winrm,
  containerId,
  storeTabId,
  resumeTabId,
  visible,
}: TerminalPaneProps) {
  const [kernelTabId, setKernelTabId] = useState<string | null>(null);
  const [searchOpen, setSearchOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [recording, setRecording] = useState(false);
  const [encoding, setEncoding] = useState("utf-8");
  const [blocks, setBlocks] = useState<CommandBlock[]>([]);
  const [blocksOpen, setBlocksOpen] = useState(false);
  /** 终端选区右键菜单（目前只有「向 AI 提问」/「复制」）。 */
  const [menu, setMenu] = useState<ContextMenuState | null>(null);
  /**
   * 输入控制权（单点模式）：当前服务端认定的控制者 / 观看人数 / 进程是否已结束。
   *
   * 拿它和本机身份 `clientId()` 比：不相等就是这个终端正被别人操作，
   * 界面要挂观察者遮罩，且**不能**再去调 `terminal_resize`。
   */
  const [control, setControl] = useState<{
    controller: string | null;
    /** 通道数（含自己）：同一台设备多开一个页面就会 +1。 */
    subscribers: number;
    /** 观看设备数（按 clientId 去重）：徽章「N 个设备正在观看」用它。 */
    viewers: number;
    exited: boolean;
  } | null>(null);
  const [remoteGrid, setRemoteGrid] = useState<{
    cols: number;
    rows: number;
    revision: number;
  } | null>(null);
  const [claiming, setClaiming] = useState(false);
  /**
   * 这个标签背后的连接已经失效（attach 拿到 `not_found`）：服务端重启后布局里的
   * `tabId` 全都失效，或会话已被回收。
   *
   * 非空 ⇒ 挂「连接已失效」遮罩，给「重新连接这台主机 / 关闭标签」两个出路，
   * 而不是让终端停在一行黄字上敲不动（输入被 `if (!kernelTabId) return` 吃掉）。
   */
  const [attachDead, setAttachDead] = useState(false);
  const [reconnecting, setReconnecting] = useState(false);
  /**
   * 重挂 XtermView 用的自增 key。
   *
   * `resumeRef` 是「只在挂载那一刻取值」的（防止 attach 成功后 `tabId` 回写导致
   * effect 重跑、把接管变成新开 shell），所以**光改 `resumeRef.current` 不会触发
   * attach effect 重跑** —— 必须靠 key 变化把组件整个重挂。漏了这条的表现是
   * 「点了重新连接但终端还是死的」。
   */
  const [epoch, setEpoch] = useState(0);
  const me = clientId();
  /**
   * resumeTabId 只在**挂载那一刻**取值。
   *
   * 不能直接跟着 prop 走：attach 成功后 `onAttach` 会把内核 tabId 写回 store，
   * 上层重新渲染时传下来的 tabId 就从 undefined 变成了那个 id —— 如果继续
   * 跟着它变，XtermView 的 attach effect（依赖里有 resumeTabId）会重跑一次，
   * 轻则清屏重放，重则按"新建"分支再开一个 shell。
   */
  const resumeRef = useRef(resumeTabId);
  /** 观察者态下每按键都会撞 not_controller；提示只给一次，别刷屏。 */
  const observerHintAt = useRef(0);
  /** 普通写入错误也别每按键弹一次。 */
  const writeErrorAt = useRef(0);
  const controlRef = useRef(control);
  controlRef.current = control;
  /**
   * 内核标签 id 的 ref：`terminal://control` 订阅只挂一次（挂载时），
   * 那时 `kernelTabId` 还是 null，事件回调必须读 ref 才能拿到最新的值来过滤。
   */
  const kernelTabIdRef = useRef<string | null>(null);
  kernelTabIdRef.current = kernelTabId;
  /** 用户是否手动关过侧栏——关过之后就不再自动弹，尊重用户选择。 */
  const userClosedBlocks = useRef(false);
  /**
   * 正在录制的落点。
   *
   * 之所以要留到停止时：服务端模式下 `pickSavePath` 给的是**盒子上的暂存文件**，
   * 录的是它；停录之后还要把内容交给浏览器保存（见 `toggleRecord` 的停止分支）。
   * 桌面模式下这就是那个真实路径，多留一份也无害。
   */
  const recordTarget = useRef<{ path: string; name: string } | null>(null);
  const searchApi = useRef<{
    findNext: (t: string) => void;
    findPrevious: (t: string) => void;
  } | null>(null);
  const handleRef = useRef<TerminalHandle | null>(null);
  const pushToast = useUi((s) => s.pushToast);
  const sessionKind = useUi((s) => s.sessions.find((x) => x.id === sessionId)?.kind);
  const sessionStatus = useUi((s) => s.sessions.find((x) => x.id === sessionId)?.status);
  const effectiveWinrm = resolveWinrmMode(winrm, sessionKind);
  const statusText =
    sessionStatus === "connected"
      ? "已连接"
      : sessionStatus === "reconnecting"
        ? "重连中"
        : sessionStatus === "connecting"
          ? "连接中"
          : sessionStatus === "failed"
            ? "连接失败"
            : "已断开";
  /** 「重新连接」只在会话确实掉线时才有意义（连着的时候重连只是白抖一下）。 */
  const canReconnect =
    sessionStatus === "disconnected" || sessionStatus === "failed" || sessionStatus === undefined;
  // winrm 是非交互行模式，没有「命令 + 输出」的块语义
  const blocksSupported = !effectiveWinrm;

  // 命令块攒够 AUTO_OPEN_BLOCKS 条就自动展开（用户手动关过则不再打扰）
  useEffect(() => {
    if (!blocksSupported || userClosedBlocks.current) return;
    if (blocks.length >= AUTO_OPEN_BLOCKS) setBlocksOpen(true);
  }, [blocks.length, blocksSupported]);

  /* ── 输入控制权（单点模式）─────────────────────────────────────────── */

  /**
   * 容器内 exec / WinRM 行模式没有控制权语义（内核那边 `controller` 恒为 null）。
   * 对它们套观察者遮罩会把正常可用的终端锁死，所以这两类直接视为"我就是控制者"。
   */
  const controlSupported = !containerId && !effectiveWinrm;
  const isObserver = controlSupported && control !== null && control.controller !== me;
  /** 观察者不调 resize（会报 not_controller，而且观察者本就不该改 PTY 尺寸）。 */
  const canResize = !isObserver;

  /** 从内核刷新一次控制权状态（谁在持权 / 几个人在看 / 进程是否结束）。 */
  const refreshControl = useCallback(
    async (tabIdArg?: string) => {
      // attach 回调里刚 setKernelTabId，本轮的闭包还是 null —— 所以允许显式传 id。
      const tabId = tabIdArg ?? kernelTabId;
      if (!tabId) return;
      try {
        const list = await terminalApi.listLive();
        const hit = list.find((t) => t.tabId === tabId);
        if (hit) {
          setControl({
            controller: hit.controller,
            subscribers: hit.subscribers,
            viewers: hit.viewers,
            exited: hit.exited,
          });
        } else {
          // 列表里没有 = 标签已经不在内核里了（进程结束 / 被别处关掉）
          setControl((c) => (c ? { ...c, exited: true } : c));
        }
      } catch {
        // 拉不到就维持现状：控制权判定不该因为一次查询失败把用户踢成观察者
      }
    },
    [kernelTabId],
  );

  /**
   * 往终端写数据（用户按键 / 粘贴 / 命令输入统一走这里）。
   *
   * 「别人正持着键盘」不是错误，是单点模式的正常状态 —— 内核专门用了
   * `not_controller` 这个错误码让我们区别对待（见 `src-tauri/src/error.rs`）。
   * 弹红色错误框会让用户以为终端坏了，其实点一下「接管控制」就好。
   */
  const sendData = useCallback(
    async (text: string) => {
      if (!kernelTabId) return;
      // 观察者一律不发写入。
      // · 内核本来就会用 `not_controller` 拒掉，本地先拦一下，反馈更即时；
      // · 更要紧的是**鼠标**：远端程序开了鼠标跟踪后，xterm 会把每次移动都编码成
      //   转义序列走 `onData`。任务4 放行了指针（为了能让观察者选中/滚动）之后，
      //   若不拦，观察者只是划一下鼠标就会往内核打几十条注定被拒的写入 ——
      //   既刷 IPC，也会把 `observerHintAt` 那条 4s 节流提示顶爆。干脆不发。
      if (isObserver) {
        const now = Date.now();
        if (now - observerHintAt.current > 4000) {
          observerHintAt.current = now;
          pushToast("info", "终端正在其他设备上操作中，点「接管控制」可接手");
        }
        return;
      }
      try {
        await terminalApi.write(kernelTabId, new TextEncoder().encode(text));
      } catch (e) {
        const code = (e as { code?: string } | null)?.code;
        if (code === "not_controller") {
          // 立刻切到观察者态（遮罩挡住输入），再去内核确认现在是谁在持权。
          // 只在"本以为自己是控制者"时才刷 —— 已经是观察者时逐按键查一遍纯属浪费。
          if (controlRef.current?.controller === me) {
            setControl((c) => (c ? { ...c, controller: "__other__" } : c));
            void refreshControl();
          }
          const now = Date.now();
          if (now - observerHintAt.current > 4000) {
            observerHintAt.current = now;
            pushToast("info", "终端正在其他设备上操作中，点「接管控制」可接手");
          }
          return;
        }
        // 真实错误（连接断了、标签没了）才报；逐按键报错会刷屏，节流一下。
        const now = Date.now();
        if (now - writeErrorAt.current > 5000) {
          writeErrorAt.current = now;
          pushToast("error", `写入终端失败：${describeError(e)}`);
        }
      }
    },
    [kernelTabId, isObserver, me, pushToast, refreshControl],
  );

  /** 接管控制权：成为操作者，并按**自己的**窗口尺寸刷新 PTY（最后活跃者赢）。 */
  const takeControl = useCallback(async () => {
    if (!kernelTabId || claiming) return;
    setClaiming(true);
    try {
      const prev = await terminalApi.claim(kernelTabId);
      setControl((c) =>
        c ? { ...c, controller: me } : { controller: me, subscribers: 1, viewers: 1, exited: false },
      );
      // 接管前本地目标尺寸可能与服务端不同；统一由网格协调器发送一次最新值。
      handleRef.current?.fit();
      pushToast("success", prev && prev !== me ? "已接管控制权（对方转为只读观看）" : "已取得控制权");
    } catch (e) {
      pushToast("error", `接管失败：${describeError(e)}`);
    } finally {
      setClaiming(false);
    }
  }, [kernelTabId, claiming, me, pushToast]);

  /**
   * 订阅内核的 `terminal://control`：别人夺权 / 有人加入观看 / 进程结束，
   * 观察者画面**立刻**更新，而不是等自己下一次按键被 `not_controller` 顶回来。
   *
   * 这个事件是**全局广播**（一次性发给所有终端），所以必须按 `payload.tabId`
   * 过滤 —— 不过滤的话别的终端一夺权，这个终端也会跟着翻观察者态。
   *
   * 只订阅一次（挂载时）：事件回调读 `kernelTabIdRef`，避免因为 kernelTabId
   * 从 null 变成 id 而重订一遍（重订期间会漏事件）。
   */
  useEffect(() => {
    let unlisten: (() => void) | null = null;
    let cancelled = false;
    void listenEvent<GridControlEvent>(EVENTS.terminalControl, (p) => {
      if (p.tabId !== kernelTabIdRef.current) return;
      setControl({
        controller: p.controller,
        subscribers: p.subscribers,
        viewers: p.viewers,
        exited: p.exited,
      });
      if (
        typeof p.gridRevision === "number" &&
        p.gridRevision > 0 &&
        typeof p.cols === "number" &&
        typeof p.rows === "number"
      ) {
        setRemoteGrid({ cols: p.cols, rows: p.rows, revision: p.gridRevision });
      }
    }).then((off) => {
      // 订阅是异步建立的：卸载可能先于它完成，此时立刻注销，别留悬挂订阅。
      if (cancelled) off();
      else unlisten = off;
    });
    return () => {
      cancelled = true;
      unlisten?.();
    };
  }, []);

  /**
   * attach 拿到 `not_found`：把 store 里这条失效的内核 `tabId` 清掉，并切到失效态。
   *
   * # 为什么清 `tabId` 就不会再累积死标签
   *
   * `src/app/layout.ts` 的 `sanitizeTab` 对**没有 `tabId` 的终端标签一律丢弃**，
   * 所以这份布局再写到服务端时就不再包含这条死标签 —— 下次打开不会恢复它。
   * 这是**有意为之**（不是漏写），别把它改成"保留 tabId 以后再说"。
   */
  const handleAttachDead = useCallback(
    (code: string) => {
      if (code !== "not_found") return;
      // `dead` 是纯本地标记：它让 `layout.ts::applyToStore` 在套用远端布局时把这条
      // 标签并回来（服务端那份因 sanitizeTab 丢掉它），遮罩才能稳定存活、按钮可点。
      useUi.getState().updateTab(storeTabId, { tabId: undefined, dead: true });
      setKernelTabId(null);
      setAttachDead(true);
    },
    [storeTabId],
  );

  /**
   * 「重新连接这台主机」：按标签所在工作区记着的资产重新连一次，然后把**这个已经存在的
   * 标签**接到一个**新 shell** 上（旧 PTY 已经没了，只能新建）。
   *
   * 不做"静默什么都不做"：拿不到 assetId 时明确告诉用户去左侧资产树手连。
   */
  const reconnectNow = useCallback(async () => {
    const st = useUi.getState();
    const ws = st.workspaces.find((w) =>
      w.panes.some((p) => p.tabs.some((t) => t.id === storeTabId)),
    );
    const assetId = ws?.assetId;
    if (!assetId) {
      pushToast("error", "找不到这台主机的资产信息，请到左侧资产树里手动连接");
      return;
    }
    setReconnecting(true);
    try {
      const s = await sessionApi.connect(assetId);
      const list = useUi.getState().sessions;
      useUi.getState().setSessions([...list.filter((x) => x.id !== s.id), s]);
      // 新的会话 + 清空内核 tabId ⇒ 重挂后 XtermView 因 resumeTabId 为空走
      // `terminal_attach` 新建分支（这正是想要的），并把新 tabId 写回 store。
      useUi.getState().updateTab(storeTabId, { sessionId: s.id, tabId: undefined, dead: false });
      resumeRef.current = undefined;
      setControl(null);
      setRemoteGrid(null);
      setAttachDead(false);
      setKernelTabId(null);
      setEpoch((n) => n + 1);
      pushToast("info", "已重新连接，正在打开一个新的终端");
    } catch (e) {
      // 失败保留失效态、允许重试 —— 不要把按钮变成一次性的死按钮。
      pushToast("error", `重新连接失败：${describeError(e)}`);
    } finally {
      setReconnecting(false);
    }
  }, [storeTabId, pushToast]);

  const toggleRecord = async () => {
    if (!kernelTabId) return;
    if (!recording) {
      const name = `${title}-${Date.now()}.log`;
      try {
        // 必须在用户手势里问落点：服务端模式下这一步要向服务端预留暂存文件、
        // 并趁激活还在把浏览器的保存句柄拿到手（见 `ui/dialogs.ts::pickSavePath`）。
        const path = await pickSavePath(name);
        if (!path) return;
        await terminalApi.recordStart(kernelTabId, path);
        // 停止时要拿它去「交付给浏览器保存」，见下面那个分支。
        recordTarget.current = { path, name };
        setRecording(true);
        pushToast("info", `开始录制 → ${describeTarget(path, name)}`);
      } catch (e) {
        pushToast("error", `开始录制失败：${describeError(e)}`);
      }
    } else {
      const target = recordTarget.current;
      recordTarget.current = null;
      let bytes: number;
      try {
        bytes = await terminalApi.recordStop(kernelTabId);
      } catch (e) {
        // 停录失败也要把状态复位，否则按钮永远卡在「停止录制」。
        setRecording(false);
        pushToast("error", `停止录制失败：${describeError(e)}`);
        return;
      }
      setRecording(false);
      try {
        // 服务端模式下录的是**盒子上的暂存文件**，停录之后才把内容交给浏览器保存。
        const where = target ? await finishSave(target.path, target.name) : null;
        pushToast("success", `录制完成（${bytes} 字节）${where ? ` → ${where}` : ""}`);
      } catch (e) {
        // 录制已经成功停下了，这时候说「停止录制失败」是误导 —— 如实说是交付那一步挂了。
        pushToast("error", `录制已停止（${bytes} 字节），但保存到本地失败：${describeError(e)}`);
      }
    }
  };

  /* ── 右键菜单 ──────────────────────────────────────────────────────── */

  /** 切换字符编码（工具条的 select 和右键子菜单共用一份逻辑）。 */
  const applyEncoding = async (v: string) => {
    if (!kernelTabId) {
      pushToast("error", "终端尚未连接，无法切换编码");
      return;
    }
    try {
      await terminalApi.switchEncoding(kernelTabId, v);
      setEncoding(v);
      pushToast("info", `编码已切换为 ${v}（重连后生效更佳）`);
    } catch (e) {
      pushToast("error", `编码切换失败：${describeError(e)}`);
    }
  };

  /** 把剪贴板内容打进终端，等价于用户按了粘贴。 */
  const pasteIntoTerminal = async () => {
    if (!kernelTabId) return;
    try {
      const text = await navigator.clipboard.readText();
      if (text) await sendData(text);
    } catch (e) {
      pushToast("error", `读取剪贴板失败：${describeError(e)}`);
    }
  };

  const disconnectSession = async () => {
    try {
      await sessionApi.disconnect(sessionId);
      pushToast("info", "已断开连接");
    } catch (e) {
      pushToast("error", `断开失败：${describeError(e)}`);
    }
  };

  /** 断开之后不用再关掉整个工作区重开 —— 直接走内核的重连。 */
  const reconnectSession = async () => {
    try {
      // true 只代表"已开始重连"：退避重试在后台跑（最坏三分钟），结果通过
      // 会话状态事件推回来（重连中 → 已连接 / 连接失败），这里的提示别写死成"已连上"。
      const started = await sessionApi.reconnect(sessionId);
      pushToast(
        started ? "info" : "error",
        started ? "正在重连…结果会显示在终端状态上" : "这个会话不能重连：本机会话没有重连语义",
      );
    } catch (e) {
      pushToast("error", `重连失败：${describeError(e)}`);
    }
  };

  /** 把回滚输出写到本地文件（内核 dump → std::fs::write）。 */
  const saveLogToFile = async () => {
    if (!kernelTabId) return;
    const name = `${title}-${Date.now()}.log`.replace(/[\\/:*?"<>|]/g, "_");
    const path = await pickSavePath(name);
    if (!path) return;
    try {
      const bytes = await terminalApi.exportLog(kernelTabId, path);
      const where = await finishSave(path, name);
      pushToast(
        where ? "success" : "info",
        where ? `已保存 ${bytes} 字节 → ${where}` : "已取消保存",
      );
    } catch (e) {
      pushToast("error", `保存失败：${describeError(e)}`);
    }
  };

  /**
   * 「粘贴选中文本」：把终端里选中的那段写回终端的输入行。
   *
   * 和上面的「粘贴」差在**不碰剪贴板** —— 典型用法是在远端 `ls` 出几个路径、
   * 选中其中一个再喂给下一条命令，不希望这一来一回把剪贴板里的东西冲掉。
   */
  const pasteSelectionBack = async () => {
    if (!kernelTabId) return;
    const selected = handleRef.current?.getSelection() ?? "";
    if (!selected) return;
    try {
      await sendData(selected);
    } catch (e) {
      pushToast("error", `写入终端失败：${describeError(e)}`);
    }
  };

  /**
   * 「命令输入」：弹多行输入框，整条命令写进终端（自动补回车 = 直接执行）。
   *
   * 多行是刚需：一段 `for … do … done` 或者带续行符的 docker 命令，
   * 在单行框里根本没法写。所以显式 multiLine，不靠弹窗自己猜。
   */
  const inputCommand = async () => {
    if (!kernelTabId) return;
    const cmd = await promptText("要发送到终端的命令（可多行，回车换行）", "", {
      multiLine: true,
    });
    if (!cmd) return;
    const body = cmd.endsWith("\n") ? cmd : `${cmd}\n`;
    try {
      await sendData(body);
      pushToast("success", "命令已发送");
    } catch (e) {
      pushToast("error", `发送失败：${describeError(e)}`);
    }
  };

  /**
   * 终端右键菜单。
   *
   * 形态：分组小标 + 右侧快捷键 + 子菜单。
   * 无论有没有选区都弹 —— 菜单本身够厚，不怕空；只是"选区相关"的那几项
   * 在没选中时置灰而不隐藏，这样菜单结构是稳定的、可预期的。
   */
  const buildTerminalMenu = (): MenuItem[] => {
    const selected = (handleRef.current?.getSelection() ?? "").trim();
    const hasSel = selected.length > 0;
    const firstLine = selected.split("\n")[0];
    const st = useUi.getState();
    const ws = st.workspaces.find((w) =>
      w.panes.some((p) => p.tabs.some((t) => t.id === storeTabId)),
    );
    const isSplit = (ws?.panes.length ?? 1) > 1;

    return [
      { kind: "group", label: "操作" },
      {
        kind: "item",
        label: "复制",
        icon: <IconCopy size={13} />,
        disabled: !hasSel,
        onSelect: () => void navigator.clipboard.writeText(selected).catch(() => undefined),
      },
      {
        kind: "item",
        label: "粘贴",
        disabled: !kernelTabId,
        onSelect: () => void pasteIntoTerminal(),
      },
      {
        kind: "item",
        label: "粘贴选中文本",
        icon: <IconEdit size={13} />,
        hint: "不动剪贴板",
        disabled: !hasSel || !kernelTabId,
        onSelect: () => void pasteSelectionBack(),
      },
      {
        kind: "item",
        label: "搜索选中内容",
        icon: <IconSearch size={13} />,
        accel: "Ctrl+F",
        disabled: !hasSel,
        onSelect: () => {
          setQuery(firstLine);
          setSearchOpen(true);
          // 搜索栏挂载后才注册好 addon 句柄，等一帧再搜
          window.setTimeout(() => searchApi.current?.findNext(firstLine), 60);
        },
      },
      {
        kind: "item",
        label: "命令输入",
        icon: <IconCommand size={13} />,
        hint: "多行，直接执行",
        disabled: !kernelTabId,
        onSelect: () => void inputCommand(),
      },
      { kind: "separator" },
      { kind: "group", label: "终端" },
      {
        kind: "item",
        label: "清屏",
        disabled: !kernelTabId,
        onSelect: () => handleRef.current?.clear(),
      },
      {
        kind: "item",
        label: "保存为日志",
        icon: <IconSave size={13} />,
        hint: "当前回滚缓冲",
        disabled: !kernelTabId,
        onSelect: () => void saveLogToFile(),
      },
      {
        kind: "item",
        label: "重新连接",
        icon: <IconRefresh size={13} />,
        hint: canReconnect ? "已断开" : statusText,
        // 已经连着的时候重连只会白抖一下，没有意义
        disabled: !canReconnect,
        onSelect: () => void reconnectSession(),
      },
      {
        kind: "item",
        label: "断开连接",
        icon: <IconPlug size={13} />,
        danger: true,
        onSelect: () => void disconnectSession(),
      },
      { kind: "separator" },
      { kind: "group", label: "分屏" },
      {
        kind: "item",
        label: isSplit ? "取消分屏" : "上下分屏",
        icon: isSplit ? <IconMergeH size={13} /> : <IconSplitH size={13} />,
        accel: "Ctrl+\\",
        onSelect: () => {
          if (!ws) return;
          const cur = useUi.getState();
          if (isSplit) void cur.unsplitWorkspace(ws.panes[1].id, ws.id);
          else cur.splitWorkspace(ws.id);
        },
      },
    ];
  };

  const openTerminalMenu = (e: ReactMouseEvent<HTMLDivElement>) => {
    e.preventDefault();
    const selected = (handleRef.current?.getSelection() ?? "").trim();
    setMenu({
      x: e.clientX,
      y: e.clientY,
      // 有选区就把它顶在菜单头上，让用户确认自己要操作的是哪一段
      title: selected ? selected.split("\n")[0].slice(0, 80) : title,
      items: buildTerminalMenu(),
    });
  };

  return (
    <div className="nx-pane bg-term">
      <div className="nx-toolbar">
        <span className="nx-toolbar-title">{title}</span>
        <span
          className={`nx-badge ${
            sessionStatus === "connected"
              ? "nx-badge-green"
              : sessionStatus === "failed"
                ? "nx-badge-red"
                : ""
          }`}
        >
          <span className="nx-dot" />
          {statusText}
        </span>
        {effectiveWinrm && <span className="nx-badge nx-badge-amber">非交互模式</span>}
        {containerId && <span className="nx-badge nx-badge-purple">容器内 exec</span>}
        <div className="nx-spacer" />

        <button
          className={`nx-btn nx-btn-ghost nx-btn-sm ${searchOpen ? "bg-neutral-800 text-neutral-100" : ""}`}
          title="搜索终端内容 (Ctrl+F)"
          onClick={() => setSearchOpen((v) => !v)}
        >
          <IconSearch size={13} />
          搜索
        </button>

        <select
          className="nx-select nx-input-sm w-[88px] font-mono"
          value={encoding}
          title="终端字符编码（右键菜单里也有）"
          onChange={(e) => void applyEncoding(e.target.value)}
        >
          {ENCODINGS.map((enc) => (
            <option key={enc} value={enc}>
              {enc}
            </option>
          ))}
        </select>

        <button
          className={`nx-btn nx-btn-sm ${recording ? "nx-btn-danger" : "nx-btn-ghost"}`}
          title="把终端输出录制到文件"
          onClick={() => void toggleRecord()}
        >
          {recording ? <IconStop size={12} /> : <span className="nx-dot bg-current" />}
          {recording ? "停止" : "录制"}
        </button>

        {blocksSupported && (
          <>
            <span className="nx-divider-v" />
            <button
              className="nx-icon-btn nx-icon-btn-sm"
              disabled={!blocks.length}
              title="定位到上一条命令"
              onClick={() => handleRef.current?.navigateBlock("prev")}
            >
              <IconArrowUp size={13} />
            </button>
            <button
              className="nx-icon-btn nx-icon-btn-sm"
              disabled={!blocks.length}
              title="定位到下一条命令"
              onClick={() => handleRef.current?.navigateBlock("next")}
            >
              <IconArrowDown size={13} />
            </button>
            <button
              className={`nx-btn nx-btn-sm ${blocksOpen ? "nx-btn-ghost bg-neutral-800 text-neutral-100" : "nx-btn-ghost"}`}
              title="命令块：折叠输出 / 复制 / 定位"
              onClick={() => {
                const next = !blocksOpen;
                setBlocksOpen(next);
                if (!next) userClosedBlocks.current = true;
                else userClosedBlocks.current = false;
              }}
            >
              <IconList size={13} />
              命令块
              {blocks.length > 0 && <span className="nx-count">{blocks.length}</span>}
            </button>
          </>
        )}
      </div>

      {searchOpen && (
        <div className="flex shrink-0 items-center gap-1.5 border-b border-neutral-800/60 bg-neutral-900/70 px-2.5 py-1.5">
          <div className="nx-field max-w-[260px]">
            <span className="nx-field-icon">
              <IconSearch size={12} />
            </span>
            <input
              autoFocus
              className="nx-input nx-input-sm"
              placeholder="搜索终端内容…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && e.shiftKey) searchApi.current?.findPrevious(query);
                else if (e.key === "Enter") searchApi.current?.findNext(query);
                if (e.key === "Escape") setSearchOpen(false);
              }}
            />
          </div>
          <button
            className="nx-icon-btn nx-icon-btn-sm"
            title="上一个"
            onClick={() => searchApi.current?.findPrevious(query)}
          >
            <IconArrowUp size={13} />
          </button>
          <button
            className="nx-icon-btn nx-icon-btn-sm"
            title="下一个"
            onClick={() => searchApi.current?.findNext(query)}
          >
            <IconArrowDown size={13} />
          </button>
          <div className="nx-spacer" />
          <button className="nx-icon-btn nx-icon-btn-sm" title="关闭搜索" onClick={() => setSearchOpen(false)}>
            <IconClose size={12} />
          </button>
        </div>
      )}

      <div className="nx-terminal-body flex min-h-0 flex-1">
        {/* 终端内容区接右键；菜单本身渲染在组件末尾（fixed 定位，不占布局）。
            relative 是为了让观察者遮罩能绝对定位盖住 xterm。 */}
        <div className="relative min-h-0 min-w-0 flex-1" onContextMenu={openTerminalMenu}>
          <div className="h-full w-full p-1.5">
            <XtermView
              // ★ 重挂用的 key：从失效态点「重新连接」后靠它把 attach effect 重跑一遍
              // （resumeRef 只在挂载时取值，光改它不会重跑 —— 见上方的注释）。
              key={epoch}
              sessionId={sessionId}
              tabId={kernelTabId ?? "pending"}
              winrm={effectiveWinrm}
              containerId={containerId}
              // ★ 挂载时取一次（resumeRef），别跟着 prop 变 —— 见上方 resumeRef 的注释。
              resumeTabId={resumeRef.current}
              // 观察者不调 resize：会报 not_controller，而且会按自己的窗口尺寸
              // 把正在操作那端的 PTY 重排。
              canResize={canResize}
              remoteGrid={remoteGrid ?? undefined}
              visible={visible}
              // 用户按键统一走 sendData：它把 not_controller 当"别人正在操作"处理，
              // 而不是当错误弹框。
              onData={(d) => void sendData(d)}
              onAttachFailed={handleAttachDead}
              onAttachInfo={(info) =>
                setControl({
                  controller: info.controller,
                  subscribers: info.subscribers,
                  viewers: info.viewers,
                  exited: info.exited,
                })
              }
              onAttach={(id) => {
                setKernelTabId(id);
                // attach 成功一定要退出失效态（重连后重挂会走到这里）。
                setAttachDead(false);
                // 必须写回 store：store.closeTab 只有拿到内核 tabId 才会调
                // terminal_close_tab 回收 PTY，否则关标签会泄漏远端 shell。
                useUi.getState().updateTab(storeTabId, { tabId: id });
                // 顺便清掉本地失效标记（防「重连成功后仍显示失效遮罩」）。只在当前
                // store 里确实带 `dead` 时才发 —— 否则每次 attach 成功都会多一次
                // setState，进而排一次无谓的布局回写。
                if (isStoreTabDead(storeTabId)) {
                  useUi.getState().updateTab(storeTabId, { dead: false });
                }
                // 「在终端打开」要求终端一落地就 cd 过去。必须等到内核 tabId
                // 出来才能发 —— 这正是它挂在 pendingCommand 上、而不是开标签时
                // 就写出去的原因。取出即清空，避免 StrictMode 双跑发两次。
                const pending = takePendingCommand(storeTabId);
                if (pending) {
                  void terminalApi
                    .write(id, new TextEncoder().encode(`${pending}\n`))
                    .catch(() => undefined);
                }
                // attach 信息里的 controller/subscribers 是那一刻的快照；
                // 稍后别人可能已经接管。拉一次最新的，遮罩状态才准。
                void refreshControl(id);
              }}
              onBlocks={blocksSupported ? setBlocks : undefined}
              onHandle={(h) => {
                handleRef.current = h;
              }}
              registerSearch={(api) => {
                searchApi.current = api;
              }}
            />
          </div>

          {/* 观察者遮罩：终端可以被多台设备一起看，但同一时刻只有一个人能操作。
              进程已结束的终端也盖一层（只能看最后的内容，敲不了）。

              ★ 放行指针（pointer-events-none）：遮罩盖住的是画布，若连指针一起吃掉，
              观察者就**既不能滚动回看历史、也不能选中复制输出** —— 而"能一起看"的
              实用价值恰恰在于看中间那段日志。只把需要点击的「接管控制」按钮放回
              pointer-events-auto。键盘写入不靠遮罩拦：键盘本来就走不到 xterm 的
              textarea（点击落到画布上只是聚焦），即便走到了，内核也会拒（not_controller），
              `sendData` 里对观察者还有一道本地拦截。 */}
          {controlSupported && control && (control.exited || isObserver) && (
            <div className="pointer-events-none absolute inset-0 z-10 flex flex-col items-center justify-center gap-2.5 bg-neutral-950/70 px-6 text-center backdrop-blur-[1px]">
              <span className="flex items-center gap-1.5 text-[13px] font-semibold text-neutral-100">
                <IconEye size={15} className="text-amber-300" />
                {control.exited
                  ? "终端进程已结束"
                  : control.controller
                    ? "终端正在其他设备上操作中"
                    : "当前无人操作"}
              </span>
              {control.exited ? (
                <span className="max-w-[440px] text-[11.5px] leading-relaxed text-neutral-400">
                  进程已经退出，这里只能查看它最后的内容。要继续操作请新建一个终端。
                </span>
              ) : (
                <>
                  <span className="max-w-[440px] text-[11.5px] leading-relaxed text-neutral-400">
                    观看可以一起看，但同一时刻只有一个设备能操作。接管后，对方将转为只读观看，
                    终端尺寸也会按你的窗口重排。
                  </span>
                  {/* 徽章 = 设备口径（viewers）。**渲染条件仍用通道口径**：
                      同一台设备开两个页面看同一标签时 subscribers==2 而 viewers==1，
                      若条件写成 `viewers > 1`，这个真实场景下徽章根本不会出现。
                      条件是「除了我这条通道还有别的推送」= 有人在看；数字才是设备数。 */}
                  {control.subscribers > 1 && (
                    <span className="nx-badge">{control.viewers} 个设备正在观看</span>
                  )}
                  <button
                    className="nx-btn nx-btn-primary nx-btn-sm pointer-events-auto"
                    disabled={claiming}
                    onClick={() => void takeControl()}
                  >
                    {claiming ? (
                      <IconRefresh size={13} className="animate-spin" />
                    ) : (
                      <IconGamepad size={13} />
                    )}
                    接管控制
                  </button>
                </>
              )}
            </div>
          )}

          {/* 连接失效遮罩：attach 拿到 not_found（服务端重启 / 会话被回收）。
              这时标签是**敲不动**的（kernelTabId 为空，输入被吃掉），必须给两条
              明确的出路，而不是留一行黄字让人干瞪眼。
              这里刻意**吃指针**（与观察者遮罩相反）：底下没有活着的终端可交互。 */}
          {attachDead && (
            <div className="absolute inset-0 z-20 flex flex-col items-center justify-center gap-2.5 bg-neutral-950/80 px-6 text-center backdrop-blur-[1px]">
              <span className="flex items-center gap-1.5 text-[13px] font-semibold text-neutral-100">
                <IconRefresh size={15} className="text-amber-300" />
                这个终端已失效
              </span>
              <span className="max-w-[440px] text-[11.5px] leading-relaxed text-neutral-400">
                它所属的连接在服务端已经不在了（服务端重启，或连接已被回收）。
              </span>
              <div className="flex items-center gap-2">
                <button
                  className="nx-btn nx-btn-primary nx-btn-sm"
                  disabled={reconnecting}
                  onClick={() => void reconnectNow()}
                >
                  {reconnecting ? (
                    <IconRefresh size={13} className="animate-spin" />
                  ) : (
                    <IconRefresh size={13} />
                  )}
                  重新连接这台主机
                </button>
                <button
                  className="nx-btn nx-btn-ghost nx-btn-sm"
                  onClick={() => void useUi.getState().closeTab(storeTabId)}
                >
                  <IconClose size={13} />
                  关闭标签
                </button>
              </div>
            </div>
          )}
        </div>
        {blocksSupported && blocksOpen && (
          <CommandBlockPanel
            blocks={blocks}
            getText={(i) => handleRef.current?.copyBlock(i) ?? ""}
            onLocate={(i) => handleRef.current?.scrollToBlock(i)}
            onClear={() => handleRef.current?.clearBlocks()}
            onToast={pushToast}
          />
        )}
      </div>

      <TerminalKeysBar
        onSend={(data) => void sendData(data)}
        onFocus={() => handleRef.current?.focus()}
      />
      <ContextMenu state={menu} onClose={() => setMenu(null)} />
    </div>
  );
}
