import { useI18n } from "@lerryn/core";
import { Button, Dropdown } from "@lerryn/ui-web";
import { Link } from "@tanstack/react-router";
import { ArrowUp, Maximize, PictureInPicture2, X } from "lucide-react";
import { FocusScope, usePreventScroll } from "react-aria";
import { useState } from "react";

import { RunPreview } from "./RunPreview";

const MODE_ICONS = { compact: PictureInPicture2, expanded: ArrowUp, fullscreen: Maximize } as const;
type PreviewMode = keyof typeof MODE_ICONS;
const MODES = Object.keys(MODE_ICONS) as PreviewMode[];

export function RunsBoardPreview({ runId, taskId }: { runId: string; taskId?: string }) {
  const { t } = useI18n();
  const [mode, setMode] = useState<PreviewMode>("compact");
  const fullscreen = mode === "fullscreen";
  const ModeIcon = MODE_ICONS[mode];
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
            <Dropdown>
              <Button variant="ghost" size="sm" isIconOnly aria-label={t("runs.board.previewMode.label")}>
                <ModeIcon className="wl-icon" aria-hidden="true" />
              </Button>
              <Dropdown.Popover className="kb-preview-menu" placement="bottom end">
                <Dropdown.Menu
                  aria-label={t("runs.board.previewMode.label")}
                  selectionMode="single"
                  disallowEmptySelection
                  selectedKeys={[mode]}
                >
                  {MODES.map((choice) => {
                    const Icon = MODE_ICONS[choice];
                    return (
                      <Dropdown.Item key={choice} id={choice} onAction={() => setMode(choice)}>
                        <Icon className="wl-icon" width={16} height={16} aria-hidden="true" />
                        {t(`runs.board.previewMode.${choice}`)}
                        <Dropdown.ItemIndicator />
                      </Dropdown.Item>
                    );
                  })}
                </Dropdown.Menu>
              </Dropdown.Popover>
            </Dropdown>
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
