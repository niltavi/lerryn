import { formatDateTime, useAuditEvents, useI18n, type AuditEvent } from "@lerryn/core";
import { Button, Card, Chip, Popover, SearchField, StateCard, Table, ToggleButton, ToggleButtonGroup } from "@lerryn/ui-web";
import { CircleInfo, Gear, Ghost, Key, ListCheck, Person, Server, Thunderbolt } from "@gravity-ui/icons";
import { useState } from "react";

import { Board, BoardTop, LoadError, LoadMore, usePageTitle } from "../../shell/layout";

type ResultFilter = "all" | "success" | "failure" | "denied";
const FILTERS: ResultFilter[] = ["all", "failure", "denied"];

const ACTOR_ICON = { user: Person, token: Key, runner: Server, trigger: Thunderbolt, anonymous: Ghost, system: Gear } as const;
const RESULT_COLOR = { success: "success", denied: "warning", failure: "danger" } as const;

/**
 * One choice out of a few, drawn as joined buttons that report `aria-pressed`.
 * The group runs in multiple-selection mode holding a single key, because its
 * single mode turns the buttons into radios and the console's choices are
 * announced, and tested, as pressed buttons.
 */
export function PressedGroup<T extends string | number>({
  label,
  labelledBy,
  options,
  value,
  onChange,
  fullWidth,
}: {
  label?: string;
  labelledBy?: string;
  options: readonly { id: T; label: string }[];
  value: T;
  onChange: (value: T) => void;
  fullWidth?: boolean;
}) {
  return (
    <ToggleButtonGroup
      aria-label={label}
      aria-labelledby={labelledBy}
      size="sm"
      fullWidth={fullWidth}
      selectionMode="multiple"
      selectedKeys={[value]}
      onSelectionChange={(keys) => {
        // Pressing the current choice empties the set; a choice always stays made.
        const next = options.find((option) => option.id !== value && keys.has(option.id));
        if (next) onChange(next.id);
      }}
    >
      {options.map((option, index) => (
        <ToggleButton key={option.id} id={option.id}>
          {index > 0 ? <ToggleButtonGroup.Separator /> : null}
          {option.label}
        </ToggleButton>
      ))}
    </ToggleButtonGroup>
  );
}

