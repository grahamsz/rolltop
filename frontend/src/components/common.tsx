// File overview: Small reusable UI primitives shared by feature views, including labeled fields,
// summary stats, list headers, and toast rendering.

import type { ReactNode } from "react";
import type { Toast } from "../appTypes";
import { Icon } from "./Icon";

/** RangePagerProps describes pager state for mailbox and search result lists. */
export type RangePagerProps = {
  page: number;
  pageSize: number;
  itemCount: number;
  total?: number;
  hasPrev: boolean;
  hasNext: boolean;
  pageURL: (page: number) => string;
  navigate: (url: string) => void;
  ariaLabel: string;
  loading?: boolean;
};

/** ListHeader renders a view title with optional actions and right-aligned range paging. */
export function ListHeader({
  title,
  titleClassName = "",
  actions,
  pager
}: {
  title: ReactNode;
  titleClassName?: string;
  actions?: ReactNode;
  pager: RangePagerProps;
}) {
  return (
    <div className="content-head list-head">
      <div className="list-head-main">
        <h1 className={titleClassName}>{title}</h1>
        {actions}
      </div>
      <RangePager {...pager} />
    </div>
  );
}

/** RangePager renders compact previous/next controls plus the visible item range. */
export function RangePager({
  page,
  pageSize,
  itemCount,
  total,
  hasPrev,
  hasNext,
  pageURL,
  navigate,
  ariaLabel,
  loading = false
}: RangePagerProps) {
  const start = itemCount > 0 || hasNext ? (page - 1) * pageSize + 1 : 0;
  const end = itemCount > 0 ? (page - 1) * pageSize + itemCount : start > 0 ? page * pageSize : 0;
  const cappedEnd = total && total > 0 ? Math.min(end, total) : end;
  const label = start > 0
    ? `${start.toLocaleString()}-${cappedEnd.toLocaleString()}${total && total > 0 ? ` of ${total.toLocaleString()}` : hasNext ? " of many" : ""}`
    : total && total > 0 ? `0 of ${total.toLocaleString()}` : "0";

  return (
    <div className="range-pager" aria-label={loading ? ariaLabel + ", loading" : ariaLabel}>
      {loading ? <span className="range-pager-label range-pager-loading" aria-hidden="true" /> : <span className="range-pager-label">{label}</span>}
      <button className="range-pager-button" type="button" disabled={loading || !hasPrev} onClick={() => navigate(pageURL(page - 1))} title="Previous page">
        <Icon name="chevron_left" />
      </button>
      <button className="range-pager-button" type="button" disabled={loading || !hasNext} onClick={() => navigate(pageURL(page + 1))} title="Next page">
        <Icon name="chevron_right" />
      </button>
    </div>
  );
}

/** Field renders a labeled text input used by settings and contact forms. */
export function Field({
  label,
  value,
  onChange,
  type = "text",
  placeholder = "",
  disabled = false,
  required = false
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  type?: string;
  placeholder?: string;
  disabled?: boolean;
  required?: boolean;
}) {
  return (
    <div>
      <label>{label}</label>
      <input type={type} value={value} placeholder={placeholder} disabled={disabled} required={required} onChange={(event) => onChange(event.target.value)} />
    </div>
  );
}

/** Stat renders one storage/settings statistic with optional detail text. */
export function Stat({ label, value, detail }: { label: string; value: string; detail?: string }) {
  return (
    <div className="stat-card">
      <div className="stat-label">{label}</div>
      <div className="stat-value">{value}</div>
      {detail ? <div className="stat-detail">{detail}</div> : null}
    </div>
  );
}

/** ToastStack renders global toasts and exposes dismiss controls. */
export function ToastStack({ toasts, onDismiss }: { toasts: Toast[]; onDismiss: (id: number) => void }) {
  return (
    <div className="toast-stack">
      {toasts.map((toast) => (
        <div className={`toast ${toast.kind === "error" ? "error" : ""}`} key={toast.id}>
          {toast.kind === "loading" ? <span className="spinner" aria-hidden="true" /> : null}
          <span className="toast-message" role={toast.kind === "error" ? "alert" : "status"}>{toast.message}</span>
          {toast.action ? (
            <button className="toast-action" type="button" onClick={toast.action.onClick}>
              {toast.action.label}
            </button>
          ) : null}
          <button className="toast-dismiss" type="button" title="Dismiss" aria-label="Dismiss notification" onClick={() => onDismiss(toast.id)}>
            <Icon name="close" />
          </button>
        </div>
      ))}
    </div>
  );
}
