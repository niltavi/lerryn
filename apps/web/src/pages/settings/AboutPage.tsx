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
          <p className="muted">{t("about.description")}</p>
          <div className="about-links">
            <a href="https://lerryn.niltavi.com/">
              {t("about.website")} <ArrowUpRight className="wl-icon" aria-hidden="true" />
            </a>
            <a href="https://github.com/niltavi/lerryn">
              GitHub <ArrowUpRight className="wl-icon" aria-hidden="true" />
            </a>
          </div>
        </div>
        <NiltaviSignature />
      </div>
    </Board>
  );
}

function NiltaviSignature() {
  return (
    <svg className="about-signature" viewBox="0 0 160 64" role="img" aria-label="niltavi">
      <text x="1" y="40" className="about-wordmark" fill="currentColor">
        niltavi<tspan fill="#c17b31">.</tspan>
      </text>
      <g transform="translate(88 1)">
        <circle cx="36" cy="26" r="24" fill="#d7c080" opacity=".42" />
        <path d="M16 58 54 42" stroke="#937249" strokeWidth="2.5" strokeLinecap="round" />
        <path d="m42 42 11 4 9 15-4 1-13-14Z" fill="#344d63" />
        <path d="M9 14c2-7 10-10 17-7 6 2 8 7 13 11 8 5 14 14 17 25-10 5-23 5-33-1C12 37 11 29 12 21l-8-6Z" fill="#30465b" />
        <path d="M12 23c6-3 12-5 21-5-4 4-3 9 1 14 4 5 10 9 16 12-13 4-24 1-32-7-4-4-6-8-6-14Z" fill="#c99a46" />
        <path d="M11 13c4-6 11-8 17-5 4 2 7 5 9 9-10 1-16-7-26-4Z" fill="#6f8ca6" />
        <path d="M28 20c10-3 24 6 28 24-10-3-19-8-25-15-3-3-4-6-3-9Z" fill="#52718b" />
        <path d="M31 24c7 0 16 6 21 14M33 28c6 1 13 6 18 12M16 30c1 5 5 8 9 10" fill="none" stroke="#e7d5a1" strokeWidth=".7" strokeLinecap="round" />
        <path d="m4 15 10-2 5 4Z" fill="#a3a785" />
        <circle cx="19" cy="15" r="1.15" fill="#1a2834" />
        <path d="m31 44-5 9m11-7-4 5" stroke="#30465b" strokeWidth="1.25" strokeLinecap="round" />
      </g>
    </svg>
  );
}
