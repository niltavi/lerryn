import { Disclosure } from "@heroui/react";
import { useId, type ReactNode } from "react";

export interface CollapsiblePanelProps {
  title: string;
  icon?: ReactNode;
  aside?: ReactNode;
  children: ReactNode;
  className?: string;
}

/** A console panel with an independently collapsible body and a persistent header. */
export function CollapsiblePanel({ title, icon, aside, children, className }: CollapsiblePanelProps) {
  const asideId = useId();
  return (
    <section className={["wl-card", className].filter(Boolean).join(" ")} aria-label={title}>
      <Disclosure defaultExpanded>
        <Disclosure.Heading level={2}>
          <Disclosure.Trigger
            className="panel-disclosure-trigger"
            aria-label={title}
            aria-describedby={aside ? asideId : undefined}
          >
            <span className="panel-title">{icon}{title}</span>
            {aside ? <span id={asideId} className="panel-disclosure-aside">{aside}</span> : null}
            <Disclosure.Indicator className="panel-disclosure-indicator" aria-hidden="true" />
          </Disclosure.Trigger>
        </Disclosure.Heading>
        <Disclosure.Content>
          <div className="panel-disclosure-body">{children}</div>
        </Disclosure.Content>
      </Disclosure>
    </section>
  );
}
