import { useI18n } from "@lerryn/core";
import { Button, ErrorState } from "@lerryn/ui-web";
import { ChevronDown } from "@gravity-ui/icons";
import { useLocation } from "@tanstack/react-router";
import { useEffect, useId, useRef, useState, type ReactNode } from "react";

const cx = (...parts: (string | false | undefined | null)[]) => parts.filter(Boolean).join(" ");

/**
 * A list pane beside the page: a titled, scrolling list of things to open.
 * On narrow consoles it folds to its heading, which then toggles the list.
 */
export function ListColumn({
  title,
  count,
  label,
  tools,
  children,
}: {
  title: string;
  count?: ReactNode;
  label: string;
  tools?: ReactNode;
  children: ReactNode;
}) {
  const [expanded, setExpanded] = useState(false);
  const bodyId = useId();
  const toggle = useRef<HTMLButtonElement>(null);
  const pathname = useLocation({ select: (location) => location.pathname });
  const heading = (
    <>
      {title}
      {count !== undefined && count !== null ? <span className="n">{count}</span> : null}
    </>
  );

  useEffect(() => setExpanded(false), [pathname]);

  return (
    <section className="tpl-list lr-list" aria-label={label} data-expanded={expanded}>
      <div className="tpl-list__head">
        <h2 className="lr-list__title">
          <span className="lr-list__heading">{heading}</span>
          <button
            ref={toggle}
            type="button"
            className="lr-list__toggle"
            aria-expanded={expanded}
            aria-controls={bodyId}
            onClick={() => setExpanded((value) => !value)}
          >
            {heading}
            <ChevronDown aria-hidden="true" width={14} height={14} />
          </button>
        </h2>
        {tools ? <div className="lr-list__tools">{tools}</div> : null}
      </div>
      <div
        id={bodyId}
        className="tpl-list__items"
        onClick={(event) => {
          // Selecting even the current link should return the compact view to its board.
          if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
          if (!(event.target instanceof Element) || !event.target.closest("a[href]")) return;
          setExpanded(false);
          if (toggle.current?.getClientRects().length) toggle.current.focus({ preventScroll: true });
        }}
      >
        {children}
      </div>
    </section>
  );
}

/** The page's main area; `center` is for whole-page states. */
export function Board({ children, center, label }: { children: ReactNode; center?: boolean; label?: string }) {
  return (
    <main className="lr-main" aria-label={label}>
      <div className={cx("nt-page", center && "nt-page--center")}>{children}</div>
    </main>
  );
}

/** The page heading with an optional description and the page's actions. */
export function BoardTop({ title, description, children }: { title: ReactNode; description?: ReactNode; children?: ReactNode }) {
  return (
    <div className="nt-page__head">
      <div>
        <h1>{title}</h1>
        {description ? <p>{description}</p> : null}
      </div>
      {children ? <div className="nt-page__actions">{children}</div> : null}
    </div>
  );
}

/** A titled group of content inside a page. */
export function Section({
  title,
  label,
  actions,
  children,
  className,
}: {
  title?: string;
  /** Accessible name when there is no visible title. */
  label?: string;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={cx("lr-section", className)} aria-label={label ?? title}>
      {title || actions ? (
        <div className="lr-section__head">
          {title ? <h2 className="lr-section__label">{title}</h2> : <span />}
          {actions}
        </div>
      ) : null}
      {children}
    </section>
  );
}

/** A failed read with a retry button and the request ID to quote. */
export function LoadError({ error, onRetry }: { error?: unknown; onRetry: () => void }) {
  const { t } = useI18n();
  const requestId = (error as { requestId?: string } | null)?.requestId;
  return (
    <ErrorState
      requestId={requestId}
      action={
        <Button size="sm" variant="secondary" onPress={onRetry}>
          {t("common.retry")}
        </Button>
      }
    />
  );
}

/** "Show more" under a paged list. */
export function LoadMore({ show, pending, onPress }: { show: boolean; pending: boolean; onPress: () => void }) {
  const { t } = useI18n();
  if (!show) return null;
  return (
    <div className="flex justify-center">
      <Button variant="ghost" size="sm" onPress={onPress} isPending={pending}>
        {t("common.showMore")}
      </Button>
    </div>
  );
}

/** Sets document.title for the page; the product name follows. */
export function usePageTitle(title?: string) {
  const { t } = useI18n();
  const full = title ? `${title} · ${t("app.name")}` : t("app.name");
  useEffect(() => {
    document.title = full;
  }, [full]);
}
