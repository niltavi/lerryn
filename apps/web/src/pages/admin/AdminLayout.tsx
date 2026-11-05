import { useI18n } from "@lerryn/core";
import { Outlet } from "@tanstack/react-router";
import { LogIn, ScrollText, Server, Users } from "lucide-react";

import { ListColumn, SectionLink } from "../../shell/layout";

/** Admin settings, visible to platform admins only (the route answers not found otherwise). */
export function AdminLayout() {
  const { t } = useI18n();
  return (
    <>
      <ListColumn title={t("admin.title")} label={t("admin.title")}>
        <SectionLink to="/admin/users" icon={Users} label={t("admin.users")} />
        <SectionLink to="/admin/login" icon={LogIn} label={t("admin.login")} />
        <SectionLink to="/admin/runners" icon={Server} label={t("admin.runners")} />
        <SectionLink to="/admin/audit" icon={ScrollText} label={t("admin.audit")} />
      </ListColumn>
      <Outlet />
    </>
  );
}
