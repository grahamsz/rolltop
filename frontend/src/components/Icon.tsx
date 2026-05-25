// File overview: Central Phosphor icon adapter. App code keeps semantic icon names while this file
// maps them to concrete icon components, aliases, and default weights.

import {
  Archive,
  ArrowBendUpLeft,
  ArrowBendUpRight,
  ArrowLeft,
  ArrowsClockwise,
  Bell,
  CaretDown,
  CaretLeft,
  CaretRight,
  DotsThreeVertical,
  EnvelopeSimple,
  Folder,
  GearSix,
  Image,
  List,
  ListBullets,
  ListNumbers,
  MagnifyingGlass,
  Mailbox as MailboxIcon,
  Minus,
  NotePencil,
  Paperclip,
  PaperPlaneTilt,
  PencilSimple,
  Plus,
  Quotes,
  SealWarning,
  ShoppingBag,
  Tag,
  TextAa,
  Trash,
  Tray,
  Users,
  X
} from "@phosphor-icons/react";
import type { Icon as PhosphorIcon, IconWeight } from "@phosphor-icons/react";

// Keep this map semantic. Folder configuration and older UI call sites still use
// Material-ish names, while this adapter decides which Phosphor glyph to render.
const iconMap: Record<string, PhosphorIcon> = {
  add: Plus,
  archive: Archive,
  arrow_back: ArrowLeft,
  attach_file: Paperclip,
  chevron_left: CaretLeft,
  chevron_right: CaretRight,
  close: X,
  delete: Trash,
  draft: NotePencil,
  edit: PencilSimple,
  expand_more: CaretDown,
  folder: Folder,
  format_color_text: TextAa,
  format_list_bulleted: ListBullets,
  format_list_numbered: ListNumbers,
  format_quote: Quotes,
  forward: ArrowBendUpRight,
  group: Users,
  image: Image,
  inbox: Tray,
  label: Tag,
  menu: List,
  mail: EnvelopeSimple,
  mailbox: MailboxIcon,
  mailmirror: MailboxIcon,
  minimize: Minus,
  more_vert: DotsThreeVertical,
  notifications: Bell,
  report: SealWarning,
  reply: ArrowBendUpLeft,
  reply_all: Users,
  search: MagnifyingGlass,
  send: PaperPlaneTilt,
  settings: GearSix,
  shopping_bag: ShoppingBag,
  sync: ArrowsClockwise
};

const iconAliases: Record<string, string> = {
  drafts: "draft",
  sent: "send",
  spam: "report",
  trash: "delete"
};

const iconWeights: Partial<Record<string, IconWeight>> = {
  mailmirror: "duotone",
  report: "duotone",
  sync: "duotone"
};

/** Resolve a semantic MailMirror icon name to a Phosphor component and weight. */
export function Icon({ name, weight }: { name: string; weight?: IconWeight }) {
  const normalized = name.trim().toLowerCase().replaceAll("-", "_");
  const key = iconAliases[normalized] || normalized;
  const Component = iconMap[key] || Folder;
  return <Component className="icon" aria-hidden="true" focusable="false" weight={weight || iconWeights[key] || "regular"} />;
}
