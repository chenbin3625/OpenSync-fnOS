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
export async function authorizeDirectory() {
  const sdk = getHost();
  await sdk.ready();
  if (sdk.isStandaloneWeb) {
    const state = crypto.randomUUID();
    sessionStorage.setItem("opensync:auth-state", state);
    await sdk.openAppAuth(
      "pickSharedFile",
      {
        appName: "opensync",
        redirectUri: "/app/opensync/auth-callback",
        state,
      },
      { target: "_self" },
    );
  } else {
    const result = await sdk.pickSharedFile({
      title: "选择本地目录",
      okText: "确认授权",
      sidebarGroup: ["myFiles", "otherShare", "favorites"],
    });
    if (result && result.code !== 0)
      throw new Error(result.msg || "目录授权失败");
  }
}
export function handleAuthCallback() {
  const result = getHost().parseAppAuthCallback(window.location.href);
  const state = sessionStorage.getItem("opensync:auth-state");
  sessionStorage.removeItem("opensync:auth-state");
  if (!state || result.state !== state)
    throw new Error("授权回调校验失败，请重新授权");
  if (result.status === "error") throw new Error("目录授权未完成");
  window.location.replace("/app/opensync/engines?view=local");
}
