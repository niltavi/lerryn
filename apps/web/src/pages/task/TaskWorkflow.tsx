import { Workflow } from "lucide-react";
import { useI18n, type TaskVersion } from "@lerryn/core";
import { CollapsiblePanel, Timeline } from "@lerryn/ui-web";

export function TaskWorkflow({ version }: { version: TaskVersion }) {
  const { t } = useI18n();
  const steps = version.manifest.workflow;
  if (!steps?.length) return null;

  return (
    <CollapsiblePanel
      title={t("task.workflow")}
      icon={<Workflow className="wl-icon" aria-hidden="true" />}
      aside={<span className="mono-sm muted">v{version.seq}</span>}
    >
      <Timeline>
        {steps.map((step, index) => (
          <Timeline.Item key={index} marker={index + 1}>
            <h3 className="panel-title">{step.title}</h3>
            <p className="task-workflow-description body-sm muted">{step.description}</p>
          </Timeline.Item>
        ))}
      </Timeline>
    </CollapsiblePanel>
  );
}
