import { useI18n } from "@lerryn/core";
import { IconButton } from "@lerryn/ui-web";
import { Link } from "@tanstack/react-router";
import { ArrowUp, Maximize, PictureInPicture2, X } from "lucide-react";
import { FocusScope, usePreventScroll } from "react-aria";
import { useState } from "react";

import { RunPreview } from "./RunPreview";

const NEXT_MODE = {
  compact: { mode: "expanded", icon: ArrowUp },
  expanded: { mode: "fullscreen", icon: Maximize },
  fullscreen: { mode: "compact", icon: PictureInPicture2 },
} as const;
type PreviewMode = keyof typeof NEXT_MODE;

export function RunsBoardPreview({ runId, taskId }: { runId: string; taskId?: string }) {
  const { t } = useI18n();
  const [mode, setMode] = useState<PreviewMode>("compact");
  const fullscreen = mode === "fullscreen";
  const next = NEXT_MODE[mode];
  usePreventScroll({ isDisabled: !fullscreen });

  return (
    <FocusScope autoFocus contain={fullscreen} restoreFocus>
      <aside
        className="kb-preview"
        data-mode={mode}
        role={fullscreen ? "dialog" : undefined}
        aria-modal={fullscreen || undefined}
        aria-label={t("runs.preview")}
        onKeyDown={(event) => {
          if (fullscreen && event.key === "Escape" && !event.defaultPrevented) setMode("compact");
        }}
      >
        <div className="kb-preview-head">
          <h2 className="h4">{t("runs.preview")}</h2>
          <div className="tools">
            <IconButton
              label={t(`runs.board.previewMode.${next.mode}`)}
              icon={next.icon}
              onPress={() => setMode((current) => NEXT_MODE[current].mode)}
            />
            <Link
              to="/runs"
              search={{ view: "board", ...(taskId ? { task: taskId } : {}) }}
              className="chip kb-preview-close"
              resetScroll={false}
              activeOptions={{ exact: true, includeSearch: true }}
              aria-label={t("common.close")}
            >
              <X className="wl-icon" aria-hidden="true" />
            </Link>
          </div>
        </div>
        <div key={runId} className="kb-preview-body">
          <RunPreview runId={runId} />
        </div>
      </aside>
    </FocusScope>
  );
}
