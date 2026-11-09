import { useI18n } from "@lerryn/core";
import { Logo } from "@lerryn/ui-web";
import { ArrowUpRight } from "lucide-react";

import { Board, usePageTitle } from "../../shell/layout";

export function AboutPage() {
  const { t } = useI18n();
  usePageTitle(t("settings.about"));

  return (
    <Board label={t("settings.about")}>
      <div className="about-panel">
        <div className="about-project">
          <Logo size={36} label="" />
          <h1 className="h1">{t("app.name")}</h1>
          <div className="about-links">
            <a href="https://lerryn.niltavi.com/">
              {t("about.website")} <ArrowUpRight className="wl-icon" aria-hidden="true" />
            </a>
            <a href="https://github.com/niltavi/lerryn">
              GitHub <ArrowUpRight className="wl-icon" aria-hidden="true" />
            </a>
          </div>
        </div>
        <svg className="about-signature" viewBox="0 0 90 32" role="img" aria-label="niltavi">
          <text x="89" y="25" textAnchor="end" fill="currentColor">
            niltavi<tspan fill="#c17b31">.</tspan>
          </text>
        </svg>
      </div>
    </Board>
  );
}
