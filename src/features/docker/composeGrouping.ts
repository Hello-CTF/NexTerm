import type { ComposeStack, ContainerSummary } from "../../ipc/commands";

export interface ComposeContainerGroup {
  project: string | null;
  stack: ComposeStack | null;
  containers: ContainerSummary[];
}

export function groupContainersByCompose(
  containers: ContainerSummary[],
  stacks: ComposeStack[],
): ComposeContainerGroup[] {
  const byProject = new Map<string, ContainerSummary[]>();
  const ungrouped: ContainerSummary[] = [];
  for (const container of containers) {
    const project = container.composeProject?.trim();
    if (!project) {
      ungrouped.push(container);
      continue;
    }
    const group = byProject.get(project) ?? [];
    group.push(container);
    byProject.set(project, group);
  }
  const metadata = new Map(stacks.map((stack) => [stack.project, stack]));
  const groups: ComposeContainerGroup[] = [...byProject.entries()]
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([project, items]) => ({
      project,
      stack: metadata.get(project) ?? null,
      containers: items,
    }));
  if (ungrouped.length > 0) groups.push({ project: null, stack: null, containers: ungrouped });
  return groups;
}
