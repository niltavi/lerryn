import { useI18n } from "@lerryn/core";
import { ArrowUp, ArrowsExpand, ChevronsCollapseUpRight, Xmark } from "@gravity-ui/icons";
import { IconButton, buttonVariants } from "@lerryn/ui-web";
import { Link } from "@tanstack/react-router";
import { FocusScope, usePreventScroll } from "react-aria";
import { useState } from "react";
import { createPortal } from "react-dom";

import { RunPreview } from "./RunPreview";

const NEXT_MODE = {
  compact: { mode: "expanded", icon: ArrowUp },
  expanded: { mode: "fullscreen", icon: ArrowsExpand },
  fullscreen: { mode: "compact", icon: ChevronsCollapseUpRight },
} as const;
type PreviewMode = keyof typeof NEXT_MODE;

export function RunsBoardPreview({ runId, taskId }: { runId: string; taskId?: string }) {
  const { t } = useI18n();
  const [mode, setMode] = useState<PreviewMode>("compact");
  const fullscreen = mode === "fullscreen";
  const next = NEXT_MODE[mode];
  usePreventScroll({ isDisabled: !fullscreen });

  // The board is a size container, which would become the containing block of a
  // fixed child: the preview would sit at the end of the board, out of view.
  return createPortal(
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
          <h2>{t("runs.preview")}</h2>
          <div className="flex items-center gap-1">
            <IconButton
              label={t(`runs.board.previewMode.${next.mode}`)}
              icon={next.icon}
              onPress={() => setMode((current) => NEXT_MODE[current].mode)}
            />
            <Link
              to="/runs"
              search={{ view: "board", ...(taskId ? { task: taskId } : {}) }}
              className={buttonVariants({ size: "sm", variant: "ghost", isIconOnly: true })}
              resetScroll={false}
              activeOptions={{ exact: true, includeSearch: true }}
              aria-label={t("common.close")}
            >
              <Xmark width={16} height={16} aria-hidden="true" />
            </Link>
          </div>
        </div>
        <div key={runId} className="kb-preview-body">
          <RunPreview runId={runId} />
        </div>
      </aside>
    </FocusScope>,
    document.body,
  );
}
