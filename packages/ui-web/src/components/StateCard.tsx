import { CircleExclamation, CloudSlash, Magnifier } from "@gravity-ui/icons";
import { Card } from "@heroui/react";
import type { ReactNode } from "react";

import { useI18n } from "@lerryn/core";

import type { IconType } from "./IconButton";

export interface StateCardProps {
  icon: IconType;
  title: string;
  /** A short line under the title, such as a time or a link back. */
  detail?: ReactNode;
  /** Centre in the available space, for whole-board states. */
  center?: boolean;
  role?: "status" | "alert";
}

/** A quiet card for empty, missing and unavailable states. */
export function StateCard({ icon: Icon, title, detail, center, role = "status" }: StateCardProps) {
  return (
    <Card
      role={role}
      variant={center ? "transparent" : "default"}
      className={center ? "items-center justify-center gap-4 px-5 py-12 text-center" : "flex-row items-center gap-4"}
    >
      <span className="g g--idle g--lg" aria-hidden="true">
        <Icon width={18} height={18} aria-hidden="true" />
      </span>
      <span className="flex min-w-0 flex-col gap-0.5">
        <span className="text-sm font-medium">{title}</span>
        {detail ? <span className="text-sm text-muted">{detail}</span> : null}
      </span>
    </Card>
  );
}

/** For data whose API is not available on this server yet. */
export function UnavailableState({ center }: { center?: boolean }) {
  const { t } = useI18n();
  return <StateCard icon={CloudSlash} title={t("states.unavailableTitle")} center={center} />;
}

/** For a resource that does not exist or that the viewer may not see. */
export function NotFoundState({ action }: { action?: ReactNode }) {
  const { t } = useI18n();
  return <StateCard icon={Magnifier} title={t("states.notFoundTitle")} detail={action} />;
}

/** For a request that failed; `action` is usually a retry button. */
export function ErrorState({ action, requestId }: { action?: ReactNode; requestId?: string }) {
  const { t } = useI18n();
  return (
    <StateCard
      icon={CircleExclamation}
      role="alert"
      title={t("states.errorTitle")}
      detail={
        action || requestId ? (
          <span className="flex flex-wrap items-center gap-2">
            {action}
            {requestId ? <span className="font-mono text-xs">{requestId}</span> : null}
          </span>
        ) : undefined
      }
    />
  );
}
