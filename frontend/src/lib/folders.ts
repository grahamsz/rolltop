import type { Mailbox } from "../types";

export type FolderNode = {
  mailbox: Mailbox;
  label: string;
  children: FolderNode[];
};

export function folderTree(mailboxes: Mailbox[]): FolderNode[] {
  const visible = mailboxes.filter((mailbox) => mailbox.show_in_sidebar !== false);
  const byName = new Map(visible.map((mailbox) => [mailbox.name, mailbox]));
  const nodes = new Map<number, FolderNode>();
  for (const mailbox of visible) {
    nodes.set(mailbox.id, { mailbox, label: folderLabel(mailbox.name), children: [] });
  }
  const roots: FolderNode[] = [];
  for (const mailbox of visible) {
    const node = nodes.get(mailbox.id);
    if (!node) continue;
    const parent = closestVisibleParent(mailbox.name, byName);
    if (!parent) {
      roots.push(node);
      continue;
    }
    const parentNode = nodes.get(parent.id);
    if (!parentNode) {
      roots.push(node);
      continue;
    }
    node.label = folderLabel(mailbox.name, parent.name);
    parentNode.children.push(node);
  }
  const sortNodes = (items: FolderNode[]) => {
    items.sort((a, b) => folderSortKey(a.mailbox).localeCompare(folderSortKey(b.mailbox), undefined, { numeric: true, sensitivity: "base" }));
    for (const item of items) sortNodes(item.children);
    return items;
  };
  return sortNodes(roots);
}

function closestVisibleParent(name: string, byName: Map<string, Mailbox>): Mailbox | null {
  for (const parent of folderParentNames(name)) {
    const mailbox = byName.get(parent);
    if (mailbox) return mailbox;
  }
  return null;
}

export function folderParentNames(name: string): string[] {
  const out: string[] = [];
  for (let i = name.length - 1; i > 0; i--) {
    if (name[i] === "." || name[i] === "/" || name[i] === "\\") out.push(name.slice(0, i));
  }
  return out;
}

export function folderLabel(name: string, parent = ""): string {
  if (!parent) return name;
  const next = name.slice(parent.length);
  return next.replace(/^[./\\]+/, "") || name;
}

export function folderSortKey(mailbox: Mailbox): string {
  if (mailbox.role === "inbox" || mailbox.name.toLowerCase() === "inbox") return "00";
  if (mailbox.role === "trash") return `90:${mailbox.name}`;
  return `10:${mailbox.name}`;
}

export function nodeContainsMailbox(node: FolderNode, id: string | null): boolean {
  if (!id) return false;
  return String(node.mailbox.id) === id || node.children.some((child) => nodeContainsMailbox(child, id));
}
