import { useI18n } from "@lerryn/core";
import { Button, ErrorState } from "@lerryn/ui-web";
import { Link, useLocation } from "@tanstack/react-router";
import { ChevronDown } from "lucide-react";
import { useEffect, useId, useRef, useState, type ComponentType, type ReactNode } from "react";

/** The middle column: a titled list of items or sections. */
export function ListColumn({
  title,
  count,
  label,
  tools,
  inlineTools,
  children,
}: {
  title: string;
  count?: ReactNode;
  label: string;
  tools?: ReactNode;
  /** Put the tools right after the title instead of at the far end. */
  inlineTools?: boolean;
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
    <section className="list" aria-label={label} data-expanded={expanded}>
      <div className={inlineTools ? "list-head list-head--inline" : "list-head"}>
        <h2 className="list-title">
          <span className="list-heading">{heading}</span>
          <button
            ref={toggle}
            type="button"
            className="list-toggle"
            aria-expanded={expanded}
            aria-controls={bodyId}
            onClick={() => setExpanded((value) => !value)}
          >
            {heading}
            <ChevronDown className="wl-icon" aria-hidden="true" />
          </button>
        </h2>
        {tools ? <div className="list-tools">{tools}</div> : null}
      </div>
      <div
        id={bodyId}
        className="list-body"
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

/** A section link in a list column, e.g. a settings page. */
export function SectionLink({
  to,
  icon: Icon,
  label,
  meta,
  metaBad,
}: {
  to: string;
  icon: ComponentType<{ className?: string }>;
  label: string;
  meta?: ReactNode;
  metaBad?: boolean;
}) {
  return (
    <Link to={to} className="li" activeProps={{ "aria-current": "page" }}>
      <span className="li-ic">
        <Icon className="wl-icon" />
      </span>
      <span className="li-name">{label}</span>
      <span className={metaBad ? "li-meta bad" : "li-meta"}>{meta}</span>
    </Link>
  );
}

/** The board: the page's main area. */
export function Board({ children, center, wide, label }: { children: ReactNode; center?: boolean; wide?: boolean; label?: string }) {
  const className = ["board", center && "board--center", wide && "board--wide"].filter(Boolean).join(" ");
  return (
    <main className={className} aria-label={label}>
      {children}
    </main>
  );
}

/** A board heading with optional actions on the right. */
export function BoardTop({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="board-top">
      <h1 className="h2">{title}</h1>
      {children}
    </div>
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
    <div className="row" style={{ justifyContent: "center" }}>
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
