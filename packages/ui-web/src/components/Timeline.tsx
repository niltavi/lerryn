import type { ComponentProps, ReactNode } from "react";

export type TimelineProps = ComponentProps<"ol">;

export type TimelineItemProps = ComponentProps<"li"> & {
  /** Decorative content in the marker, such as a step number or an icon. */
  marker?: ReactNode;
};

export function Timeline({ className, ...props }: TimelineProps) {
  return <ol role="list" className={["wl-timeline", className].filter(Boolean).join(" ")} {...props} />;
}

Timeline.Item = function TimelineItem({ marker, children, className, ...props }: TimelineItemProps) {
  return (
    <li className={["wl-timeline-item", className].filter(Boolean).join(" ")} {...props}>
      <span className="wl-timeline-rail" aria-hidden="true">
        <span className="wl-timeline-marker">{marker}</span>
      </span>
      <div className="wl-timeline-content">{children}</div>
    </li>
  );
};
