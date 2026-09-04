import { Menu as MenuPrimitive } from "@base-ui/react/menu";
import { ChevronDown, LogOut, Monitor, Moon, Sun } from "lucide-react";
import type { ReactNode } from "react";
import { getAppConfig } from "@/app/config";
import { signOut } from "@/auth/keycloak";
import { useUser } from "@/auth/user";
import { Avatar, AvatarFallback } from "@/components/Avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuPortal,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { MenuBarActions, MenuBarBrand, NavigationMenu } from "@/components/ui/navigation-menu";
import { useTheme } from "@/hooks/theme-provider";
import { isThemeMode, type ThemeMode } from "@/hooks/use-theme-preference";
import { userInitials } from "@/lib/format";
import { cn } from "@/lib/utils";

export function Topbar() {
  const { user } = useUser();
  const { themeMode, isDarkMode, setThemeMode } = useTheme();

  const displayName = user?.name || user?.email || "Account";

  // Branded logos from /config.json (frontend.branding.logoUrl / logoUrlDark),
  // falling back to the built-in Nebari logos. Dark mode prefers the dark logo,
  // then the light override, then the built-in dark logo — mirroring the
  // nebari-landing header. loadAppConfig() has already resolved before mount.
  const branding = getAppConfig();
  const logoAlt = branding?.title || "Nebari";
  const logoSrc = isDarkMode
    ? (branding?.logoUrlDark ?? branding?.logoUrl ?? "/nebari-logo_dark.svg")
    : (branding?.logoUrl ?? "/nebari-logo.svg");

  return (
    <NavigationMenu className="h-14 justify-between border-header-border bg-header-background pl-4 text-header-foreground">
      <MenuBarBrand href="/" aria-label="Go to homepage">
        <img src={logoSrc} alt={logoAlt} className="h-8 w-auto" />
      </MenuBarBrand>

      <MenuBarActions className="gap-2">
        <DropdownMenu modal={false}>
          <DropdownMenuTrigger
            variant="ghost"
            aria-label="Account menu"
            className="h-auto px-2.5 py-1 hover:bg-header-action-hover hover:no-underline focus-visible:ring-offset-0 active:bg-header-action-hover data-[popup-open]:bg-header-action-hover data-[popup-open]:no-underline"
          >
            <Avatar>
              <AvatarFallback className="bg-primary font-semibold text-primary-foreground">
                {userInitials(user?.name, user?.email)}
              </AvatarFallback>
            </Avatar>

            <span>{displayName}</span>

            <ChevronDown />
          </DropdownMenuTrigger>

          <DropdownMenuPortal>
            <DropdownMenuContent align="end" className="w-[248px] p-2">
              <div className="border-b px-1.5 pb-2">
                <p className="font-medium text-foreground text-sm">{user?.name || "Signed in"}</p>
                {user?.email ? <p className="text-muted-foreground text-xs">{user.email}</p> : null}
              </div>

              <div className="py-2">
                <MenuPrimitive.RadioGroup
                  aria-label="Theme"
                  value={themeMode}
                  onValueChange={(value) => {
                    if (isThemeMode(value)) setThemeMode(value);
                  }}
                  className="flex h-[34px] items-center gap-1 rounded-md bg-muted p-1"
                >
                  <ThemeOption value="light" label="Light mode" text="Light">
                    <Sun className="h-4 w-4" />
                  </ThemeOption>

                  <ThemeOption value="dark" label="Dark mode" text="Dark">
                    <Moon className="h-4 w-4" />
                  </ThemeOption>

                  <ThemeOption value="system" label="System theme" text="System">
                    <Monitor className="h-4 w-4" />
                  </ThemeOption>
                </MenuPrimitive.RadioGroup>
              </div>

              <DropdownMenuSeparator />

              <DropdownMenuItem
                className="leading-5 text-sign-out-foreground data-[highlighted]:text-sign-out-foreground"
                onClick={() => signOut()}
              >
                <LogOut className="size-4 shrink-0" aria-hidden="true" />
                Sign out
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenuPortal>
        </DropdownMenu>
      </MenuBarActions>
    </NavigationMenu>
  );
}

function ThemeOption({
  value,
  label,
  text,
  children,
}: {
  value: ThemeMode;
  label: string;
  text: string;
  children: ReactNode;
}): ReactNode {
  return (
    <MenuPrimitive.RadioItem
      value={value}
      aria-label={label}
      title={label}
      closeOnClick={false}
      className={cn(
        "flex h-auto flex-1 cursor-pointer items-center justify-center gap-1 rounded-sm border border-transparent px-1.5 py-0.5 font-medium text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring",
        "text-muted-foreground-strong hover:text-foreground",
        "data-checked:border-border-strong data-checked:bg-card data-checked:text-foreground data-checked:shadow-[0_1px_3px_0_rgba(0,0,0,0.10)]",
      )}
    >
      {children}
      <span>{text}</span>
    </MenuPrimitive.RadioItem>
  );
}
