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
  Link,
  Navigate,
  NavLink,
  Route,
  Routes,
  useLocation,
  useNavigate,
} from "react-router-dom";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconAlertTriangle,
  IconApps,
  IconBellStroked,
  IconCloudStroked,
  IconFolderStroked,
  IconRefresh,
  IconServerStroked,
  IconSettingStroked,
  IconTreeTriangleDown,
  IconTreeTriangleRight,
} from "@douyinfe/semi-icons";
import { api, sessionExpiredEvent } from "./api/client";
import { errorToast, InlineNotice, LoadingPanel } from "./components/common";
import { VersionContext, VersionLink } from "./components/VersionLink";
import {
  notifyUpdateAvailable,
  updateNoticeKey,
} from "./components/updateNotice";
import { applyTheme, connectHost } from "./lib/host";
import { useResource } from "./lib/hooks";
import { createGuardedRefresh } from "./lib/sessionGuard";
import { buildTaskSearchParams } from "./lib/taskForm";
import { getJobName } from "./pages/Home/homeUtils";
import type { JobItem } from "./types";

const Dashboard = lazy(() => import("./pages/Dashboard"));
const Tasks = lazy(() => import("./pages/Tasks"));
const Engines = lazy(() => import("./pages/Engines"));
const Notifications = lazy(() => import("./pages/Notifications"));
const Settings = lazy(() => import("./pages/Settings"));
const sections = [
  {
    path: "/dashboard",
    label: "总览看板",
    icon: <IconApps aria-hidden="true" />,
  },
  {
    path: "/tasks",
    label: "任务管理",
    icon: <IconCloudStroked aria-hidden="true" />,
  },
  {
    path: "/engines",
    label: "存储管理",
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

function jobStatusDotClass(job: JobItem): string {
  if (job.legacyOpenList) return "job-dot job-dot--error";
  if (job.pendingApproval) return "job-dot job-dot--approval";
  if (job.running && job.paused) return "job-dot job-dot--paused";
  if (job.running) return "job-dot job-dot--running";
  if (job.queued) return "job-dot job-dot--queued";
  if (job.lastStatus && [3, 5, 6, 7].includes(job.lastStatus))
    return "job-dot job-dot--error";
  if (job.enable !== 1 && job.isCron !== 2) return "job-dot job-dot--disabled";
  return "";
}

function Shell({ children, version }: { children: ReactNode; version: string }) {
  const { pathname, search } = useLocation();
  const navigate = useNavigate();
  const [theme, setTheme] = useState<"light" | "dark">("light");
  const [taskMenuOpen, setTaskMenuOpen] = useState(false);
  const [taskMenuTouched, setTaskMenuTouched] = useState(false);
  const taskMenu = useResource((signal) => api.jobMenu(signal));
  const latest = useResource((signal) => api.latestVersion(signal));
  const [checking, setChecking] = useState(false);
  const checkInFlight = useRef<AbortController | null>(null);
  const lastVersionCheck = useRef(Date.now());
  const notifiedRelease = useRef<string | null>(null);
  const taskItems = taskMenu.data?.dataList || [];
  const hasTaskItems = taskItems.length > 0;
  const pendingApprovalJobs = useMemo(
    () => taskItems.filter((job) => Boolean(job.pendingApproval)),
    [taskItems],
  );
  const rawJobId = new URLSearchParams(search).get("jobId");
  const currentTaskId = rawJobId && /^\d+$/.test(rawJobId) ? rawJobId : null;
  // Tasks.selectJob() falls back to the first job when the URL has no jobId, so
  // the sidebar must highlight the same row instead of showing nothing selected.
  const activeTaskId =
    currentTaskId ??
    (pathname.startsWith("/tasks") && taskItems[0]
      ? String(taskItems[0].id)
      : null);
  const taskHref = (job: Pick<JobItem, "id" | "running">) => {
    const next = buildTaskSearchParams(job, {
      onTasksPage: pathname.startsWith("/tasks"),
      currentSearch: search,
      activeJobId: activeTaskId,
    });
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
    // Poll job states gently while visible so sidebar status dots and global
    // pending-approval alerts stay current across all pages.
    const interval = window.setInterval(() => {
      if (document.visibilityState === "visible") {
        void taskMenu.refresh(true);
      }
    }, 15_000);
    let lastRefresh = Date.now();
    const refresh = () => {
      if (document.visibilityState !== "visible") return;
      if (Date.now() - lastRefresh < 10_000) return;
      lastRefresh = Date.now();
      void taskMenu.refresh(true);
    };
    document.addEventListener("visibilitychange", refresh);
    return () => {
      window.clearInterval(interval);
      document.removeEventListener("visibilitychange", refresh);
    };
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
    // A ref, not the `checking` state: two clicks in the same render both saw
    // checking=false and fired two upstream requests.
    if (checkInFlight.current) return;
    const controller = new AbortController();
    checkInFlight.current = controller;
    setChecking(true);
    try {
      // refresh=1：用户点了按钮就要真实结果，绕过后端 30 分钟的缓存。
      const info = await api.latestVersion(controller.signal, true);
      if (controller.signal.aborted) return;
      latest.setData(info);
      if (info.hasUpdate) {
        notifiedRelease.current = info.latestVersion;
        notifyUpdateAvailable(version, info);
      } else {
        Toast.success(`已是最新版本 ${info.latestVersion}`);
      }
    } catch (err) {
      if (!controller.signal.aborted) errorToast(err, "检查更新失败");
    } finally {
      checkInFlight.current = null;
      if (!controller.signal.aborted) setChecking(false);
    }
  }, [latest.setData, version]);
  useEffect(() => () => checkInFlight.current?.abort(), []);
  const tasksSelected = pathname.startsWith("/tasks") && !hasTaskItems;
  const showGlobalApprovalStrip =
    pendingApprovalJobs.length > 0 &&
    !pathname.startsWith("/dashboard") &&
    !(
      pathname.startsWith("/tasks") &&
      pendingApprovalJobs.some((j) => String(j.id) === activeTaskId)
    );
  return (
    <VersionContext
      value={{
        currentVersion: version,
        latest: latest.data,
        latestError: latest.error,
        checking,
        checkUpdate,
      }}
    >
      <div className="app-shell">
        <aside className="app-sidebar" aria-label="主菜单">
          <nav aria-label="主导航">
            <NavLink
              to="/dashboard"
              className={({ isActive }) =>
                `nav-link${isActive ? " selected" : ""}`
              }
            >
              {sections[0].icon}
              <span className="nav-label">{sections[0].label}</span>
              {pendingApprovalJobs.length > 0 && (
                <span
                  className="nav-badge"
                  aria-label={`${pendingApprovalJobs.length} 个待审批`}
                >
                  {pendingApprovalJobs.length}
                </span>
              )}
            </NavLink>
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
                    navigate(taskHref(taskItems[0]));
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
                {sections[1].icon}
                <span className="nav-label">任务管理</span>
              </NavLink>
            )}
            {taskMenu.error && (
              <InlineNotice
                className="sidebar-task-error"
                message={taskMenu.error}
                retrying={taskMenu.loading}
                onRetry={() => void taskMenu.refresh()}
              />
            )}
            {hasTaskItems && taskMenuOpen && (
              <div className="nav-submenu" id="task-menu">
                {taskItems.map((job) => {
                  const dotClass = jobStatusDotClass(job);
                  return (
                    // A plain Link: NavLink ignores the query string, so every
                    // /tasks?jobId=… entry was marked aria-current="page".
                    <Link
                      key={job.id}
                      to={taskHref(job)}
                      className={`task-sub-link${activeTaskId === String(job.id) ? " selected" : ""}`}
                      aria-current={
                        activeTaskId === String(job.id) ? "page" : undefined
                      }
                      title={getJobName(job)}
                    >
                      <IconFolderStroked
                        className="task-sub-icon"
                        aria-hidden="true"
                      />
                      <span className="task-sub-name">{getJobName(job)}</span>
                      {dotClass && <span className={dotClass} aria-hidden="true" />}
                    </Link>
                  );
                })}
              </div>
            )}
            {sections.slice(2, -1).map((section) => (
              <NavLink
                key={section.path}
                to={section.path}
                className={({ isActive }) =>
                  `nav-link${isActive || (section.path === "/engines" && pathname.startsWith("/storages")) ? " selected" : ""}`
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
              {sections[sections.length - 1]?.icon}
              <span className="nav-label">设置</span>
            </NavLink>
            <VersionLink />
          </div>
        </aside>
        <main className="app-workspace">
          {showGlobalApprovalStrip && (
            <div className="global-approval-strip" role="alert">
              <IconAlertTriangle aria-hidden="true" size="small" />
              <span>
                有 {pendingApprovalJobs.length} 个全量同步任务（
                {getJobName(pendingApprovalJobs[0])}
                {pendingApprovalJobs.length > 1 ? " 等" : ""}）存在待确认删除项（不影响同步运行）
              </span>
              <Button
                size="small"
                type="danger"
                theme="solid"
                onClick={() =>
                  navigate(
                    `/tasks?jobId=${pendingApprovalJobs[0].id}&tab=overview&approval=1`,
                  )
                }
              >
                查看并审批
              </Button>
            </div>
          )}
          <Suspense fallback={<LoadingPanel />}>{children}</Suspense>
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
  const session = useResource((signal) => api.session(signal));
  const refreshSession = session.refresh;
  const [retrying, setRetrying] = useState(false);
  // Parallel requests that all hit 401 each dispatch the event; the guard
  // collapses them into one /session check instead of aborting and restarting
  // it once per failure.
  const expiredRefresh = useMemo(
    () => createGuardedRefresh(() => refreshSession(true)),
    [refreshSession],
  );
  useEffect(() => {
    const expired = () => void expiredRefresh();
    window.addEventListener(sessionExpiredEvent, expired);
    return () => window.removeEventListener(sessionExpiredEvent, expired);
  }, [expiredRefresh]);
  const retrySession = useCallback(async () => {
    if (retrying) return;
    setRetrying(true);
    try {
      // Keep the error screen visible until /session actually succeeds,
      // preventing stale session.data from briefly mounting <Shell> and firing 401s.
      await refreshSession(true);
    } finally {
      setRetrying(false);
    }
  }, [retrying, refreshSession]);
  if (session.error)
    return (
      <div className="session-error">
        <img src="/app/opensync/favicon.svg" alt="OpenSync" />
        <p className="inline-error">{session.error}</p>
        <Button
          icon={<IconRefresh aria-hidden="true" />}
          loading={retrying}
          onClick={() => void retrySession()}
        >
          重试
        </Button>
      </div>
    );
  if (!session.data) return <LoadingPanel />;
  return (
    <SessionContext value={session.data}>
      <Shell version={session.data.version}>
        <Routes>
          <Route path="/dashboard" element={<Dashboard />} />
          <Route path="/tasks" element={<Tasks />} />
          <Route path="/engines" element={<Engines />} />
          <Route path="/storages" element={<Engines />} />
          <Route path="/notifications" element={<Notifications />} />
          <Route path="/settings" element={<Settings />} />
          <Route path="*" element={<Navigate to="/dashboard" replace />} />
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
