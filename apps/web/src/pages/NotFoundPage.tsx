import { useI18n } from "@lerryn/core";
import { NotFoundState } from "@lerryn/ui-web";
import { Link } from "@tanstack/react-router";

import { Board, usePageTitle } from "../shell/layout";

/** Shown for unknown paths and for resources the viewer may not see. */
export function NotFoundPage() {
  const { t } = useI18n();
  usePageTitle(t("states.notFoundTitle"));
  return (
    // Also the router's fallback outside the console frame, so it brings its own full-size row.
    <div className="flex size-full min-h-0">
      <Board center>
        <div className="w-full max-w-md">
          <NotFoundBody />
        </div>
      </Board>
    </div>
  );
}

/** The not-found card with a way back, for use inside a page's board. */
export function NotFoundBody() {
  const { t } = useI18n();
  return (
    <NotFoundState
      action={
        <Link to="/" className="font-medium text-accent hover:underline">
          {t("common.backToOverview")}
        </Link>
      }
    />
  );
}
