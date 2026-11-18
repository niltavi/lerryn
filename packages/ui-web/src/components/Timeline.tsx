import type { ComponentProps, ReactNode } from "react";

export type TimelineProps = ComponentProps<"ol">;

export type TimelineItemProps = ComponentProps<"li"> & {
  /** Decorative content in the marker, such as a step number or an icon. */
  marker?: ReactNode;
};

export function Timeline({ className, ...props }: TimelineProps) {
  return <ol role="list" className={["timeline", className].filter(Boolean).join(" ")} {...props} />;
}

Timeline.Item = function TimelineItem({ marker, children, className, ...props }: TimelineItemProps) {
  return (
    <li className={["timeline__item", className].filter(Boolean).join(" ")} {...props}>
      <span className="timeline__rail" aria-hidden="true">
        <span className="timeline__marker">{marker}</span>
      </span>
      <div className="timeline__content">{children}</div>
    </li>
  );
};
