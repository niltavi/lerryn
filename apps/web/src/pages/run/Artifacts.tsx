import { formatBytes, formatDate, useArtifactPreview, useArtifacts, useI18n, type Artifact } from "@lerryn/core";
import { IconButton } from "@lerryn/ui-web";
import { Clock, Download, Eye, EyeOff, FileCode, FileJson, FileText, File as FileIcon } from "lucide-react";
import { useState } from "react";

function iconOf(a: Artifact) {
  if (a.preview === "json") return FileJson;
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
    <section className="stack" aria-labelledby="files-title">
      <span className="sec-label" id="files-title">
        {t("artifacts.title")} <span className="num">{items.length}</span>
      </span>
      <div className="wl-card files">
        {items.map((a) => (
          <ArtifactRow key={a.id} artifact={a} open={open === a.id} onToggle={() => setOpen((o) => (o === a.id ? null : a.id))} />
        ))}
      </div>
    </section>
  );
}

function ArtifactRow({ artifact: a, open, onToggle }: { artifact: Artifact; open: boolean; onToggle: () => void }) {
  const { t, locale } = useI18n();
  const preview = useArtifactPreview(open ? a : null);
  const Icon = iconOf(a);
  const name = a.title ?? a.path;
  const expired = !!a.expired_at;
  return (
    <>
      <div className={expired ? "file file--expired" : "file"}>
        <span className="ic-box" aria-hidden="true">
          {expired ? <Clock className="wl-icon" width={18} /> : <Icon className="wl-icon" width={18} />}
        </span>
        <span style={{ minWidth: 0 }}>
          <span className="file-name">{name}</span>
          <span className="caption muted">
            {expired ? t("artifacts.expired", { date: formatDate(a.expired_at!, locale) }) : formatBytes(a.size)}
            {a.title ? ` · ${a.path}` : ""}
          </span>
        </span>
        {expired ? null : (
          <span className="tools">
            {a.preview !== "none" ? (
              <IconButton
                variant={open ? "tertiary" : "ghost"}
                label={open ? t("artifacts.hidePreview") : t("artifacts.previewNamed", { name })}
                tip={open ? t("artifacts.hidePreview") : t("artifacts.preview")}
                icon={open ? EyeOff : Eye}
                onPress={onToggle}
              />
            ) : null}
            <a
              className="button button--sm button--ghost button--icon-only"
              href={a.links.download}
              download
              aria-label={t("artifacts.downloadNamed", { name })}
              data-tip={a.preview === "none" ? t("artifacts.downloadOnly") : t("artifacts.download")}
              data-tip-pos="top"
            >
              <Download className="wl-icon" aria-hidden="true" />
            </a>
          </span>
        )}
      </div>
      {open ? (
        <pre className="preview" aria-label={name}>
          {preview.isError ? t("states.errorTitle") : preview.data ? preview.data.text : t("common.loading")}
          {preview.data?.cut ? `\n…\n${t("artifacts.cut")}` : ""}
        </pre>
      ) : null}
    </>
  );
}
