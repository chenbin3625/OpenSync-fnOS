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
