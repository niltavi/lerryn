import { ListOl } from "@gravity-ui/icons";
import { useI18n, type TaskVersion } from "@lerryn/core";
import { CollapsiblePanel, Timeline } from "@lerryn/ui-web";

export function TaskWorkflow({ version }: { version: TaskVersion }) {
  const { t } = useI18n();
  const steps = version.manifest.workflow;
  if (!steps?.length) return null;

  return (
    <CollapsiblePanel
      title={t("task.workflow")}
      icon={<ListOl width={18} height={18} aria-hidden="true" />}
      aside={<span className="font-mono text-xs text-muted">v{version.seq}</span>}
    >
      <Timeline>
        {steps.map((step, index) => (
          <Timeline.Item key={index} marker={index + 1}>
            <h3 className="text-sm font-medium">{step.title}</h3>
            <p className="mt-1 whitespace-pre-line text-sm text-muted">{step.description}</p>
          </Timeline.Item>
        ))}
      </Timeline>
    </CollapsiblePanel>
  );
}
