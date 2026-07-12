// File overview: Reusable snooze picker with preset and custom reminder times.

import { useMemo, useState } from "react";
import { createPortal } from "react-dom";
import { Icon } from "../../components/Icon";

export type SnoozePreset = {
  label: string;
  detail: string;
  until: Date;
};

function atLocalTime(base: Date, dayOffset: number, hour: number) {
  const value = new Date(base);
  value.setDate(value.getDate() + dayOffset);
  value.setHours(hour, 0, 0, 0);
  return value;
}

export function defaultSnoozeUntil(now = new Date()) {
  return atLocalTime(now, 1, 9);
}

export function snoozePresets(now = new Date()): SnoozePreset[] {
  const laterToday = now.getHours() < 17 ? atLocalTime(now, 0, 18) : defaultSnoozeUntil(now);
  const tomorrow = defaultSnoozeUntil(now);
  const daysUntilMonday = ((8 - now.getDay()) % 7) || 7;
  const nextWeek = atLocalTime(now, daysUntilMonday, 9);
  const time = new Intl.DateTimeFormat(undefined, { weekday: "short", hour: "numeric", minute: "2-digit" });
  return [
    { label: now.getHours() < 17 ? "Later today" : "Tomorrow morning", detail: time.format(laterToday), until: laterToday },
    { label: "Tomorrow", detail: time.format(tomorrow), until: tomorrow },
    { label: "Next week", detail: time.format(nextWeek), until: nextWeek }
  ].filter((choice, index, choices) => choice.until.getTime() > now.getTime() && choices.findIndex((item) => item.until.getTime() === choice.until.getTime()) === index);
}

function localDateTimeInputValue(value: Date) {
  const local = new Date(value.getTime() - value.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
}

export function SnoozeControl({
  onSnooze,
  disabled = false,
  label = "Snooze",
  className = ""
}: {
  onSnooze: (until: Date) => void | Promise<void>;
  disabled?: boolean;
  label?: string;
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const presets = useMemo(() => snoozePresets(), [open]);
  const [custom, setCustom] = useState(() => localDateTimeInputValue(defaultSnoozeUntil()));

  async function choose(until: Date) {
    if (busy || !until || until.getTime() <= Date.now()) return;
    setBusy(true);
    try {
      await onSnooze(until);
      setOpen(false);
  } catch {
    // The caller owns user-facing error reporting; keep the picker open for retry.
    } finally {
      setBusy(false);
    }
  }

  const dialog = open && typeof document !== "undefined" ? createPortal(
    <div className="snooze-backdrop" role="presentation" onClick={() => !busy && setOpen(false)}>
      <section className="snooze-dialog" role="dialog" aria-modal="true" aria-labelledby="snooze-dialog-title" onClick={(event) => event.stopPropagation()}>
        <header>
          <h2 id="snooze-dialog-title">Snooze until</h2>
          <button className="ghost" type="button" disabled={busy} onClick={() => setOpen(false)} title="Close snooze picker" aria-label="Close snooze picker">
            <Icon name="close" />
          </button>
        </header>
        <div className="snooze-presets">
          {presets.map((preset) => (
            <button type="button" disabled={busy} key={`${preset.label}:${preset.until.toISOString()}`} onClick={() => void choose(preset.until)}>
              <span>{preset.label}</span>
              <small>{preset.detail}</small>
            </button>
          ))}
        </div>
        <div className="snooze-custom">
          <input
            type="datetime-local"
            value={custom}
            min={localDateTimeInputValue(new Date(Date.now() + 60_000))}
            onChange={(event) => setCustom(event.target.value)}
            aria-label="Custom snooze date and time"
          />
          <button type="button" disabled={busy || !custom || new Date(custom).getTime() <= Date.now()} onClick={() => void choose(new Date(custom))}>
            <Icon name="clock" />
            Set
          </button>
        </div>
      </section>
    </div>,
    document.body
  ) : null;

  return (
    <>
      <button className={className} type="button" disabled={disabled || busy} onClick={() => setOpen(true)} title="Snooze">
        <Icon name="clock" />
        <span>{label}</span>
      </button>
      {dialog}
    </>
  );
}
