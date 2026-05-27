// File overview: Central Phosphor icon adapter. App code keeps semantic icon names while this file
// maps them to concrete icon components, aliases, and default weights.

import {
  Archive,
  ArrowBendUpLeft,
  ArrowBendUpRight,
  ArrowLeft,
  ArrowsClockwise,
  AirplaneTilt,
  Bank,
  Bell,
  BookmarkSimple,
  Briefcase,
  Buildings,
  CalendarBlank,
  Camera,
  CaretDown,
  CaretLeft,
  CaretRight,
  ChartBar,
  Clock,
  CreditCard,
  DotsThreeVertical,
  EnvelopeSimple,
  FileText,
  Flame,
  Folder,
  GearSix,
  GraduationCap,
  Heart,
  House,
  Image,
  List,
  ListBullets,
  ListNumbers,
  LinkSimple,
  MagnifyingGlass,
  Mailbox as MailboxIcon,
  Minus,
  Newspaper,
  NotePencil,
  Paperclip,
  PaperPlaneTilt,
  PencilSimple,
  Plus,
  Quotes,
  Receipt,
  SealWarning,
  ShoppingBag,
  Star,
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
  bank: Bank,
  bookmark: BookmarkSimple,
  briefcase: Briefcase,
  building: Buildings,
  calendar: CalendarBlank,
  arrow_back: ArrowLeft,
  attach_file: Paperclip,
  camera: Camera,
  chart: ChartBar,
  chevron_left: CaretLeft,
  chevron_right: CaretRight,
  clock: Clock,
  close: X,
  credit_card: CreditCard,
  delete: Trash,
  draft: NotePencil,
  edit: PencilSimple,
  expand_more: CaretDown,
  file_text: FileText,
  flame: Flame,
  folder: Folder,
  format_color_text: TextAa,
  format_list_bulleted: ListBullets,
  format_list_numbered: ListNumbers,
  format_quote: Quotes,
  forward: ArrowBendUpRight,
  group: Users,
  heart: Heart,
  home: House,
  image: Image,
  inbox: Tray,
  label: Tag,
  link: LinkSimple,
  menu: List,
  mail: EnvelopeSimple,
  mailbox: MailboxIcon,
  mailmirror: MailboxIcon,
  minimize: Minus,
  more_vert: DotsThreeVertical,
  newspaper: Newspaper,
  notifications: Bell,
  receipt: Receipt,
  report: SealWarning,
  reply: ArrowBendUpLeft,
  reply_all: Users,
  search: MagnifyingGlass,
  send: PaperPlaneTilt,
  settings: GearSix,
  school: GraduationCap,
  shopping_bag: ShoppingBag,
  star: Star,
  sync: ArrowsClockwise,
  travel: AirplaneTilt
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


export function LogoMark({ className = "brand-logo" }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 66.066406 69.11528" aria-hidden="true" focusable="false">
      <g transform="translate(509.99438 175.90039)">
        <path fill="#f7faf8" d="m-446.67797-106.79728-.008-32.18705v-.002l-.006-3.89258c0-16.75746-13.51402-30.27148-30.27148-30.27148h.00056c-16.75746 0-30.27149 13.51402-30.27149 30.27148l-.006 3.89258v.002l-.004 32.18705" />
        <path fill="none" stroke="#c46b44" strokeWidth="5.5" strokeLinecap="round" d="m-446.67797-109.53511-.008-29.44922v-.002l-.006-3.89258c0-16.75746-13.51402-30.27148-30.27148-30.27148h.00056c-16.75746 0-30.27149 13.51402-30.27149 30.27148l-.006 3.89258v.002l-.004 29.44922" />
        <path fill="#151f2e" d="m-454.95974-139.81893-15.33653 11.85302c-3.75097 2.89903-9.93295 2.89799-13.68392-.001l-14.98513-11.58172-.00052.56844-.004 32.19493h44.0154l-.004-32.19286z" />
        <path fill="#c46b44" d="m-476.96253-164.87683c-11.12016.00024-20.09174 7.892-21.73045 18.48931l19.27324 14.89573c1.30333 1.00731 3.25815 1.00731 4.56148 0l19.58588-15.13706c-1.73637-10.47584-10.65408-18.24774-21.68963-18.24798z" />
      </g>
    </svg>
  );
}

/** Resolve a semantic MailMirror icon name to a Phosphor component and weight. */
export function Icon({ name, weight }: { name: string; weight?: IconWeight }) {
  const normalized = name.trim().toLowerCase().replaceAll("-", "_");
  const key = iconAliases[normalized] || normalized;
  const Component = iconMap[key] || Folder;
  return <Component className="icon" aria-hidden="true" focusable="false" weight={weight || iconWeights[key] || "regular"} />;
}
