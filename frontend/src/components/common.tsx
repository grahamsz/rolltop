import type { ReactNode } from "react";
import type { Toast } from "../appTypes";
import { Icon } from "./Icon";

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
};

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

export function RangePager({
  page,
  pageSize,
  itemCount,
  total,
  hasPrev,
  hasNext,
  pageURL,
  navigate,
  ariaLabel
}: RangePagerProps) {
  const start = itemCount > 0 || hasNext ? (page - 1) * pageSize + 1 : 0;
  const end = itemCount > 0 ? (page - 1) * pageSize + itemCount : start > 0 ? page * pageSize : 0;
  const cappedEnd = total && total > 0 ? Math.min(end, total) : end;
  const label = start > 0
    ? `${start.toLocaleString()}-${cappedEnd.toLocaleString()}${total && total > 0 ? ` of ${total.toLocaleString()}` : hasNext ? " of many" : ""}`
    : total && total > 0 ? `0 of ${total.toLocaleString()}` : "0";

  return (
    <div className="range-pager" aria-label={ariaLabel}>
      <span>{label}</span>
      <button className="range-pager-button" type="button" disabled={!hasPrev} onClick={() => navigate(pageURL(page - 1))} title="Previous page">
        <Icon name="chevron_left" />
      </button>
      <button className="range-pager-button" type="button" disabled={!hasNext} onClick={() => navigate(pageURL(page + 1))} title="Next page">
        <Icon name="chevron_right" />
      </button>
    </div>
  );
}

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

export function Stat({ label, value, detail }: { label: string; value: string; detail?: string }) {
  return (
    <div className="stat-card">
      <div className="stat-label">{label}</div>
      <div className="stat-value">{value}</div>
      {detail ? <div className="stat-detail">{detail}</div> : null}
    </div>
  );
}

export function ToastStack({ toasts, onDismiss }: { toasts: Toast[]; onDismiss: (id: number) => void }) {
  return (
    <div className="toast-stack">
      {toasts.map((toast) => (
        <button className={`toast ${toast.kind === "error" ? "error" : ""}`} key={toast.id} onClick={() => onDismiss(toast.id)}>
          {toast.kind === "loading" ? <span className="spinner" /> : null}
          <span>{toast.message}</span>
        </button>
      ))}
    </div>
  );
}
