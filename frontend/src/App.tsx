import {
  createContext,
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useMemo,
  useRef,
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
  useNavigate,
} from "react-router-dom";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Spin from "@douyinfe/semi-ui/lib/es/spin";
import {
  IconBellStroked,
  IconCloudStroked,
  IconFolderStroked,
  IconServerStroked,
  IconSettingStroked,
  IconTreeTriangleDown,
  IconTreeTriangleRight,
} from "@douyinfe/semi-icons";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import { api, sessionExpiredEvent } from "./api/client";
import { errorToast } from "./components/common";
import { VersionContext, VersionLink } from "./components/VersionLink";
import {
  notifyUpdateAvailable,
  updateNoticeKey,
} from "./components/updateNotice";
import { applyTheme, connectHost } from "./lib/host";
import { useResource } from "./lib/hooks";
import { createGuardedRefresh } from "./lib/sessionGuard";
import { getJobName } from "./pages/Home/homeUtils";

const Tasks = lazy(() => import("./pages/Tasks"));
const Engines = lazy(() => import("./pages/Engines"));
const Notifications = lazy(() => import("./pages/Notifications"));
const Settings = lazy(() => import("./pages/Settings"));
const sections = [
  {
    path: "/tasks",
    label: "任务管理",
    icon: <IconCloudStroked aria-hidden="true" />,
  },
  {
    path: "/engines",
    label: "引擎管理",
    icon: <IconServerStroked aria-hidden="true" />,
  },
  {
    path: "/notifications",
    label: "通知配置",
    icon: <IconBellStroked aria-hidden="true" />,
  },
  {
    path: "/settings",
    label: "设置",
    icon: <IconSettingStroked aria-hidden="true" />,
  },
];
export const SessionContext = createContext<{ development: boolean }>({
  development: false,
});

