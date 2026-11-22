import { useI18n, useLogout, type Me } from "@lerryn/core";
import { Avatar, Button, Dropdown, Header, Kbd, Label, ListBox, Modal, SearchField, Separator, useLerrynTheme, type ThemePreference } from "@lerryn/ui-web";
import { ArrowRightFromSquare, ChevronsLeft, ChevronsRight, Globe, Magnifier, Moon } from "@gravity-ui/icons";
import { Link, useNavigate } from "@tanstack/react-router";
import { useEffect, useState, type ComponentType, type ReactNode, type SVGProps } from "react";

export type NavIcon = ComponentType<SVGProps<SVGSVGElement>>;

export interface NavItem {
  id: string;
  to: string;
  label: string;
  /** Accessible name when it should say more than the label, e.g. a pending count. */
  name?: string;
  icon: NavIcon;
  badge?: ReactNode;
  /** Draws the badge as something to act on. */
  attention?: boolean;
}

export interface NavGroup {
  id: string;
  label?: string;
  items: NavItem[];
}

/** Initial for the avatar: the first character of the display name or username. */
export function initialOf(user: { display_name?: string | null; username: string }): string {
  return Array.from(user.display_name?.trim() || user.username)[0] ?? "?";
}

/**
 * The console sidebar: grouped destinations, the account menu and a collapse
 * control. Collapsed, it keeps the icons and each link's accessible name.
 */
export function AppSidebar({
  groups,
  active,
  collapsed,
  onCollapsedChange,
  label,
  footer,
}: {
  groups: NavGroup[];
  active: string | undefined;
  collapsed: boolean;
  onCollapsedChange: (collapsed: boolean) => void;
  label: string;
  footer: ReactNode;
}) {
  const { t } = useI18n();
  return (
    <aside className="nt-side" data-collapsed={collapsed || undefined}>
      <nav className="nt-side__nav" aria-label={label}>
        {groups.map((group) => (
          <div key={group.id} className="nt-side__group" role="group" aria-label={group.label}>
            {group.label ? (
              <span className="nt-side__heading" aria-hidden="true">
                {group.label}
              </span>
            ) : null}
            <ul>
              {group.items.map((item) => (
                <li key={item.id}>
                  <Link
                    to={item.to}
                    className="nt-side__item nt-press"
                    activeProps={{}}
                    // Collapsed, the label is hidden, so the link is named explicitly.
                    aria-label={item.name ?? (collapsed ? item.label : undefined)}
                    aria-current={active === item.id ? "page" : undefined}
                    title={collapsed ? item.label : undefined}
                    data-pip={item.attention || undefined}
                  >
                    <item.icon aria-hidden="true" width={16} height={16} />
                    <span className="nt-side__label">{item.label}</span>
                    {item.badge !== undefined && item.badge !== null ? (
                      <span className="nt-side__badge" data-tone={item.attention ? "warning" : undefined}>
                        {item.badge}
                      </span>
                    ) : null}
                  </Link>
                </li>
              ))}
            </ul>
          </div>
        ))}
      </nav>
      <div className="nt-side__footer">
        {footer}
        <Button
          isIconOnly
          size="sm"
          variant="ghost"
          className="nt-side__collapse"
          aria-label={collapsed ? t("nav.expand") : t("nav.collapse")}
          onPress={() => onCollapsedChange(!collapsed)}
        >
          {collapsed ? <ChevronsRight aria-hidden="true" width={16} height={16} /> : <ChevronsLeft aria-hidden="true" width={16} height={16} />}
        </Button>
      </div>
    </aside>
  );
}

