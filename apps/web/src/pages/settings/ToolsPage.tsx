import { groupToolsByProject, toolLocation, useI18n, useProjects, useTools, type Tool, type ToolGroup } from "@lerryn/core";
import { Card, Chip, CopyButton, Link, StateCard } from "@lerryn/ui-web";
import { ArrowDownToLine, Copy, FileCode, Globe, PlugConnection } from "@gravity-ui/icons";
import { Link as RouterLink } from "@tanstack/react-router";

import { origin } from "../../lib/hooks";
import { Board, BoardTop, LoadError, Section, usePageTitle } from "../../shell/layout";

// These are Lerryn's own MCP tools, separate from the project registry.
const BUILTIN_TOOLS = [
  ["discover", "read"],
  ["environment_get", "read"],
  ["task_get", "read"],
  ["task_list", "read"],
  ["task_set_tags", "write"],
  ["task_apply", "write"],
  ["task_validate", "read"],
  ["trigger_set", "write"],
  ["task_set_active", "execute"],
  ["run_start", "execute"],
  ["run_get", "read"],
  ["run_logs", "read"],
  ["run_cancel", "execute"],
  ["run_rerun", "execute"],
  ["credential_request", "write"],
  ["ai_models", "read"],
  ["ai_chat", "execute"],
  ["api_describe", "read"],
  ["api_call", "perOperation"],
] as const;

export function ToolsPage() {
  const { t } = useI18n();
  usePageTitle(t("tools.title"));
  const tools = useTools(null);
  const projects = useProjects();
  const groups = groupToolsByProject(tools.data?.items ?? [], projects.data?.items ?? []);

  return (
    <Board label={t("tools.title")}>
      <BoardTop title={t("tools.title")} description={t("tools.intro")}>
        <Link className="gap-1 text-sm" href="/docs/en/tools.md" hrefLang="en" download="lerryn-tools.md">
          <ArrowDownToLine aria-hidden="true" width={14} height={14} />
          {t("tools.guide")}
        </Link>
        <Link className="gap-1 text-sm" href="/docs/zh-CN/tools.md" hrefLang="zh-CN" download="lerryn-tools-zh-CN.md">
          <ArrowDownToLine aria-hidden="true" width={14} height={14} />
          {t("tools.guideZh")}
        </Link>
      </BoardTop>
      <Interfaces />
      <Section title={t("tools.builtins")}>
        <p className="text-sm text-muted">{t("tools.builtinsHelp")}</p>
        <Card className="tpl-card nt-fade-up">
          <ul className="lr-rows" aria-label={t("tools.builtins")}>
            {BUILTIN_TOOLS.map(([name, scope]) => (
              <li className="lr-row" key={name}>
                <span className="lr-row__icon" aria-hidden="true">
                  <PlugConnection width={16} height={16} />
                </span>
                <div className="flex min-w-0 flex-1 flex-col">
                  <code className="font-mono text-sm font-medium">{name}</code>
                  <p className="text-xs text-muted">{t(`tools.builtin.${name}`)}</p>
                </div>
                <Chip size="sm" variant="soft" className="font-mono">
                  {scope === "perOperation" ? t("tools.perOperation") : scope}
                </Chip>
              </li>
            ))}
          </ul>
        </Card>
      </Section>
      <Section title={t("tools.sdk")}>
        <p className="text-sm text-muted">{t("tools.sdkHelp")}</p>
        <pre className="lr-keys__code"><code>{`sbx stage start fetch
sbx stage done fetch
sbx result --status ok --summary "Done" --data-json '{"count": 1}'`}</code></pre>
      </Section>
      <Section title={t("tools.registered")}>
        <p className="text-sm text-muted">{t("tools.registeredHelp")}</p>
        {tools.isError ? (
          <LoadError error={tools.error} onRetry={() => void tools.refetch()} />
        ) : tools.data && groups.length === 0 ? (
          <StateCard icon={PlugConnection} title={t("tools.empty")} />
        ) : (
          groups.map((g) => <Group key={g.projectId} group={g} labelled={groups.length > 1} />)
        )}
      </Section>
    </Board>
  );
}

function Interfaces() {
  const { t } = useI18n();
  const base = origin();
  const lines = [
    ["MCP", `${base}/mcp`, "tokens.copyMcp"],
    ["HTTP API", `${base}/api/v1`, "tokens.copyApi"],
    ["CLI", `lerryn login ${base}`, "tokens.copyCli"],
    ["Skill", "lerryn skills install", "tokens.copySkills"],
  ] as const;
  return (
    <Section title={t("tools.interfaces")}>
      <p className="text-sm text-muted">
        {t("tools.authHelp")}{" "}
        <RouterLink to="/settings/tokens" className="font-medium text-accent underline-offset-2 hover:underline">
          {t("tools.manageTokens")}
        </RouterLink>
      </p>
      <div className="lr-keys__copies">
        {lines.map(([name, value, label]) => (
          <div className="lr-copy lr-copy--wrap" key={name}>
            <span className="lr-copy__tag">{name}</span>
            <code>{value}</code>
            <CopyButton value={value} label={t(label)} icon={Copy} />
          </div>
        ))}
      </div>
    </Section>
  );
}

function Group({ group, labelled }: { group: ToolGroup; labelled: boolean }) {
  const { t } = useI18n();
  const name = group.project ? (group.project.is_personal ? t("overview.personalProject") : group.project.name) : t("tools.otherProject");
  return (
    <section className="lr-section" aria-label={labelled ? name : t("tools.title")}>
      {labelled ? <span className="lr-section__label">{name}</span> : null}
      <Card className="tpl-card nt-fade-up">
        <ul className="lr-rows">
          {group.tools.map((tool) => (
            <ToolRow key={tool.id} tool={tool} />
          ))}
        </ul>
      </Card>
    </section>
  );
}

const KIND_ICON = { mcp: PlugConnection, http: Globe, script: FileCode } as const;

function ToolRow({ tool }: { tool: Tool }) {
  const { t } = useI18n();
  // A kind added later than this page falls back to the generic icon.
  const Icon = KIND_ICON[tool.kind] ?? Globe;
  const where = toolLocation(tool);
  return (
    <li className="lr-row">
      <span className="lr-row__icon" aria-hidden="true">
        <Icon width={16} height={16} />
      </span>
      <span className="lr-row__text">
        <span className="lr-row__title font-mono text-sm">{tool.name}</span>
        <span className="lr-row__sub" title={tool.description}>
          {tool.description}
        </span>
      </span>
      <span className="lr-row__end">
        <Chip size="sm" variant="soft" className="font-mono">
          {tool.kind.toUpperCase()}
        </Chip>
        {where?.kind === "url" ? (
          <>
            <span className="font-mono text-xs text-muted" title={where.url}>
              {where.host}
            </span>
            <CopyButton value={where.url} label={t("tools.copyUrl", { name: tool.name })} icon={Copy} />
          </>
        ) : where?.kind === "files" ? (
          <span className="font-mono text-xs text-muted" title={where.treeHash ?? undefined}>
            {[where.shortHash, t("tools.fileCount", { count: where.fileCount })].filter(Boolean).join(" · ")}
          </span>
        ) : null}
      </span>
    </li>
  );
}
