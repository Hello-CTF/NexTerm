import type { TerminalLinkSource } from "../../features/terminal/terminalLinkRegistry";
import { cleanTerminalLink, displayTerminalLink } from "../../features/terminal/terminalLinks";
import { IconTerminal } from "../../ui/icons";
import type { JumpCommand } from "./useJumpCommands";

const MAX_LINK_COMMANDS = 50;
const HINT_LIMIT = 48;

// The panel command plus one command per URL of the active terminal, so a URL fragment typed
// into the jump palette finds it. Computed when the palette's commands are built (the palette is
// remounted on each open), not while it is closed.
export function terminalLinkCommands(source: TerminalLinkSource, bindingHint?: string): JumpCommand[] {
  const commands: JumpCommand[] = [
    {
      id: "terminal-links-panel",
      label: "终端链接面板",
      hint: bindingHint,
      icon: IconTerminal,
      run: () => source.openPanel(),
    },
  ];
  source.extract().slice(0, MAX_LINK_COMMANDS).forEach((link, index) => {
    const clean = cleanTerminalLink(link.context);
    const context = clean.length > HINT_LIMIT ? `${clean.slice(0, HINT_LIMIT)}…` : clean;
    commands.push({
      id: `terminal-link-${index}`,
      label: displayTerminalLink(link.url),
      hint: context || undefined,
      section: "当前终端链接",
      icon: IconTerminal,
      run: () => source.open(link.url),
    });
  });
  return commands;
}
