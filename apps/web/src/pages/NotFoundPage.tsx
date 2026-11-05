import { useI18n } from "@lerryn/core";
import { NotFoundState } from "@lerryn/ui-web";
import { Link } from "@tanstack/react-router";

import { Board, usePageTitle } from "../shell/layout";

/** Shown for unknown paths and for resources the viewer may not see. */
export function NotFoundPage() {
  const { t } = useI18n();
  usePageTitle(t("states.notFoundTitle"));
  return (
    <div className="wl-app" style={{ display: "flex" }}>
      <Board center>
        <div style={{ maxWidth: 420, width: "100%", margin: "0 auto" }}>
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
        <Link to="/" className="link">
          {t("common.backToOverview")}
        </Link>
      }
    />
  );
}