function Shell({ children, version }: { children: ReactNode; version: string }) {
  const { pathname, search } = useLocation();
  const navigate = useNavigate();
  const [theme, setTheme] = useState<"light" | "dark">("light");
  const [taskMenuOpen, setTaskMenuOpen] = useState(false);
  const [taskMenuTouched, setTaskMenuTouched] = useState(false);
  const taskMenu = useResource((signal) => api.jobMenu(signal));
  const latest = useResource((signal) => api.latestVersion(signal));
  const [checking, setChecking] = useState(false);
  const lastVersionCheck = useRef(Date.now());
  const notifiedRelease = useRef<string | null>(null);
  const taskItems = taskMenu.data?.dataList || [];
  const hasTaskItems = taskItems.length > 0;
  const currentTaskId = new URLSearchParams(search).get("jobId");
  const taskHref = (jobId: number) => {
    const next = new URLSearchParams(pathname.startsWith("/tasks") ? search : "");
    next.set("jobId", String(jobId));
    return `/tasks?${next.toString()}`;
  };
  useEffect(() => {
    if (!hasTaskItems) {
      setTaskMenuOpen(false);
      return;
    }
    if (!taskMenuTouched) setTaskMenuOpen(true);
  }, [hasTaskItems, taskMenuTouched]);
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
    const refresh = () => void taskMenu.refresh();
    window.addEventListener("opensync:jobs-changed", refresh);
    return () => window.removeEventListener("opensync:jobs-changed", refresh);
  }, [taskMenu.refresh]);
  useEffect(() => {
    // 后端成功结果缓存 30 分钟（backend/cmd/server/version_checker.go），前端按
    // 同一节奏在回到前台时刷新：既不漏掉新版本，也不会每次切标签页都请求。
    const intervalMs = 30 * 60 * 1000;
    const refresh = () => {
      if (
        document.visibilityState !== "visible" ||
        Date.now() - lastVersionCheck.current < intervalMs
      )
        return;
      lastVersionCheck.current = Date.now();
      void latest.refresh(true);
    };
    const interval = window.setInterval(refresh, intervalMs);
    document.addEventListener("visibilitychange", refresh);
    return () => {
      window.clearInterval(interval);
      document.removeEventListener("visibilitychange", refresh);
    };
  }, [latest.refresh]);
  useEffect(() => {
    const info = latest.data;
    if (!info?.hasUpdate || notifiedRelease.current === info.latestVersion)
      return;
    notifiedRelease.current = info.latestVersion;
    // 同一次浏览器会话里同一个版本只提醒一次，刷新页面不再重复弹窗。
    try {
      if (sessionStorage.getItem(updateNoticeKey) === info.latestVersion) return;
      sessionStorage.setItem(updateNoticeKey, info.latestVersion);
    } catch {
      // 存储被禁用（隐私模式 / 受限 WebView）时退化为每次加载提醒一次。
    }
    notifyUpdateAvailable(version, info);
  }, [latest.data, version]);
  const checkUpdate = useCallback(async () => {
    if (checking) return;
    setChecking(true);
    try {
      // refresh=1：用户点了按钮就要真实结果，绕过后端 30 分钟的缓存。
      const info = await api.latestVersion(undefined, true);
      latest.setData(info);
      if (info.hasUpdate) {
        notifiedRelease.current = info.latestVersion;
        notifyUpdateAvailable(version, info);
      } else {
        Toast.success(`已是最新版本 ${info.latestVersion}`);
      }
    } catch (err) {
      errorToast(err, "检查更新失败");
    } finally {
      setChecking(false);
    }
  }, [checking, latest.setData, version]);
  const tasksSelected = pathname.startsWith("/tasks") && !hasTaskItems;
  return (
    <VersionContext
      value={{
        currentVersion: version,
        latest: latest.data,
        checking,
        checkUpdate,
      }}
    >
      <div className="app-shell">
        <aside className="app-sidebar" aria-label="主菜单">
          <nav aria-label="主导航">
            {hasTaskItems ? (
              <button
                type="button"
                className={`nav-link nav-menu-toggle${tasksSelected ? " selected" : ""}`}
                aria-expanded={taskMenuOpen}
                aria-controls="task-menu"
                onClick={() => {
                  setTaskMenuTouched(true);
                  if (!pathname.startsWith("/tasks")) {
                    setTaskMenuOpen(true);
                    navigate(taskHref(taskItems[0].id));
                    return;
                  }
                  setTaskMenuOpen((open) => !open);
                }}
              >
                {taskMenuOpen ? (
                  <IconTreeTriangleDown
                    className="task-menu-triangle"
                    aria-hidden="true"
                  />
                ) : (
                  <IconTreeTriangleRight
                    className="task-menu-triangle"
                    aria-hidden="true"
                  />
                )}
                <span className="nav-label">任务管理</span>
              </button>
            ) : (
              <NavLink
                to="/tasks"
                className={({ isActive }) =>
                  `nav-link${isActive ? " selected" : ""}`
                }
              >
                {sections[0].icon}
                <span className="nav-label">任务管理</span>
              </NavLink>
            )}
            {hasTaskItems && taskMenuOpen && (
              <div className="nav-submenu" id="task-menu">
                {taskItems.map((job) => (
                  <NavLink
                    key={job.id}
                    to={taskHref(job.id)}
                    className={`task-sub-link${currentTaskId === String(job.id) ? " selected" : ""}`}
                    title={getJobName(job)}
                  >
                    <IconFolderStroked
                      className="task-sub-icon"
                      aria-hidden="true"
                    />
                    {getJobName(job)}
                  </NavLink>
                ))}
              </div>
            )}
            {sections.slice(1, -1).map((section) => (
              <NavLink
                key={section.path}
                to={section.path}
                className={({ isActive }) =>
                  `nav-link${isActive ? " selected" : ""}`
                }
              >
                {section.icon}
                <span className="nav-label">{section.label}</span>
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
              <span className="nav-label">设置</span>
            </NavLink>
            <VersionLink />
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
    </VersionContext>
  );
}
function Workspace() {
  const session = useResource(() => api.session());
  const refreshSession = session.refresh;
  // Parallel requests that all hit 401 each dispatch the event; the guard
  // collapses them into one /session check instead of aborting and restarting
  // it once per failure.
  const expiredRefresh = useMemo(
    () => createGuardedRefresh(() => refreshSession()),
    [refreshSession],
  );
  useEffect(() => {
    const expired = () => void expiredRefresh();
    window.addEventListener(sessionExpiredEvent, expired);
    return () => window.removeEventListener(sessionExpiredEvent, expired);
  }, [expiredRefresh]);
  if (session.error)
    return (
      <div className="session-error">
        <img src="/app/opensync/favicon.svg" alt="OpenSync" />
        <p className="inline-error">{session.error}</p>
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
      <Shell version={session.data.version}>
        <Routes>
          <Route path="/tasks" element={<Tasks />} />
          <Route path="/engines" element={<Engines />} />
          <Route path="/notifications" element={<Notifications />} />
          <Route path="/settings" element={<Settings />} />
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
