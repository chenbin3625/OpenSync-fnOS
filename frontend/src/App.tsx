import {
  createContext,
  lazy,
  Suspense,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import {
  BrowserRouter,
  Navigate,
  NavLink,
  Route,
  Routes,
  useLocation,
} from "react-router-dom";
import Banner from "@douyinfe/semi-ui/lib/es/banner";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Spin from "@douyinfe/semi-ui/lib/es/spin";
import {
  IconBell,
  IconCloud,
  IconFolder,
  IconSetting,
} from "@douyinfe/semi-icons";
import { api } from "./api/client";
import {
  applyTheme,
  connectHost,
  getHost,
  handleAuthCallback,
} from "./lib/host";
import { useResource } from "./lib/hooks";

const Tasks = lazy(() => import("./pages/Tasks"));
const Engines = lazy(() => import("./pages/Engines"));
const Notifications = lazy(() => import("./pages/Notifications"));
const Settings = lazy(() => import("./pages/Settings"));
const sections = [
  { path: "/tasks", label: "任务管理", icon: <IconCloud aria-hidden="true" /> },
  {
    path: "/engines",
    label: "引擎管理",
    icon: <IconFolder aria-hidden="true" />,
  },
  {
    path: "/notifications",
    label: "通知配置",
    icon: <IconBell aria-hidden="true" />,
  },
  {
    path: "/settings",
    label: "系统设置",
    icon: <IconSetting aria-hidden="true" />,
  },
];
export const SessionContext = createContext<{ development: boolean }>({
  development: false,
});

function Shell({ children }: { children: ReactNode }) {
  const { pathname } = useLocation();
  const [theme, setTheme] = useState<"light" | "dark">("light");
  useEffect(() => {
    applyTheme(theme);
  }, [theme]);
  useEffect(() => {
    let alive = true;
    let cleanup: (() => void) | undefined;
    void connectHost(setTheme)
      .then((fn) => {
        if (alive) cleanup = fn;
        else fn();
      })
      .catch(() => {});
    return () => {
      alive = false;
      cleanup?.();
    };
  }, []);
  useEffect(() => {
    const title =
      sections.find((s) => pathname.startsWith(s.path))?.label || "OpenSync";
    const host = getHost();
    if (!host.isStandaloneWeb)
      void host.setTitle("OpenSync · " + title).catch(() => {});
    document.title = "OpenSync · " + title;
  }, [pathname]);
  return (
    <div className="app-shell">
      <aside className="app-sidebar" aria-label="主菜单">
        <nav aria-label="主导航">
          {sections.slice(0, -1).map((section) => (
            <NavLink
              key={section.path}
              to={section.path}
              className={({ isActive }) =>
                `nav-link${isActive ? " selected" : ""}`
              }
            >
              {section.icon}
              {section.label}
            </NavLink>
          ))}
        </nav>
        <div className="sidebar-settings">
          <NavLink
            to="/settings"
            className={({ isActive }) =>
              `nav-link${isActive ? " selected" : ""}`
            }
          >
            {sections.at(-1)?.icon}
            系统设置
          </NavLink>
        </div>
      </aside>
      <main className="app-workspace">
        <Suspense
          fallback={
            <div className="state-panel">
              <Spin size="large" />
            </div>
          }
        >
          {children}
        </Suspense>
      </main>
      <nav className="mobile-navigation" aria-label="主导航">
        {sections.map((s) => (
          <NavLink
            key={s.path}
            to={s.path}
            className={({ isActive }) => (isActive ? "active" : "")}
          >
            {s.icon}
            <span>{s.label}</span>
          </NavLink>
        ))}
      </nav>
    </div>
  );
}
function AuthCallback() {
  const [error, setError] = useState("");
  useEffect(() => {
    try {
      handleAuthCallback();
    } catch (err) {
      setError(err instanceof Error ? err.message : "授权失败");
    }
  }, []);
  return (
    <Banner
      type="danger"
      description={error || "正在确认授权"}
      closeIcon={null}
    />
  );
}
function Workspace() {
  const session = useResource(() => api.session());
  useEffect(() => {
    const expired = () => void session.refresh();
    window.addEventListener("opensync:session-expired", expired);
    return () =>
      window.removeEventListener("opensync:session-expired", expired);
  }, [session.refresh]);
  if (session.error)
    return (
      <div className="session-error">
        <img src="/app/opensync/favicon.svg" alt="OpenSync" />
        <Banner type="danger" closeIcon={null} description={session.error} />
        <Button onClick={() => void session.refresh()}>重试</Button>
      </div>
    );
  if (!session.data)
    return (
      <div className="state-panel">
        <Spin size="large" />
      </div>
    );
  return (
    <SessionContext value={session.data}>
      <Shell>
        <Routes>
          <Route path="/tasks" element={<Tasks />} />
          <Route path="/engines" element={<Engines />} />
          <Route path="/notifications" element={<Notifications />} />
          <Route path="/settings" element={<Settings />} />
          <Route path="/auth-callback" element={<AuthCallback />} />
          <Route path="*" element={<Navigate to="/tasks" replace />} />
        </Routes>
      </Shell>
    </SessionContext>
  );
}
export function App() {
  return (
    <BrowserRouter basename="/app/opensync">
      <Workspace />
    </BrowserRouter>
  );
}
