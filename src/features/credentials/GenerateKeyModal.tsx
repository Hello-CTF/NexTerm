import { useId, useRef, useState } from "react";
import { vaultApi } from "../../ipc/commands";
import type { GeneratedKeyDto } from "../../ipc/types";
import { describeError } from "../../ui/errorText";
import { useUi } from "../../app/store";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { IconCopy, IconKey } from "../../ui/icons";

type Algorithm = "ed25519" | "rsa";

const ALGORITHMS: { key: Algorithm; label: string; hint: string }[] = [
  { key: "ed25519", label: "Ed25519", hint: "推荐：更快更安全，OpenSSH 6.5+ 可用" },
  { key: "rsa", label: "RSA 4096", hint: "兼容老设备；生成更慢" },
];

export function GenerateKeyModal({
  onClose,
  onSaved,
}: {
  onClose: () => void;
  onSaved: (id: string) => void;
}) {
  const { pushToast } = useUi();
  const [name, setName] = useState("");
  const [algorithm, setAlgorithm] = useState<Algorithm>("ed25519");
  const [passphrase, setPassphrase] = useState("");
  const [generating, setGenerating] = useState(false);
  const [generated, setGenerated] = useState<GeneratedKeyDto | null>(null);

  const modalRef = useRef<HTMLDivElement>(null);
  const nameInputRef = useRef<HTMLInputElement>(null);
  const titleId = useId();
  const nameId = useId();
  const passphraseId = useId();
  const layer = useOverlayFocus(true, modalRef, {
    initialFocus: () => nameInputRef.current,
  });

  const ready = name.trim().length > 0 && !generating;

  const generate = async () => {
    setGenerating(true);
    try {
      const result = await vaultApi.generateKey(
        name.trim(),
        algorithm,
        passphrase ? passphrase : undefined,
      );
      setGenerated(result);
      onSaved(result.id);
    } catch (e) {
      pushToast("error", describeError(e));
    } finally {
      setGenerating(false);
    }
  };

  const copy = async (value: string, label: string) => {
    try {
      await navigator.clipboard.writeText(value);
      pushToast("success", label);
    } catch (e) {
      pushToast("error", `复制失败：${describeError(e)}`);
    }
  };

  return (
    <div className="nx-overlay" onClick={onClose}>
      <div
        ref={modalRef}
        className="nx-modal flex max-w-[480px] flex-col"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          e.stopPropagation();
          if (!layer.isTopmost()) return;
          if (e.key === "Escape" && !e.repeat && !isImeKeyEvent(e)) {
            e.preventDefault();
            onClose();
            return;
          }
          trapOverlayTab(e, modalRef.current);
        }}
      >
        <div className="nx-modal-header shrink-0">
          <span id={titleId} className="text-[13px] font-semibold text-neutral-100">
            生成密钥对
          </span>
        </div>
        <div className="nx-modal-body min-h-0 flex-1 overflow-y-auto">
          {generated === null ? (
            <>
              <div className="nx-form-row">
                <label className="nx-label" htmlFor={nameId}>名称</label>
                <input
                  id={nameId}
                  ref={nameInputRef}
                  className="nx-input"
                  value={name}
                  placeholder="例如：github-deploy"
                  onChange={(e) => setName(e.target.value)}
                />
                <span className="nx-hint mt-1.5 block">
                  私钥加密存入凭据库，不会写入任何明文文件。
                </span>
              </div>

              <div className="nx-form-row">
                <span className="nx-label">算法</span>
                <div className="grid grid-cols-2 gap-1.5">
                  {ALGORITHMS.map((item) => {
                    const on = item.key === algorithm;
                    return (
                      <button
                        key={item.key}
                        type="button"
                        aria-pressed={on}
                        className={`flex items-center gap-2 rounded-md border px-2.5 py-2 text-left text-[12px] ${
                          on
                            ? "border-blue-500/50 bg-blue-500/10 text-neutral-100"
                            : "border-neutral-800/80 text-neutral-400 hover:border-neutral-700 hover:text-neutral-200"
                        }`}
                        onClick={() => setAlgorithm(item.key)}
                      >
                        <span
                          className={`hidden min-[400px]:flex h-6 w-6 shrink-0 items-center justify-center rounded bg-purple-500/12 text-purple-400`}
                        >
                          <IconKey size={13} />
                        </span>
                        <span className="truncate">{item.label}</span>
                      </button>
                    );
                  })}
                </div>
                <span className="nx-hint mt-1.5 block">
                  {ALGORITHMS.find((item) => item.key === algorithm)?.hint}
                </span>
              </div>

              <div className="nx-form-row">
                <label className="nx-label" htmlFor={passphraseId}>私钥口令</label>
                <input
                  id={passphraseId}
                  type="password"
                  className="nx-input font-mono"
                  value={passphrase}
                  autoComplete="off"
                  onChange={(e) => setPassphrase(e.target.value)}
                  placeholder="没有就留空"
                />
                <div className="nx-hint mt-1.5">
                  ↳ 选填。给私钥加一道口令，连接时自动使用。
                </div>
              </div>
            </>
          ) : (
            <div role="status">
              <div className="nx-form-row">
                <span className="nx-label">SHA256 指纹</span>
                <div className="flex items-start gap-2">
                  <code className="min-w-0 flex-1 break-all rounded-md border border-neutral-800/70 bg-neutral-900/60 px-2.5 py-1.5 font-mono text-[11.5px] text-neutral-300">
                    {generated.fingerprint}
                  </code>
                  <button
                    className="nx-btn nx-btn-sm shrink-0"
                    onClick={() => void copy(generated.fingerprint, "已复制指纹")}
                  >
                    <IconCopy size={12} />
                    复制
                  </button>
                </div>
              </div>
              <div className="nx-form-row">
                <span className="nx-label">authorized_keys 公钥行</span>
                <div className="flex items-start gap-2">
                  <pre className="nx-mono max-h-[140px] min-w-0 flex-1 overflow-auto whitespace-pre-wrap break-all rounded-md border border-purple-500/25 bg-neutral-900/60 px-2.5 py-1.5 text-[11.5px] leading-[1.5] text-neutral-300">
                    {generated.publicKey}
                  </pre>
                  <button
                    className="nx-btn nx-btn-sm shrink-0"
                    onClick={() => void copy(generated.publicKey, "已复制公钥行")}
                  >
                    <IconCopy size={12} />
                    复制
                  </button>
                </div>
                <div className="nx-hint mt-1.5">
                  ↳ 把它追加到目标主机的 ~/.ssh/authorized_keys。私钥已加密入库，可在凭据详情里查看。
                </div>
              </div>
            </div>
          )}
        </div>
        <div className="nx-modal-footer shrink-0">
          <button className="nx-btn nx-btn-ghost" onClick={onClose}>
            {generated === null ? "取消" : "关闭"}
          </button>
          {generated === null && (
            <button
              className="nx-btn nx-btn-primary"
              disabled={!ready}
              onClick={() => void generate()}
            >
              {generating ? "生成中…" : "生成并入库"}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}
