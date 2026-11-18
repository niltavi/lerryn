import { formatBytes, formatDate, useArtifactPreview, useArtifacts, useI18n, type Artifact } from "@lerryn/core";
import { ArrowDownToLine, Clock, CurlyBrackets, Eye, EyeSlash, File as FileIcon, FileCode, FileText } from "@gravity-ui/icons";
import { Card, IconButton, buttonVariants } from "@lerryn/ui-web";
import { useState } from "react";

function iconOf(a: Artifact) {
  if (a.preview === "json") return CurlyBrackets;
  if (a.preview === "text") return FileText;
  if (/html|javascript|x-sh|x-python/.test(a.content_type)) return FileCode;
  return FileIcon;
}

/**
 * The files a run produced (prototype Run-Done.dc.html). Text and JSON can
 * be previewed as plain text; HTML, scripts and binaries are offered as
 * downloads only, and the server sends them as attachments. Expired files
 * stay listed as expired. Renders nothing for a run without files.
 */
export function Artifacts({ runId, active }: { runId: string; active: boolean }) {
  const { t } = useI18n();
  const artifacts = useArtifacts(runId, active);
  const [open, setOpen] = useState<string | null>(null);
  const items = artifacts.data?.items ?? [];
  if (items.length === 0) return null;
  return (
    <Card className="tpl-card" role="region" aria-labelledby="files-title">
      <div className="tpl-card__head">
        <h2 id="files-title" className="text-sm font-semibold">
          {t("artifacts.title")} <span className="font-normal text-muted tabular-nums">{items.length}</span>
        </h2>
      </div>
      <ul className="lr-rows">
        {items.map((a) => (
          <ArtifactRow key={a.id} artifact={a} open={open === a.id} onToggle={() => setOpen((o) => (o === a.id ? null : a.id))} />
        ))}
      </ul>
    </Card>
  );
}

function ArtifactRow({ artifact: a, open, onToggle }: { artifact: Artifact; open: boolean; onToggle: () => void }) {
  const { t, locale } = useI18n();
  const preview = useArtifactPreview(open ? a : null);
  const name = a.title ?? a.path;
  const expired = !!a.expired_at;
  const Icon = expired ? Clock : iconOf(a);
  return (
    <li className="lr-row flex-wrap" data-quiet={expired || undefined}>
      <span className="lr-row__icon" data-tone="idle" aria-hidden="true">
        <Icon width={16} height={16} />
      </span>
      <span className="lr-row__text">
        <span className="lr-row__title font-mono text-xs">{name}</span>
        <span className="lr-row__sub">
          {expired ? t("artifacts.expired", { date: formatDate(a.expired_at!, locale) }) : formatBytes(a.size)}
          {a.title ? ` · ${a.path}` : ""}
        </span>
      </span>
      {expired ? null : (
        <span className="lr-row__end gap-1">
          {a.preview !== "none" ? (
            <IconButton
              variant={open ? "tertiary" : "ghost"}
              label={open ? t("artifacts.hidePreview") : t("artifacts.previewNamed", { name })}
              tip={open ? t("artifacts.hidePreview") : t("artifacts.preview")}
              icon={open ? EyeSlash : Eye}
              onPress={onToggle}
            />
          ) : null}
          {/* A real link keeps the browser's own download handling; a Button cannot carry href. */}
          <a
            className={buttonVariants({ size: "sm", variant: "ghost", isIconOnly: true })}
            href={a.links.download}
            download
            aria-label={t("artifacts.downloadNamed", { name })}
            title={a.preview === "none" ? t("artifacts.downloadOnly") : t("artifacts.download")}
          >
            <ArrowDownToLine width={16} height={16} aria-hidden="true" />
          </a>
        </span>
      )}
      {open ? (
        <pre className="preview" aria-label={name}>
          {preview.isError ? t("states.errorTitle") : preview.data ? preview.data.text : t("common.loading")}
          {preview.data?.cut ? `\n…\n${t("artifacts.cut")}` : ""}
        </pre>
      ) : null}
    </li>
  );
}
