import { ask } from "../../ui/dialogs";
import { assetApi, type Asset } from "../../ipc/commands";

export async function cloneAsset(a: Asset): Promise<Asset | null> {
  const ok = await ask(
    `克隆「${a.name}」？\n\n会新建一条独立资产「${a.name} 副本」：共享原资产的凭据引用与私钥路径，不复制凭据内容。`,
    { title: "克隆资产", kind: "info" },
  );
  if (!ok) return null;
  return assetApi.create({
    kind: a.kind,
    name: `${a.name} 副本`,
    groupId: a.groupId,
    host: a.host,
    port: a.port,
    username: a.username,
    authKind: a.authKind,
    keyPath: a.keyPath,
    credId: a.credId,
    options: a.options,
    tags: a.tags,
    note: a.note,
  });
}
