import { formatInZone, localTimeZone, useI18n, type Trigger } from "@lerryn/core";

type CronDetail = NonNullable<Trigger["cron"]>;

export interface CronTextProps {
  cron: CronDetail;
  className?: string;
}

/**
 * A cron trigger's frequency as people read it, e.g. "每个工作日 08:00",
 * with the expression and time zone in the native tooltip. The text comes
 * from the server (schedule_text), so web and phone say the same thing.
 */
export function CronText({ cron, className }: CronTextProps) {
  return (
    <span className={className} title={`${cron.schedule} · ${cron.timezone}`}>
      {cron.schedule_text}
    </span>
  );
}

export interface CronDetailsProps {
  cron: CronDetail;
  /** The trigger name, shown in the heading. */
  name?: string;
  /** The task is active and the trigger enabled, so these fires will happen. */
  firing: boolean;
  /** How many upcoming fires to list; the server gives up to five. */
  count?: number;
}

/**
 * The details of a cron trigger: expression, the next fires in the trigger's
 * own time zone, and the time zone itself. When the task is inactive or the
 * trigger is off the fires are labelled as expected only (TriggerSet
 * contract: next_fires are still computed).
 */
export function CronDetails({ cron, name, firing, count = 3 }: CronDetailsProps) {
  const { t, locale } = useI18n();
  const fires = cron.next_fires.slice(0, count);
  const sameZone = cron.timezone === localTimeZone();
  return (
    <div className="stack" style={{ gap: 0 }}>
      <div className="pop-title">
        <span>{name ?? t("trigger.cron")}</span>
        <span className="mono-sm">{cron.schedule}</span>
      </div>
      <span className="body-sm" style={{ marginBottom: 6 }}>
        {cron.schedule_text}
      </span>
      {fires.map((at) => {
        const { date, time } = formatInZone(at, cron.timezone, locale);
        return (
          <div className="pop-row" key={at} style={firing ? undefined : { color: "var(--ink-muted)" }}>
            <span>{date}</span>
            <span className="mono">{time}</span>
          </div>
        );
      })}
      <span className="caption muted" style={{ marginTop: 8 }}>
        {cron.timezone}
        {sameZone ? null : ` · ${t("trigger.inZone")}`}
      </span>
      {!firing ? (
        <span className="caption muted" style={{ marginTop: 2 }}>
          {t("trigger.notFiring")}
        </span>
      ) : null}
    </div>
  );
}
