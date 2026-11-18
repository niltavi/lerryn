import { Card, Disclosure } from "@heroui/react";
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
    <section className={className} aria-label={title}>
      <Card className="gap-0 p-0">
        <Disclosure defaultExpanded>
          {/* The aside stays outside the button so the button's name is exactly its visible title. */}
          <div className="lr-panel__head">
            <Disclosure.Heading level={2} className="min-w-0 flex-1">
              <Disclosure.Trigger className="lr-panel__trigger" aria-describedby={aside ? asideId : undefined}>
                <span className="lr-panel__title">{icon}{title}</span>
                <Disclosure.Indicator className="ms-0 text-muted" aria-hidden="true" />
              </Disclosure.Trigger>
            </Disclosure.Heading>
            {aside ? <span id={asideId} className="lr-panel__aside">{aside}</span> : null}
          </div>
          <Disclosure.Content>
            <Disclosure.Body className="flex flex-col gap-4 px-5 pt-0 pb-5">{children}</Disclosure.Body>
          </Disclosure.Content>
        </Disclosure>
      </Card>
    </section>
  );
}
