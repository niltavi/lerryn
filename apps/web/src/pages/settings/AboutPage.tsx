import { useDiscovery, useI18n } from "@lerryn/core";
import { Card, Link, Logo, useLerrynTheme } from "@lerryn/ui-web";
import { Globe, LogoGithub } from "@gravity-ui/icons";

import niltaviDark from "../../../../../docs/assets/niltavi-logo-dark.svg?url";
import niltaviLight from "../../../../../docs/assets/niltavi-logo-light.svg?url";
import { Board, usePageTitle } from "../../shell/layout";

/** What this console is, which version runs, and the thinking behind its look. */
export function AboutPage() {
  const { t } = useI18n();
  const { theme } = useLerrynTheme();
  const discovery = useDiscovery();
  const version = discovery.data?.server.version;
  usePageTitle(t("settings.about"));

  return (
    <Board label={t("settings.about")} center>
      <div className="lr-about nt-fade-up">
        <div className="lr-about__grid">
          <Card className="tpl-card">
            <div className="flex min-w-0 items-center gap-4">
              <span className="lr-about__mark">
                <Logo size={16} label="" />
              </span>
              <div className="min-w-0">
                <h1 className="text-xl font-semibold tracking-tight">{t("app.name")}</h1>
                <dl className="lr-about__version">
                  <dt>{t("about.version")}</dt>
                  <dd aria-live="polite">
                    {version ? <code className="text-xs">{version}</code> : t(discovery.isPending ? "common.loading" : "common.unavailable")}
                  </dd>
                </dl>
              </div>
            </div>
            <ul className="lr-rows text-sm">
              <li className="lr-row">
                <Globe className="shrink-0 text-muted" aria-hidden="true" width={16} height={16} />
                <Link href="https://lerryn.niltavi.com/">
                  {t("about.website")}
                  <Link.Icon />
                </Link>
              </li>
              <li className="lr-row">
                <LogoGithub className="shrink-0 text-muted" aria-hidden="true" width={16} height={16} />
                <Link href="https://github.com/niltavi/lerryn">
                  GitHub
                  <Link.Icon />
                </Link>
              </li>
            </ul>
          </Card>

          <section className="lr-about__philosophy" aria-labelledby="about-philosophy-title">
            <h2 id="about-philosophy-title" className="text-xs font-medium">
              <Link href="https://ui.niltavi.com/#/philosophy" className="text-muted">
                {t("about.philosophy")}
                <Link.Icon />
              </Link>
            </h2>
            <blockquote cite="https://ui.niltavi.com/#/philosophy" className="lr-about__quote">
              <p>{t("about.philosophyText")}</p>
            </blockquote>
          </section>
        </div>
        <footer className="flex justify-end">
          <a href="https://niltavi.com/" className="lr-about__signature">
            <img src={theme === "dark" ? niltaviDark : niltaviLight} alt="niltavi" width="108" height="40" />
          </a>
        </footer>
      </div>
    </Board>
  );
}
