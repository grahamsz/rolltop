// File overview: Shared settings navigation, page chrome, index rows, and async states used by
// both core settings and runtime plugins.

import { useEffect, useRef } from "react";
import type { KeyboardEvent as ReactKeyboardEvent, ReactNode } from "react";
import type { Navigate } from "../../appTypes";
import { Icon } from "../../components/Icon";

export type SettingsSectionID = "general" | "mail" | "preferences" | "plugins";

type SettingsTab = {
  id: SettingsSectionID;
  label: string;
  path: string;
};

const settingsTabs: readonly SettingsTab[] = [
  { id: "general", label: "General", path: "/settings/account/general" },
  { id: "mail", label: "Mail", path: "/settings/account/mail" },
  { id: "preferences", label: "Preferences", path: "/settings/account/preferences" },
  { id: "plugins", label: "Plugins", path: "/settings/account/plugins" }
];

/** SettingsShell keeps the routed settings sections in one stable, keyboard-accessible frame. */
export function SettingsShell({
  activeSection,
  navigate,
  children
}: {
  activeSection: SettingsSectionID;
  navigate: Navigate;
  children: ReactNode;
}) {
  const tabListRef = useRef<HTMLElement | null>(null);
  const tabRefs = useRef<Partial<Record<SettingsSectionID, HTMLAnchorElement | null>>>({});

  useEffect(() => {
    const list = tabListRef.current;
    const tab = tabRefs.current[activeSection];
    if (!list || !tab) return;

    const listRect = list.getBoundingClientRect();
    const tabRect = tab.getBoundingClientRect();
    if (tabRect.left < listRect.left) {
      list.scrollTo({ left: list.scrollLeft + tabRect.left - listRect.left, behavior: "auto" });
    } else if (tabRect.right > listRect.right) {
      list.scrollTo({ left: list.scrollLeft + tabRect.right - listRect.right, behavior: "auto" });
    }
  }, [activeSection]);

  function activateTab(tab: SettingsTab, focus = false) {
    navigate(tab.path);
    if (focus) window.requestAnimationFrame(() => tabRefs.current[tab.id]?.focus());
  }

  function handleTabKeyDown(event: ReactKeyboardEvent<HTMLAnchorElement>, index: number) {
    let nextIndex = index;
    if (event.key === "ArrowRight") nextIndex = (index + 1) % settingsTabs.length;
    else if (event.key === "ArrowLeft") nextIndex = (index - 1 + settingsTabs.length) % settingsTabs.length;
    else if (event.key === "Home") nextIndex = 0;
    else if (event.key === "End") nextIndex = settingsTabs.length - 1;
    else return;

    event.preventDefault();
    activateTab(settingsTabs[nextIndex], true);
  }

  return (
    <section className="settings-shell">
      <nav
        ref={tabListRef}
        className="settings-tabs"
        aria-label="Settings sections"
        role="tablist"
      >
        {settingsTabs.map((tab, index) => {
          const active = tab.id === activeSection;
          return (
            <a
              ref={(element) => { tabRefs.current[tab.id] = element; }}
              id={`settings-tab-${tab.id}`}
              className={active ? "active" : ""}
              href={tab.path}
              role="tab"
              aria-selected={active}
              aria-current={active ? "page" : undefined}
              aria-controls="settings-tab-panel"
              tabIndex={active ? 0 : -1}
              key={tab.id}
              onClick={(event) => {
                if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
                event.preventDefault();
                activateTab(tab);
              }}
              onKeyDown={(event) => handleTabKeyDown(event, index)}
            >
              {tab.label}
            </a>
          );
        })}
      </nav>
      <div
        id="settings-tab-panel"
        className="settings-shell-content"
        role="tabpanel"
        aria-labelledby={`settings-tab-${activeSection}`}
      >
        {children}
      </div>
    </section>
  );
}