/** The audit log, newest first, filterable by result and action. No record holds a credential value. */
export function AdminAuditPage() {
  const { t, locale } = useI18n();
  usePageTitle(t("admin.audit"));
  const [result, setResult] = useState<ResultFilter>("all");
  const [action, setAction] = useState("");
  const events = useAuditEvents({ result: result === "all" ? undefined : result, action });
  const items = events.data?.items ?? [];

  return (
    <Board label={t("admin.audit")}>
      <BoardTop title={t("admin.audit")}>
        <SearchField aria-label={t("admin.auditAction")} value={action} onChange={setAction}>
          <SearchField.Group>
            <SearchField.SearchIcon />
            <SearchField.Input className="w-40" placeholder={t("admin.auditAction")} />
            <SearchField.ClearButton />
          </SearchField.Group>
        </SearchField>
        <PressedGroup label={t("admin.auditResult")} value={result} onChange={setResult} options={FILTERS.map((f) => ({ id: f, label: t(`admin.result.${f}`) }))} />
      </BoardTop>
      {events.isError ? (
        <LoadError error={events.error} onRetry={() => void events.refetch()} />
      ) : events.data && items.length === 0 ? (
        <StateCard icon={ListCheck} title={t("admin.noAudit")} />
      ) : items.length > 0 ? (
        <Card className="tpl-card nt-fade-up">
          <Table>
            <Table.ScrollContainer>
              <Table.Content aria-label={t("admin.audit")} className="min-w-[720px]">
                <Table.Header>
                  <Table.Column>{t("admin.auditTime")}</Table.Column>
                  <Table.Column>{t("admin.auditActor")}</Table.Column>
                  <Table.Column isRowHeader>{t("admin.auditAction")}</Table.Column>
                  <Table.Column>{t("admin.resource")}</Table.Column>
                  <Table.Column>{t("admin.auditResult")}</Table.Column>
                  <Table.Column className="text-end">
                    <span className="sr-only">{t("admin.auditDetails")}</span>
                  </Table.Column>
                </Table.Header>
                <Table.Body>
                  {items.map((e) => {
                    const Icon = ACTOR_ICON[e.actor.kind] ?? Gear;
                    return (
                      <Table.Row key={e.id} id={e.id}>
                        <Table.Cell className="whitespace-nowrap text-xs text-muted tabular-nums">{formatDateTime(e.ts, locale)}</Table.Cell>
                        <Table.Cell>
                          <div className="flex min-w-0 items-center gap-2">
                            <Icon aria-hidden="true" width={14} height={14} className="shrink-0 text-muted" />
                            <div className="flex min-w-0 flex-col">
                              <span className="truncate text-sm">{e.actor.name ?? t(`admin.actor.${e.actor.kind}`)}</span>
                              {e.via ? <span className="truncate text-xs text-muted">{e.tool ? `${e.via} · ${e.tool}` : e.via}</span> : null}
                            </div>
                          </div>
                        </Table.Cell>
                        <Table.Cell className="font-mono text-xs">{e.action}</Table.Cell>
                        <Table.Cell>
                          <div className="flex flex-col">
                            {e.resource_kind ? <span className="text-sm">{e.resource_kind}</span> : null}
                            {e.resource_id ? <span className="font-mono text-xs text-muted">{e.resource_id.slice(0, 13)}</span> : null}
                          </div>
                        </Table.Cell>
                        <Table.Cell>
                          <Chip size="sm" variant="soft" color={RESULT_COLOR[e.result]}>
                            {t(`admin.result.${e.result}`)}
                          </Chip>
                        </Table.Cell>
                        <Table.Cell>
                          <div className="flex justify-end">
                            <AuditDetails event={e} />
                          </div>
                        </Table.Cell>
                      </Table.Row>
                    );
                  })}
                </Table.Body>
              </Table.Content>
            </Table.ScrollContainer>
          </Table>
        </Card>
      ) : null}
      <LoadMore show={!!events.data?.hasMore} pending={events.isFetchingNextPage} onPress={() => void events.fetchNextPage()} />
    </Board>
  );
}

/** Everything recorded for one event, behind a button so the table stays scannable. */
function AuditDetails({ event: e }: { event: AuditEvent }) {
  const { t } = useI18n();
  return (
    <Popover>
      <Button isIconOnly size="sm" variant="ghost" aria-label={t("admin.auditDetails")}>
        <CircleInfo aria-hidden="true" width={16} height={16} />
      </Button>
      <Popover.Content placement="bottom end" className="max-w-sm">
        <Popover.Dialog aria-label={t("admin.auditDetails")}>
          <dl className="kv text-sm">
            <dt>{t("admin.auditResult")}</dt>
            <dd>{t(`admin.result.${e.result}`)}</dd>
            {e.ip ? (
              <>
                <dt>IP</dt>
                <dd className="font-mono text-xs">{e.ip}</dd>
              </>
            ) : null}
            {e.request_id ? (
              <>
                <dt>{t("admin.requestId")}</dt>
                <dd className="font-mono text-xs">{e.request_id}</dd>
              </>
            ) : null}
            {e.resource_id ? (
              <>
                <dt>{t("admin.resource")}</dt>
                <dd className="font-mono text-xs">{e.resource_id}</dd>
              </>
            ) : null}
            {e.details ? (
              <>
                <dt>{t("admin.auditMore")}</dt>
                <dd className="whitespace-pre-wrap font-mono text-xs">{JSON.stringify(e.details, null, 1)}</dd>
              </>
            ) : null}
          </dl>
        </Popover.Dialog>
      </Popover.Content>
    </Popover>
  );
}
