import { useEffect } from "react";
import { terminalApi } from "../../ipc/commands";
import { disposeChannel } from "../../ipc/events";
import { IconClose } from "../../ui/icons";
import { DockerLogStream, type DockerBinarySink } from "./DockerLogStream";

export interface DockerOutputStream {
  id: string;
  sessionId: string;
  title: string;
  channel: ReturnType<typeof import("../../ipc/events").createBinaryChannel>;
  sink: DockerBinarySink;
}

export function DockerStreamView({ stream, onClose }: { stream: DockerOutputStream; onClose: () => void }) {
  useEffect(() => () => {
    void terminalApi.closeTab(stream.id, "kill", stream.sessionId).catch(() => undefined);
    disposeChannel(stream.channel);
  }, [stream]);

  return (
    <div className="flex h-[320px] min-h-0 flex-col border-t border-neutral-800/70 bg-term">
      <div className="nx-toolbar">
        <span className="nx-toolbar-title">{stream.title}</span>
        <span className="nx-badge">流式输出</span>
        <div className="nx-spacer" />
        <button type="button" className="nx-icon-btn nx-icon-btn-sm" title="停止并关闭输出" onClick={onClose}>
          <IconClose size={12} />
        </button>
      </div>
      <div className="min-h-0 flex-1">
        <DockerLogStream sink={stream.sink} ariaLabel={stream.title} />
      </div>
    </div>
  );
}