/** SettingsPage supplies consistent title, back navigation, actions, and content structure. */
export function SettingsPage({
  title,
  description,
  backPath,
  navigate,
  actions,
  children,
  className = ""
}: {
  title: ReactNode;
  description?: ReactNode;
  backPath?: string;
  navigate: Navigate;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  const classes = ["settings-page", className].filter(Boolean).join(" ");
  return (
    <section className={classes}>
      <header className="settings-page-header">
        <div className="settings-page-heading">
          {backPath ? (
            <button
              className="icon-button settings-page-back"
              type="button"
              title="Back"
              aria-label="Back"
              onClick={() => navigate(backPath)}
            >
              <Icon name="arrow_back" />
            </button>
          ) : null}
          <div>
            <h1 tabIndex={-1}>{title}</h1>
            {description ? <p>{description}</p> : null}
          </div>
        </div>
        {actions ? <div className="settings-page-actions">{actions}</div> : null}
      </header>
      <div className="settings-page-content">{children}</div>
    </section>
  );
}

/** SettingsLoading reserves a stable settings state while initial data is unavailable. */
export function SettingsLoading({ label = "Loading settings...", compact = false }: { label?: string; compact?: boolean }) {
  return (
    <div className={`settings-state settings-loading${compact ? " compact" : ""}`} role="status" aria-live="polite" aria-busy="true">
      <div className="settings-loading-label">
        <span className="spinner" aria-hidden="true" />
        <span>{label}</span>
      </div>
      {!compact ? (
        <div className="settings-loading-skeleton" aria-hidden="true">
          <span />
          <span />
          <span />
        </div>
      ) : null}
    </div>
  );
}

/** SettingsError keeps request failures visible and offers an in-place retry when available. */
export function SettingsError({ message, onRetry, compact = false }: { message: string; onRetry?: () => void; compact?: boolean }) {
  return (
    <div className={`settings-state settings-error${compact ? " compact" : ""}`} role="alert">
      <Icon name="report" />
      <div>
        <strong>Unable to load settings</strong>
        <p>{message}</p>
      </div>
      {onRetry ? (
        <button className="secondary" type="button" onClick={onRetry}>
          <Icon name="sync" />Retry
        </button>
      ) : null}
    </div>
  );
}

/** SettingsEmpty renders the same empty-state treatment for core and plugin pages. */
export function SettingsEmpty({
  icon,
  title,
  description,
  action
}: {
  icon: string;
  title: ReactNode;
  description: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="settings-state settings-empty">
      <Icon name={icon} />
      <div>
        <h2>{title}</h2>
        <p>{description}</p>
      </div>
      {action ? <div className="settings-empty-action">{action}</div> : null}
    </div>
  );
}

/** SettingsIndex groups navigation rows on a settings section index. */
export function SettingsIndex({ children, ariaLabel = "Settings" }: { children: ReactNode; ariaLabel?: string }) {
  return <div className="settings-index" role="list" aria-label={ariaLabel}>{children}</div>;
}

/** SettingsIndexRow is the common core/plugin entry point into an individual settings page. */
export function SettingsIndexRow({
  icon,
  title,
  description,
  meta,
  path,
  navigate,
  onNavigate
}: {
  icon: string;
  title: ReactNode;
  description: ReactNode;
  meta?: ReactNode;
  path: string;
  navigate: Navigate;
  onNavigate?: () => void;
}) {
  return (
    <div className="settings-index-item" role="listitem">
      <a
        className="settings-index-row"
        href={path}
        onClick={(event) => {
          if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
          event.preventDefault();
          onNavigate?.();
          navigate(path);
          window.requestAnimationFrame(() => {
            const heading = document.querySelector<HTMLElement>("#settings-tab-panel .settings-page h1");
            heading?.focus();
          });
        }}
      >
        <span className="settings-index-icon"><Icon name={icon} /></span>
        <span className="settings-index-copy">
          <strong>{title}</strong>
          <small>{description}</small>
        </span>
        {meta ? <span className="settings-index-meta">{meta}</span> : null}
        <span className="settings-index-chevron"><Icon name="chevron_right" /></span>
      </a>
    </div>
  );
}
