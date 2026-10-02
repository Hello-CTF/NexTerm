import { describe, expect, it } from "vitest";
import * as ts from "typescript";
import commandsSource from "../ipc/commands.ts?raw";
import appSource from "../app/App.tsx?raw";

function propertyName(node: ts.PropertyName | undefined): string | undefined {
  return node && (ts.isIdentifier(node) || ts.isStringLiteral(node)) ? node.text : undefined;
}

function commandCalls() {
  const source = ts.createSourceFile(
    "commands.ts",
    commandsSource,
    ts.ScriptTarget.Latest,
    true,
    ts.ScriptKind.TS,
  );
  const calls: { command: string; nested: boolean }[] = [];
  const visit = (node: ts.Node): void => {
    if (
      ts.isCallExpression(node) &&
      ts.isIdentifier(node.expression) &&
      node.expression.text === "call" &&
      node.arguments[0] &&
      ts.isStringLiteral(node.arguments[0])
    ) {
      const args = node.arguments[1];
      const nested =
        args !== undefined &&
        ts.isObjectLiteralExpression(args) &&
        args.properties.some(
          (property) =>
            (ts.isPropertyAssignment(property) || ts.isShorthandPropertyAssignment(property)) &&
            propertyName(property.name) === "args",
        ) &&
        args.properties.every((property) => {
          const name = propertyName(property.name);
          return name === "args" || name === "channel";
        });
      calls.push({ command: node.arguments[0].text, nested });
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return calls;
}

describe("IPC facade 静态契约", () => {
  it("保留 139 个方法和 138 个唯一命令", () => {
    const names = commandCalls().map((call) => call.command);
    expect(names).toHaveLength(139);
    expect(new Set(names)).toHaveProperty("size", 138);
    expect(names.filter((name, index) => names.indexOf(name) !== index)).toEqual([
      "ai_presets",
    ]);
  });

  it("保留全部 21 个嵌套 args 命令", () => {
    const nested = commandCalls()
      .filter((call) => call.nested)
      .map((call) => call.command);
    expect(nested).toEqual([
      "session_connect",
      "session_probe",
      "terminal_write",
      "layout_put",
      "asset_create",
      "asset_update",
      "audit_query",
      "fs_write",
      "mount_create",
      "docker_exec_attach",
      "docker_action",
      "db_connect",
      "ai_chat",
      "ai_takeover_enter",
      "vault_set_credential",
      "credential_update",
      "sync_export",
      "sync_import",
      "sync_link_set",
      "sync_push",
      "sync_pull",
    ]);
  });

  it("拖动区域内的每个按钮都有 no-drag，双击只匹配目标自身", () => {
    expect(appSource).not.toContain("@tauri-apps");
    expect(appSource).not.toContain("data-tauri-drag-region");
    expect(appSource).not.toContain('closest("[data-wails-drag-region]")');
    expect(appSource).toContain("isWailsDragRegionTarget(event.target)");
    expect(appSource).toContain("onDoubleClick={onDragRegionDoubleClick}");

    const source = ts.createSourceFile(
      "App.tsx",
      appSource,
      ts.ScriptTarget.Latest,
      true,
      ts.ScriptKind.TSX,
    );
    type OpeningElement = ts.JsxOpeningElement | ts.JsxSelfClosingElement;
    const hasAttribute = (node: OpeningElement, name: string) =>
      node.attributes.properties.some(
        (property) =>
          ts.isJsxAttribute(property) &&
          ts.isIdentifier(property.name) &&
          property.name.text === name,
      );
    const hasNoDragStyle = (node: OpeningElement) =>
      node.attributes.properties.some(
        (property) =>
          ts.isJsxAttribute(property) &&
          ts.isIdentifier(property.name) &&
          property.name.text === "style" &&
          property.initializer !== undefined &&
          ts.isJsxExpression(property.initializer) &&
          property.initializer.expression !== undefined &&
          ts.isIdentifier(property.initializer.expression) &&
          property.initializer.expression.text === "wailsNoDragRegionStyle",
      );
    const missingNoDrag: number[] = [];
    let buttons = 0;
    const checkButton = (node: OpeningElement, insideDragRegion: boolean) => {
      if (!ts.isIdentifier(node.tagName) || node.tagName.text !== "button" || !insideDragRegion) {
        return;
      }
      buttons += 1;
      if (!hasNoDragStyle(node)) {
        missingNoDrag.push(source.getLineAndCharacterOfPosition(node.getStart(source)).line + 1);
      }
    };
    const visit = (node: ts.Node, insideDragRegion: boolean): void => {
      if (ts.isJsxElement(node)) {
        const current =
          insideDragRegion || hasAttribute(node.openingElement, "data-wails-drag-region");
        checkButton(node.openingElement, current);
        for (const child of node.children) visit(child, current);
        return;
      }
      if (ts.isJsxSelfClosingElement(node)) {
        const current = insideDragRegion || hasAttribute(node, "data-wails-drag-region");
        checkButton(node, current);
        return;
      }
      ts.forEachChild(node, (child) => visit(child, insideDragRegion));
    };
    visit(source, false);

    expect(buttons).toBe(8);
    expect(missingNoDrag).toEqual([]);
  });
});
