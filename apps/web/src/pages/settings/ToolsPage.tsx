import { groupToolsByProject, toolLocation, useI18n, useProjects, useTools, type Tool, type ToolGroup } from "@lerryn/core";
import { CopyButton, StateCard } from "@lerryn/ui-web";
import { Link } from "@tanstack/react-router";
import { BookOpen, Copy, FileCode, Globe, Plug } from "lucide-react";

import guideZh from "../../../../../docs/zh-CN/tools.md?url";
import guideEn from "../../../../../docs/en/tools.md?url";
import { origin } from "../../lib/hooks";
import { Board, LoadError, usePageTitle } from "../../shell/layout";

// These are Lerryn's own MCP tools, separate from the project registry.
const BUILTIN_TOOLS = [
  ["discover", "read"],
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
  ["api_describe", "read"],
  ["api_call", "perOperation"],
] as const;

export function ToolsPage() {
  const { t, locale } = useI18n();
  usePageTitle(t("tools.title"));
  const tools = useTools(null);
  const projects = useProjects();
  const groups = groupToolsByProject(tools.data?.items ?? [], projects.data?.items ?? []);

  return (
    <Board label={t("tools.title")}>
      <div className="board-top">
        <h1 className="h2">{t("tools.title")}</h1>
        <a href={locale === "en" ? guideEn : guideZh} download="lerryn-tools.md" className="row">
          <BookOpen className="wl-icon" aria-hidden="true" />
          {t("tools.guide")}
        </a>
      </div>
      <p className="body-sm muted">{t("tools.intro")}</p>
      <Interfaces />
      <section className="stack" aria-labelledby="builtin-tools-title" style={{ gap: "var(--space-3)" }}>
        <h2 className="sec-label" id="builtin-tools-title">{t("tools.builtins")}</h2>
        <p className="body-sm muted">{t("tools.builtinsHelp")}</p>
        <div className="rows" role="list" aria-label={t("tools.builtins")}>
          {BUILTIN_TOOLS.map(([name, scope]) => (
            <div className="rowi" role="listitem" key={name}>
              <span className="ic-box" aria-hidden="true"><Plug className="wl-icon" /></span>
              <div style={{ minWidth: 0 }}>
                <code className="rowi-name">{name}</code>
                <p className="body-sm muted">{t(`tools.builtin.${name}`)}</p>
              </div>
              <span className="tag tag--mono">{scope === "perOperation" ? t("tools.perOperation") : scope}</span>
            </div>
          ))}
        </div>
      </section>
      <section className="stack" aria-labelledby="sdk-title" style={{ gap: "var(--space-3)" }}>
        <h2 className="sec-label" id="sdk-title">{t("tools.sdk")}</h2>
        <p className="body-sm muted">{t("tools.sdkHelp")}</p>
        <pre className="data-pre"><code>{`sbx stage start fetch
sbx stage done fetch
sbx result --status ok --summary "Done" --data-json '{"count": 1}'`}</code></pre>
      </section>
      <section className="stack" aria-labelledby="project-tools-title" style={{ gap: "var(--space-3)" }}>
        <h2 className="sec-label" id="project-tools-title">{t("tools.registered")}</h2>
        <p className="body-sm muted">{t("tools.registeredHelp")}</p>
        {tools.isError ? (
          <LoadError error={tools.error} onRetry={() => void tools.refetch()} />
        ) : tools.data && groups.length === 0 ? (
          <StateCard icon={Plug} title={t("tools.empty")} />
        ) : (
          groups.map((g) => <Group key={g.projectId} group={g} labelled={groups.length > 1} />)
        )}
      </section>
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
    <section className="stack" aria-labelledby="tool-interfaces-title" style={{ gap: "var(--space-3)" }}>
      <h2 className="sec-label" id="tool-interfaces-title">{t("tools.interfaces")}</h2>
      <p className="body-sm muted">{t("tools.authHelp")} <Link to="/settings/tokens">{t("tools.manageTokens")}</Link></p>
      <div className="copygrid">
        {lines.map(([name, value, label]) => (
          <div className="copyline copyline--wrap" key={name}>
            <span className="tag tag--mono">{name}</span>
            <code>{value}</code>
            <CopyButton value={value} label={t(label)} icon={Copy} />
          </div>
        ))}
      </div>
    </section>
  );
}

function Group({ group, labelled }: { group: ToolGroup; labelled: boolean }) {
  const { t } = useI18n();
  const name = group.project ? (group.project.is_personal ? t("overview.personalProject") : group.project.name) : t("tools.otherProject");
  return (
    <section className="stack" style={{ gap: 10 }} aria-label={labelled ? name : t("tools.title")}>
      {labelled ? <span className="sec-label">{name}</span> : null}
      <div className="rows" role="list">
        {group.tools.map((tool) => (
          <ToolRow key={tool.id} tool={tool} />
        ))}
      </div>
    </section>
  );
}

const KIND_ICON = { mcp: Plug, http: Globe, script: FileCode } as const;

function ToolRow({ tool }: { tool: Tool }) {
  const { t } = useI18n();
  // A kind added later than this page falls back to the generic icon.
  const Icon = KIND_ICON[tool.kind] ?? Globe;
  const where = toolLocation(tool);
  return (
    <div className="rowi" role="listitem">
      <span className="ic-box" aria-hidden="true">
        <Icon className="wl-icon" width={18} />
      </span>
      <span style={{ minWidth: 0 }}>
        <span className="rowi-name mono" style={{ fontSize: 14 }}>
          {tool.name}
        </span>
        <span className="rowi-sub" title={tool.description}>
          {tool.description}
        </span>
      </span>
      <span className="rowi-end">
        <span className="tag tag--mono">{tool.kind.toUpperCase()}</span>
        {where?.kind === "url" ? (
          <>
            <span className="mono-sm muted" title={where.url}>
              {where.host}
            </span>
            <CopyButton value={where.url} label={t("tools.copyUrl", { name: tool.name })} icon={Copy} />
          </>
        ) : where?.kind === "files" ? (
          <span className="mono-sm muted" title={where.treeHash ?? undefined}>
            {[where.shortHash, t("tools.fileCount", { count: where.fileCount })].filter(Boolean).join(" · ")}
          </span>
        ) : null}
      </span>
    </div>
  );
}
