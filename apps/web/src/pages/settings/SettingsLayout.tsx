import { daysUntil, useAccessTokens, useDevices, useI18n, useNotificationChannels, usePendingCredentialRequests } from "@lerryn/core";
import { WaveGlyph } from "@lerryn/ui-web";
import { Outlet } from "@tanstack/react-router";
import { Bell, Info, KeyRound, Plug, Smartphone, UserRound } from "lucide-react";

import { ListColumn, SectionLink } from "../../shell/layout";

const TokenIcon = (props: { className?: string }) => <WaveGlyph size={18} className={props.className} />;

/** Agent access: tokens, credentials and registered tools. */
export function KeySettingsLayout() {
  const { t } = useI18n();
  const tokens = useAccessTokens();
  const requests = usePendingCredentialRequests();
  const usable = tokens.data?.items.filter(
    (tok) => !tok.revoked_at && (!tok.expires_at || daysUntil(tok.expires_at) > 0),
  ).length;
  const pending = requests.data?.items.length ?? 0;
  return (
    <>
      <ListColumn title={t("settings.keyTitle")} label={t("settings.keyTitle")}>
        <SectionLink to="/settings/tokens" icon={TokenIcon} label={t("settings.tokens")} meta={tokens.data?.hasMore ? `${usable}+` : usable} />
        <SectionLink
          to="/settings/credentials"
          icon={KeyRound}
          label={t("settings.credentials")}
          meta={pending ? t("credentials.pendingCount", { count: requests.hasNextPage ? `${pending}+` : pending }) : undefined}
          metaBad={pending > 0}
        />
        <SectionLink to="/settings/tools" icon={Plug} label={t("settings.tools")} />
      </ListColumn>
      <Outlet />
    </>
  );
}

/** Personal account and project information. */
export function AccountSettingsLayout() {
  const { t } = useI18n();
  const channels = useNotificationChannels();
  const devices = useDevices();
  return (
    <>
      <ListColumn title={t("settings.account")} label={t("settings.account")}>
        <SectionLink to="/settings/account" icon={UserRound} label={t("settings.account")} />
        <SectionLink to="/settings/notifications" icon={Bell} label={t("settings.notifications")} meta={channels.data?.items.length || undefined} />
        <SectionLink to="/settings/pairing" icon={Smartphone} label={t("settings.pairing")} meta={devices.data?.items.length || undefined} />
        <SectionLink to="/settings/about" icon={Info} label={t("settings.about")} />
      </ListColumn>
      <Outlet />
    </>
  );
}
