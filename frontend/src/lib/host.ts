import { TrimApp } from "@trimjs/web-app";

let instance: TrimApp | undefined;
export function getHost() {
  return (instance ??= new TrimApp());
}
export function applyTheme(theme: "light" | "dark") {
  if (theme === "dark") document.body.setAttribute("theme-mode", "dark");
  else document.body.removeAttribute("theme-mode");
  document.documentElement.dataset.theme = theme;
}
/**
 * Opens an external link. Inside the fnOS shell the host handles it (system
 * browser on mobile, new tab on desktop); a plain browser or a broken bridge
 * falls back to window.open so the link still works.
 */
export function safeExternalURL(raw?: string | null): string | null {
  const trimmed = typeof raw === "string" ? raw.trim() : "";
  if (!trimmed) return null;
  try {
    const parsed = new URL(trimmed);
    if (
      (parsed.protocol !== "http:" && parsed.protocol !== "https:") ||
      !parsed.hostname
    ) {
      return null;
    }
    return parsed.toString();
  } catch {
    return null;
  }
}

export async function openExternalURL(url: string) {
  const safe = safeExternalURL(url);
  if (!safe) return;
  const host = getHost();
  // isStandaloneWeb is known synchronously (set in the TrimApp constructor), so
  // a plain browser can open the tab inside the click gesture instead of after
  // an await, where popup blockers would reject it.
  if (host.isStandaloneWeb) {
    window.open(safe, "_blank", "noopener,noreferrer");
    return;
  }
  try {
    await host.ready();
    await host.openURL(safe);
  } catch {
    window.open(safe, "_blank", "noopener,noreferrer");
  }
}

export async function connectHost(onTheme: (theme: "light" | "dark") => void) {
  const sdk = getHost();
  await sdk.ready();
  if (sdk.isStandaloneWeb) return () => {};
  const config = await sdk.getPlatformConfig();
  onTheme(config.theme === "dark" ? "dark" : "light");
  document.documentElement.lang = config.language || "zh-CN";
  const themeListener = (theme: "light" | "dark") => onTheme(theme);
  const languageListener = (language: string) => {
    document.documentElement.lang = language;
  };
  if (sdk.isWeb && !sdk.isStandaloneWeb) {
    await sdk.$on("os/theme", themeListener);
    await sdk.$on("os/language", languageListener);
    return () => {
      void sdk.$off("os/theme", themeListener);
      void sdk.$off("os/language", languageListener);
    };
  }
  return () => {};
}
