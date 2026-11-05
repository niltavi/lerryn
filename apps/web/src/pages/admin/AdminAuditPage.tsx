import { formatDateTime, useAuditEvents, useI18n, type AuditEvent } from "@lerryn/core";
import { Disclosure, StateCard } from "@lerryn/ui-web";
import { Ban, Bot, Check, Cog, KeyRound, ScrollText, Search, Server, UserRound, Webhook, X } from "lucide-react";
import { useState } from "react";

import { Board, BoardTop, LoadError, LoadMore, usePageTitle } from "../../shell/layout";

type ResultFilter = "all" | "success" | "failure" | "denied";
const FILTERS: ResultFilter[] = ["all", "failure", "denied"];

const ACTOR_ICON = { user: UserRound, token: KeyRound, runner: Server, trigger: Webhook, anonymous: Bot, system: Cog } as const;

/** The audit log, newest first, filterable by result and action. No record holds a credential value. */
export function AdminAuditPage() {
  const { t } = useI18n();
  usePageTitle(t("admin.audit"));
  const [result, setResult] = useState<ResultFilter>("all");
  const [action, setAction] = useState("");
  const events = useAuditEvents({ result: result === "all" ? undefined : result, action });
  const items = events.data?.items ?? [];

  return (
    <Board label={t("admin.audit")}>
      <BoardTop title={t("admin.audit")}>
        <div className="tools" style={{ gap: 8, flexWrap: "wrap" }}>
          <label className="log-find" style={{ height: 36 }}>
            <Search className="wl-icon" aria-hidden="true" />
            <input type="search" value={action} onChange={(e) => setAction(e.target.value)} aria-label={t("admin.auditAction")} placeholder={t("admin.auditAction")} style={{ width: 140, fontSize: 13 }} />
          </label>
          <div className="seg" role="group" aria-label={t("admin.auditResult")}>
            {FILTERS.map((f) => (
              <button key={f} type="button" aria-pressed={result === f} onClick={() => setResult(f)}>
                {t(`admin.result.${f}`)}
              </button>
            ))}
          </div>
        </div>
      </BoardTop>
      {events.isError ? (
        <LoadError error={events.error} onRetry={() => void events.refetch()} />
      ) : events.data && items.length === 0 ? (
        <StateCard icon={ScrollText} title={t("admin.noAudit")} />
      ) : items.length > 0 ? (
        <div className="rows" role="list" aria-label={t("admin.audit")}>
          {items.map((e) => (
            <AuditRow key={e.id} event={e} />
          ))}
        </div>
      ) : null}
      <LoadMore show={!!events.data?.hasMore} pending={events.isFetchingNextPage} onPress={() => void events.fetchNextPage()} />
    </Board>
  );
}

function AuditRow({ event: e }: { event: AuditEvent }) {
  const { t, locale } = useI18n();
  const Icon = ACTOR_ICON[e.actor.kind] ?? Cog;
  const actor = e.actor.name ?? t(`admin.actor.${e.actor.kind}`);
  const resource = [e.resource_kind, e.resource_id ? e.resource_id.slice(0, 13) : null].filter(Boolean).join(" · ");
  return (
    <div className="rowi" role="listitem">
      <span className="ic-box" aria-hidden="true">
        <Icon className="wl-icon" width={18} />
      </span>
      <span style={{ minWidth: 0 }}>
        <span className="rowi-name mono" style={{ fontSize: 14 }}>
          {e.action}
        </span>
        <span className="rowi-sub">{[actor, resource, e.via ? (e.tool ? `${e.via} · ${e.tool}` : e.via) : null].filter(Boolean).join(" · ")}</span>
      </span>
      <span className="rowi-end">
        <span className="caption muted">{formatDateTime(e.ts, locale)}</span>
        <Disclosure
          alignRight
          triggerClassName={`g g--${e.result === "success" ? "ok" : e.result === "denied" ? "warn" : "bad"} g-btn`}
          label={t(`admin.result.${e.result}`)}
          detailsLabel={t("admin.auditDetails")}
          trigger={e.result === "success" ? <Check className="wl-icon" aria-hidden="true" /> : e.result === "denied" ? <Ban className="wl-icon" aria-hidden="true" /> : <X className="wl-icon" aria-hidden="true" />}
          details={
            <dl className="kv">
              <dt>{t("admin.auditResult")}</dt>
              <dd>{t(`admin.result.${e.result}`)}</dd>
              {e.ip ? (
                <>
                  <dt>IP</dt>
                  <dd className="mono-sm">{e.ip}</dd>
                </>
              ) : null}
              {e.request_id ? (
                <>
                  <dt>{t("admin.requestId")}</dt>
                  <dd className="mono-sm">{e.request_id}</dd>
                </>
              ) : null}
              {e.resource_id ? (
                <>
                  <dt>{t("admin.resource")}</dt>
                  <dd className="mono-sm">{e.resource_id}</dd>
                </>
              ) : null}
              {e.details ? (
                <>
                  <dt>{t("admin.auditMore")}</dt>
                  <dd className="mono-sm" style={{ whiteSpace: "pre-wrap" }}>
                    {JSON.stringify(e.details, null, 1)}
                  </dd>
                </>
              ) : null}
            </dl>
          }
        />
      </span>
    </div>
  );
}
