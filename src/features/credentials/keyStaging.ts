import { useCallback, useEffect, useRef, useState } from "react";
import { pickKeyFile } from "../../ui/dialogs";
import { browserFilesAvailable, pickBrowserFile } from "../../ipc/webFiles";

export interface InlineKeySelection {
  path: string;
  content: string | null;
}

export async function pickInlineKeyFile(): Promise<InlineKeySelection | null> {
  if (browserFilesAvailable()) {
    const picked = await pickBrowserFile();
    if (!picked) return null;
    if (picked.size > 64 * 1024) throw new Error("文件超过 64KB，不是私钥");
    return { path: picked.name, content: await picked.text() };
  }
  const path = await pickKeyFile();
  return path ? { path, content: null } : null;
}

export function useInlineKeyPicker() {
  const generationRef = useRef(0);
  const [pending, setPending] = useState(false);

  useEffect(() => {
    return () => {
      generationRef.current += 1;
    };
  }, []);

  const invalidate = useCallback(() => {
    generationRef.current += 1;
    setPending(false);
  }, []);

  const pick = useCallback(async (): Promise<InlineKeySelection | null> => {
    const generation = ++generationRef.current;
    setPending(true);
    try {
      const selection = await pickInlineKeyFile();
      return generation === generationRef.current ? selection : null;
    } catch (error) {
      if (generation !== generationRef.current) return null;
      throw error;
    } finally {
      if (generation === generationRef.current) setPending(false);
    }
  }, []);

  return { pick, pending, invalidate };
}

export async function resolveInlineKeyContent(
  selection: InlineKeySelection,
  readKeyFile: (path: string) => Promise<string>,
): Promise<string> {
  return selection.content ?? readKeyFile(selection.path);
}
