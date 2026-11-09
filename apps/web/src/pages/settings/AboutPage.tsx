import { useDiscovery, useI18n } from "@lerryn/core";
import { Card, Logo, useLerrynTheme } from "@lerryn/ui-web";
import { ArrowUpRight, CodeXml, Globe } from "lucide-react";

import niltaviDark from "../../../../../docs/assets/niltavi-logo-dark.svg?url";
import niltaviLight from "../../../../../docs/assets/niltavi-logo-light.svg?url";
import { Board, usePageTitle } from "../../shell/layout";

export function AboutPage() {
  const { t } = useI18n();
  const { theme } = useLerrynTheme();
  const discovery = useDiscovery();
  const version = discovery.data?.server.version;
  usePageTitle(t("settings.about"));

  return (
    <Board label={t("settings.about")}>
      <div className="about-panel">
        <div className="about-content">
          <div className="about-grid">
            <Card className="about-project">
              <Card.Header className="about-project-header">
                <span className="about-project-mark"><Logo size={16} label="" /></span>
                <div className="about-project-details">
                  <h1 className="h1">{t("app.name")}</h1>
                  <dl className="about-version body-sm muted">
                    <dt>{t("about.version")}</dt>
                    <dd aria-live="polite">
                      {version ? <code className="mono-sm">{version}</code> : t(discovery.isPending ? "common.loading" : "common.unavailable")}
                    </dd>
                  </dl>
                </div>
              </Card.Header>
              <Card.Footer className="about-links">
                <a href="https://lerryn.niltavi.com/">
                  <Globe className="wl-icon" aria-hidden="true" />
                  {t("about.website")}
                  <ArrowUpRight className="wl-icon" aria-hidden="true" />
                </a>
                <a href="https://github.com/niltavi/lerryn">
                  <CodeXml className="wl-icon" aria-hidden="true" />
                  GitHub
                  <ArrowUpRight className="wl-icon" aria-hidden="true" />
                </a>
              </Card.Footer>
            </Card>

            <section className="about-philosophy" aria-labelledby="about-philosophy-title">
              <h2 id="about-philosophy-title" className="sec-label">
                <a href="https://ui.niltavi.com/#/philosophy">
                  {t("about.philosophy")}
                  <ArrowUpRight className="wl-icon" aria-hidden="true" />
                </a>
              </h2>
              <blockquote cite="https://ui.niltavi.com/#/philosophy">
                <p>{t("about.philosophyText")}</p>
              </blockquote>
            </section>
          </div>
        </div>
        <footer className="about-footer">
          <a href="https://niltavi.com/" className="about-signature">
            <img src={theme === "dark" ? niltaviDark : niltaviLight} alt="niltavi" width="108" height="40" />
          </a>
        </footer>
      </div>
    </Board>
  );
}
