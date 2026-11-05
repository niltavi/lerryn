import { CircleAlert, CloudOff, SearchX, type LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

import { useI18n } from "@lerryn/core";

export interface StateCardProps {
  icon: LucideIcon;
  title: string;
  /** A short line under the title, such as a time or a link back. */
  detail?: ReactNode;
  /** Centre in the available space, for whole-board states. */
  center?: boolean;
  role?: "status" | "alert";
}

/** A quiet card for empty, missing and unavailable states (prototype: 空与缺失). */
export function StateCard({ icon: Icon, title, detail, center, role = "status" }: StateCardProps) {
  return (
    <div className={["wl-card state", center && "state--center"].filter(Boolean).join(" ")} role={role}>
      <span className="g g--idle g--lg" aria-hidden="true">
        <Icon className="wl-icon" />
      </span>
      <span className="state-text">
        <span className="h4">{title}</span>
        {detail ? <span className="body-sm muted">{detail}</span> : null}
      </span>
    </div>
  );
}

/** For data whose API is not available on this server yet. */
export function UnavailableState({ center }: { center?: boolean }) {
  const { t } = useI18n();
  return <StateCard icon={CloudOff} title={t("states.unavailableTitle")} center={center} />;
}

/** For a resource that does not exist or that the viewer may not see. */
export function NotFoundState({ action }: { action?: ReactNode }) {
  const { t } = useI18n();
  return <StateCard icon={SearchX} title={t("states.notFoundTitle")} detail={action} />;
}

/** For a request that failed; `action` is usually a retry button. */
export function ErrorState({ action, requestId }: { action?: ReactNode; requestId?: string }) {
  const { t } = useI18n();
  return (
    <StateCard
      icon={CircleAlert}
      role="alert"
      title={t("states.errorTitle")}
      detail={
        action || requestId ? (
          <span className="row">
            {action}
            {requestId ? <span className="mono-sm">{requestId}</span> : null}
          </span>
        ) : undefined
      }
    />
  );
}