/** The signed-in user's menu: the account pages, appearance, language and sign-out. */
export function UserMenu({ me, showName, pages, active }: { me: Me; showName: boolean; pages: NavItem[]; active: string | undefined }) {
  const { t, locale, setLocale } = useI18n();
  const { preference, setPreference } = useLerrynTheme();
  const navigate = useNavigate();
  const logout = useLogout();
  const name = me.display_name || me.username;
  const themes: { key: ThemePreference; label: string }[] = [
    { key: "system", label: t("common.themeSystem") },
    { key: "light", label: t("common.themeLight") },
    { key: "dark", label: t("common.themeDark") },
  ];
  const avatar = (size: "sm" | "md") => (
    <Avatar size={size} color="accent">
      <Avatar.Fallback>{initialOf(me)}</Avatar.Fallback>
    </Avatar>
  );
  return (
    <Dropdown>
      <Button variant="ghost" className="nt-user" aria-label={showName ? undefined : t("nav.accountMenu", { name })} isIconOnly={!showName}>
        {avatar("sm")}
        {showName ? (
          <span className="nt-ws__text">
            <strong>{name}</strong>
            <span>{me.username}</span>
          </span>
        ) : null}
      </Button>
      <Dropdown.Popover placement="top start" className="min-w-60">
        <div className="nt-user__card">
          {avatar("md")}
          <div>
            <strong>{name}</strong>
            <span>{me.username}</span>
          </div>
        </div>
        <Dropdown.Menu
          aria-label={t("settings.account")}
          onAction={(key) => {
            const page = pages.find((item) => item.id === key);
            if (page) void navigate({ to: page.to });
            else if (key === "language") setLocale(locale === "zh-CN" ? "en" : "zh-CN");
            else if (key === "signout") logout.mutate(undefined, { onSettled: () => void navigate({ to: "/login", replace: true }) });
          }}
        >
          <Dropdown.Section>
            {pages.map((page) => (
              <Dropdown.Item key={page.id} id={page.id} textValue={page.label} aria-current={active === page.id ? "page" : undefined}>
                <page.icon aria-hidden="true" width={14} height={14} />
                <Label>{page.label}</Label>
                {page.badge !== undefined && page.badge !== null ? <span className="nt-side__badge ms-auto">{page.badge}</span> : null}
              </Dropdown.Item>
            ))}
          </Dropdown.Section>
          <Separator />
          <Dropdown.Section>
            <Dropdown.SubmenuTrigger>
              <Dropdown.Item id="theme" textValue={t("common.theme")}>
                <Moon aria-hidden="true" width={14} height={14} />
                <Label>{t("common.theme")}</Label>
                <Dropdown.SubmenuIndicator />
              </Dropdown.Item>
              <Dropdown.Popover>
                <Dropdown.Menu
                  aria-label={t("common.theme")}
                  selectionMode="single"
                  disallowEmptySelection
                  selectedKeys={[preference]}
                  onAction={(key) => setPreference(key as ThemePreference)}
                >
                  {themes.map((theme) => (
                    <Dropdown.Item key={theme.key} id={theme.key} textValue={theme.label}>
                      <Label>{theme.label}</Label>
                      <Dropdown.ItemIndicator />
                    </Dropdown.Item>
                  ))}
                </Dropdown.Menu>
              </Dropdown.Popover>
            </Dropdown.SubmenuTrigger>
            <Dropdown.Item id="language" textValue={t("common.switchLanguage")}>
              <Globe aria-hidden="true" width={14} height={14} />
              <Label>{t("common.switchLanguage")}</Label>
            </Dropdown.Item>
          </Dropdown.Section>
          <Separator />
          <Dropdown.Item id="signout" textValue={t("auth.logout")} variant="danger">
            <ArrowRightFromSquare aria-hidden="true" width={14} height={14} />
            <Label>{t("auth.logout")}</Label>
          </Dropdown.Item>
        </Dropdown.Menu>
      </Dropdown.Popover>
    </Dropdown>
  );
}

export interface Command {
  id: string;
  label: string;
  group: string;
  icon: NavIcon;
  run: () => void;
}

/** Opens the command menu; the shortcut is shown where there is room. */
export function CommandTrigger({ onPress }: { onPress: () => void }) {
  const { t } = useI18n();
  return (
    <Button variant="secondary" size="sm" className="nt-cmd-trigger" onPress={onPress}>
      <Magnifier aria-hidden="true" width={14} height={14} />
      <span>{t("nav.jump")}</span>
      <Kbd variant="light" className="ms-auto">
        <Kbd.Abbr keyValue="command" />
        <Kbd.Content>K</Kbd.Content>
      </Kbd>
    </Button>
  );
}

/** A filterable list of destinations and actions, opened with ⌘K or Ctrl+K. */
export function CommandMenu({ isOpen, onOpenChange, commands }: { isOpen: boolean; onOpenChange: (open: boolean) => void; commands: Command[] }) {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        onOpenChange(!isOpen);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [isOpen, onOpenChange]);
  useEffect(() => {
    if (!isOpen) setQuery("");
  }, [isOpen]);
  const q = query.trim().toLowerCase();
  const visible = commands.filter((command) => !q || command.label.toLowerCase().includes(q));
  const groups = [...new Set(visible.map((command) => command.group))];
  return (
    <Modal.Backdrop isOpen={isOpen} onOpenChange={onOpenChange}>
      <Modal.Container placement="top">
        <Modal.Dialog className="nt-cmd sm:max-w-[520px]" aria-label={t("nav.commandMenu")}>
          <SearchField aria-label={t("nav.commandMenu")} value={query} onChange={setQuery} autoFocus className="nt-cmd__search">
            <SearchField.Group>
              <SearchField.SearchIcon />
              <SearchField.Input placeholder={t("nav.jumpPlaceholder")} />
            </SearchField.Group>
          </SearchField>
          {visible.length === 0 ? (
            <p className="tpl-empty">{t("nav.noResults")}</p>
          ) : (
            <ListBox
              aria-label={t("nav.commandMenu")}
              className="nt-cmd__list"
              onAction={(key) => {
                onOpenChange(false);
                commands.find((command) => command.id === key)?.run();
              }}
            >
              {groups.map((group) => (
                <ListBox.Section key={group}>
                  <Header>{group}</Header>
                  {visible
                    .filter((command) => command.group === group)
                    .map((command) => (
                      <ListBox.Item key={command.id} id={command.id} textValue={command.label}>
                        <command.icon aria-hidden="true" width={14} height={14} />
                        <span>{command.label}</span>
                      </ListBox.Item>
                    ))}
                </ListBox.Section>
              ))}
            </ListBox>
          )}
        </Modal.Dialog>
      </Modal.Container>
    </Modal.Backdrop>
  );
}
