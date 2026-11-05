import { groupToolsByProject, toolLocation, useI18n, useProjects, useTools, type Tool, type ToolGroup } from "@lerryn/core";
import { CopyButton, StateCard } from "@lerryn/ui-web";
import { Copy, FileCode, Globe, Plug } from "lucide-react";

import { Board, BoardTop, LoadError, usePageTitle } from "../../shell/layout";

/**
 * Tools registered in the projects the user can see (tech design 7.8),
 * read-only: agents register and remove them through the API, and the
 * console does not edit what tasks run with (tech design 13.3).
 */
export function ToolsPage() {
  const { t } = useI18n();
  usePageTitle(t("tools.title"));
  const tools = useTools(null);
  const projects = useProjects();
  const groups = groupToolsByProject(tools.data?.items ?? [], projects.data?.items ?? []);

  return (
    <Board label={t("tools.title")}>
      <BoardTop title={t("tools.title")} />
      {tools.isError ? (
        <LoadError error={tools.error} onRetry={() => void tools.refetch()} />
      ) : tools.data && groups.length === 0 ? (
        <StateCard icon={Plug} title={t("tools.empty")} />
      ) : (
        groups.map((g) => <Group key={g.projectId} group={g} labelled={groups.length > 1} />)
      )}
    </Board>
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
