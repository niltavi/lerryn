import { useI18n, useTaskDiff, type FileDiff } from "@lerryn/core";
import { Button, ConsoleDialog } from "@lerryn/ui-web";
import { GitCompare } from "lucide-react";

/** Splits a unified diff into lines tagged for colour. Exported for tests. */
export function diffLines(patch: string): { kind: "add" | "del" | "hunk" | "ctx"; text: string }[] {
  return patch
    .split("\n")
    .filter((line, i, all) => !(i === all.length - 1 && line === ""))
    .filter((line) => !line.startsWith("+++ ") && !line.startsWith("--- "))
    .map((text) => ({
      kind: text.startsWith("@@") ? "hunk" : text.startsWith("+") ? "add" : text.startsWith("-") ? "del" : "ctx",
      text,
    }));
}

function Patch({ patch, label }: { patch: string; label: string }) {
  return (
    <pre className="diff" aria-label={label}>
      {diffLines(patch).map((l, i) => (
        <span key={i} className={l.kind === "ctx" ? undefined : l.kind}>
          {l.text || " "}
        </span>
      ))}
    </pre>
  );
}

/**
 * What changed in a version, read-only: the manifest and each file as
 * unified diffs (diffTaskVersion). The console never edits a task.
 */
export function VersionDiffDialog({ taskId, seq, onClose }: { taskId: string; seq: number | null; onClose: () => void }) {
  const { t } = useI18n();
  const diff = useTaskDiff(taskId, seq);
  const d = diff.data;
  const change = (f: FileDiff) => t(`diff.change.${f.change}`);
  return (
    <ConsoleDialog
      isOpen={seq !== null}
      size="lg"
      onOpenChange={(open) => (open ? undefined : onClose())}
      title={
        <>
          <GitCompare className="wl-icon" aria-hidden="true" />
          {d ? (d.base ? t("diff.title", { target: d.target, base: d.base }) : t("diff.titleFirst", { target: d.target })) : t("diff.loading")}
        </>
      }
      footer={<Button onPress={onClose}>{t("common.close")}</Button>}
    >
      {diff.isError ? <p className="body-sm muted">{t("states.errorTitle")}</p> : null}
      {d ? (
        <div className="stack" style={{ gap: 18 }}>
          {d.manifest_patch ? (
            <div className="diff-file">
              <span className="label-text">{t("diff.manifest")}</span>
              <Patch patch={d.manifest_patch} label={t("diff.manifest")} />
            </div>
          ) : null}
          {d.files.map((f) => (
            <div className="diff-file" key={f.path}>
              <span className="spread">
                <span className="mono-sm" style={{ fontWeight: 600 }}>
                  {f.path}
                </span>
                <span className="caption muted">
                  {change(f)}
                  {f.additions || f.deletions ? ` · +${f.additions} −${f.deletions}` : ""}
                </span>
              </span>
              {f.patch ? <Patch patch={f.patch} label={f.path} /> : <span className="caption muted">{t("diff.noPatch")}</span>}
            </div>
          ))}
          {!d.manifest_patch && d.files.length === 0 ? <p className="body-sm muted">{t("diff.none")}</p> : null}
          {d.truncated ? <p className="caption muted">{t("diff.truncated")}</p> : null}
        </div>
      ) : null}
    </ConsoleDialog>
  );
}
