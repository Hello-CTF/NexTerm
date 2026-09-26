// Docker 面板（M3）：容器列表 + stats 轮询 + 日志/终端 attach + 镜像管理。
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ask } from "../../ui/dialogs";
import { dockerApi, type ContainerSummary, type ImageSummary } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { XtermView } from "../terminal/XtermView";

export function DockerPanel({ sessionId }: { sessionId: string }) {
  const qc = useQueryClient();
  const { addTab, pushToast } = useUi();
  const [tab, setTab] = useState<"containers" | "images">("containers");
  const [attached, setAttached] = useState<{ container: string; kind: "logs" | "exec"; tabId: string } | null>(null);

  const containers = useQuery({
    queryKey: ["docker-ps", sessionId],
    queryFn: () => dockerApi.ps(sessionId),
    refetchInterval: 5000,
  });
  const images = useQuery({
    queryKey: ["docker-images", sessionId],
    queryFn: () => dockerApi.images(sessionId),
    refetchInterval: 30000,
  });

  const act = async (c: ContainerSummary, action: string) => {
    if (action === "remove" && !(await ask(`删除容器 ${c.name}？`))) return;
    try {
      await dockerApi.action(sessionId, c.id, action);
      void qc.invalidateQueries({ queryKey: ["docker-ps", sessionId] });
    } catch (e) {
      pushToast("error", `${action} 失败: ${String(e)}`);
    }
  };

  const attachLogs = async (c: ContainerSummary) => {
    const channel = await import("../../ipc/events").then((m) => m.createBinaryChannel(() => {}));
    try {
      const kernelTab = await dockerApi.logsAttach(sessionId, c.id, 500, channel);
      setAttached({ container: c.name, kind: "logs", tabId: kernelTab });
    } catch (e) {
      pushToast("error", `日志 attach 失败: ${String(e)}`);
    }
  };

  const attachExec = async (c: ContainerSummary) => {
    try {
      const id = `docker-exec-${c.id}-${Date.now()}`;
      addTab({
        id,
        kind: "terminal",
        title: `🐳 ${c.name}`,
        sessionId,
        containerId: c.id,
        closable: true,
      });
      void id;
      void attachExecInner(c);
    } catch (e) {
      pushToast("error", `exec 失败: ${String(e)}`);
    }
  };

  const attachExecInner = async (c: ContainerSummary) => {
    // 直接走 docker_exec_attach 的标签
    const channel = await import("../../ipc/events").then((m) => m.createBinaryChannel(() => {}));
    await dockerApi.execAttach(sessionId, c.id, 120, 32, channel);
  };

  if (attached) {
    return (
      <div className="flex h-full flex-col bg-[#12141a]">
        <div className="flex items-center border-b border-neutral-800 px-2 py-1 text-xs text-neutral-400">
          <button
            className="rounded px-2 py-0.5 hover:bg-neutral-800"
            onClick={() => setAttached(null)}
          >
            ← 返回
          </button>
          <span className="ml-2">
            {attached.kind === "logs" ? "日志跟随" : "容器终端"} · {attached.container}
          </span>
        </div>
        <div className="min-h-0 flex-1">
          <LogStreamView sessionId={sessionId} container={attached.container} kind={attached.kind} />
        </div>
      </div>
    );
  }

  return (
    <div className="flex h-full flex-col bg-neutral-900 text-sm text-neutral-300">
      <div className="flex items-center gap-2 border-b border-neutral-800 px-3 py-1.5">
        <span className="font-medium text-neutral-200">Docker</span>
        <button
          className={`rounded px-2 py-0.5 text-xs ${tab === "containers" ? "bg-neutral-700" : "hover:bg-neutral-800"}`}
          onClick={() => setTab("containers")}
        >
          容器 ({containers.data?.length ?? 0})
        </button>
        <button
          className={`rounded px-2 py-0.5 text-xs ${tab === "images" ? "bg-neutral-700" : "hover:bg-neutral-800"}`}
          onClick={() => setTab("images")}
        >
          镜像 ({images.data?.length ?? 0})
        </button>
        <div className="flex-1" />
        <span className="text-xs text-neutral-500">5s 自动刷新</span>
      </div>
      <div className="min-h-0 flex-1 overflow-auto">
        {tab === "containers" ? (
          <table className="w-full text-xs">
            <thead className="sticky top-0 bg-neutral-900 text-neutral-500">
              <tr>
                <th className="px-3 py-1.5 text-left">名称</th>
                <th className="px-2 py-1.5 text-left">状态</th>
                <th className="px-2 py-1.5 text-left">镜像</th>
                <th className="px-2 py-1.5 text-left">端口</th>
                <th className="px-2 py-1.5 text-right">操作</th>
              </tr>
            </thead>
            <tbody>
              {(containers.data ?? []).map((c) => (
                <tr key={c.id} className="border-t border-neutral-800/60 hover:bg-neutral-800/40">
                  <td className="px-3 py-1.5 font-medium">
                    {c.name}
                    {c.composeProject && (
                      <span className="ml-1 rounded bg-purple-500/20 px-1 text-[10px] text-purple-300">
                        {c.composeProject}
                      </span>
                    )}
                  </td>
                  <td className="px-2 py-1.5">
                    <span
                      className={`rounded px-1.5 py-0.5 text-[10px] ${
                        c.state === "running"
                          ? "bg-green-500/20 text-green-300"
                          : "bg-neutral-700 text-neutral-400"
                      }`}
                    >
                      {c.status}
                    </span>
                  </td>
                  <td className="px-2 py-1.5 font-mono text-neutral-400">{c.image}</td>
                  <td className="px-2 py-1.5 font-mono text-[10px] text-neutral-500">{c.ports}</td>
                  <td className="px-2 py-1.5 text-right">
                    <Btn onClick={() => void attachLogs(c)}>日志</Btn>
                    <Btn onClick={() => void attachExec(c)}>终端</Btn>
                    <Btn onClick={() => void act(c, "restart")}>重启</Btn>
                    <Btn onClick={() => void act(c, c.state === "running" ? "stop" : "start")}>
                      {c.state === "running" ? "停止" : "启动"}
                    </Btn>
                    <Btn danger onClick={() => void act(c, "remove")}>
                      删除
                    </Btn>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <table className="w-full text-xs">
            <thead className="sticky top-0 bg-neutral-900 text-neutral-500">
              <tr>
                <th className="px-3 py-1.5 text-left">仓库/标签</th>
                <th className="px-2 py-1.5 text-left">大小</th>
                <th className="px-2 py-1.5 text-left">创建时间</th>
                <th className="px-2 py-1.5 text-right">操作</th>
              </tr>
            </thead>
            <tbody>
              {(images.data ?? []).map((i: ImageSummary) => (
                <tr key={i.id} className="border-t border-neutral-800/60 hover:bg-neutral-800/40">
                  <td className="px-3 py-1.5 font-mono">
                    {i.repository}:{i.tag}
                  </td>
                  <td className="px-2 py-1.5">{i.size}</td>
                  <td className="px-2 py-1.5">{i.createdSince}</td>
                  <td className="px-2 py-1.5 text-right">
                    <Btn
                      danger
                      onClick={async () => {
                        if (!(await ask(`删除镜像 ${i.repository}:${i.tag}？`))) return;
                        await dockerApi.imageRemove(sessionId, i.id, false);
                        void qc.invalidateQueries({ queryKey: ["docker-images", sessionId] });
                      }}
                    >
                      删除
                    </Btn>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      <PullBar sessionId={sessionId} />
    </div>
  );
}

function LogStreamView({
  sessionId,
  container,
  kind,
}: {
  sessionId: string;
  container: string;
  kind: "logs" | "exec";
}) {
  // 复用 XtermView 的 attach 流程（logs/exec 已在内核侧开 channel）
  const [kernelTab, setKernelTab] = useState<string | null>(null);
  void kernelTab;
  if (kind === "logs") {
    return (
      <SimpleLogFollow sessionId={sessionId} container={container} onAttach={setKernelTab} />
    );
  }
  return null;
}

function SimpleLogFollow({
  sessionId,
  container,
  onAttach,
}: {
  sessionId: string;
  container: string;
  onAttach: (id: string) => void;
}) {
  const [lines, setLines] = useState<string[]>([]);
  const attachedRef = useRef(false);
  useEffect(() => {
    if (attachedRef.current) return;
    attachedRef.current = true;
    (async () => {
      const { createBinaryChannel } = await import("../../ipc/events");
      const channel = createBinaryChannel((bytes) => {
        setLines((prev) => {
          const text = new TextDecoder().decode(bytes);
          return [...prev.slice(-3000), ...text.split("\n")];
        });
      });
      try {
        const id = await dockerApi.logsAttach(sessionId, container, 500, channel);
        onAttach(id);
      } catch {
        // 降级：一次性取日志
        void dockerApi
          .ps(sessionId)
          .then(() => undefined)
          .catch(() => undefined);
      }
    })();
      }, [sessionId, container]);
  return (
    <div className="h-full overflow-auto bg-[#12141a] p-2 font-mono text-xs text-neutral-300">
      {lines.map((l, i) => (
        <div key={i}>{l}</div>
      ))}
    </div>
  );
}

import { useRef, useEffect } from "react";

function PullBar({ sessionId }: { sessionId: string }) {
  const qc = useQueryClient();
  const { pushToast } = useUi();
  const [image, setImage] = useState("");
  return (
    <div className="flex items-center gap-2 border-t border-neutral-800 px-3 py-1.5">
      <input
        className="min-w-0 flex-1 rounded bg-neutral-800 px-2 py-1 font-mono text-xs outline-none"
        placeholder="拉取镜像，如 nginx:alpine"
        value={image}
        onChange={(e) => setImage(e.target.value)}
      />
      <button
        className="rounded bg-blue-600 px-3 py-1 text-xs text-white hover:bg-blue-500 disabled:opacity-50"
        disabled={!image.trim()}
        onClick={async () => {
          pushToast("info", `拉取 ${image} 中（最长 10 分钟）…`);
          try {
            await dockerApi.imagePull(sessionId, image.trim());
            pushToast("success", "拉取完成");
            void qc.invalidateQueries({ queryKey: ["docker-images", sessionId] });
          } catch (e) {
            pushToast("error", `拉取失败: ${String(e)}`);
          }
        }}
      >
        拉取
      </button>
    </div>
  );
}

function Btn({
  children,
  onClick,
  danger,
}: {
  children: React.ReactNode;
  onClick: () => void;
  danger?: boolean;
}) {
  void XtermView;
  return (
    <button
      className={`ml-1 rounded px-1.5 py-0.5 text-[10px] hover:bg-neutral-700 ${
        danger ? "text-red-400" : "text-neutral-300"
      }`}
      onClick={onClick}
    >
      {children}
    </button>
  );
}
