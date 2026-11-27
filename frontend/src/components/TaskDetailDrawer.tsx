import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import Button from "@douyinfe/semi-ui/lib/es/button";
import SideSheet from "@douyinfe/semi-ui/lib/es/sideSheet";
import Tabs from "@douyinfe/semi-ui/lib/es/tabs";
import { IconExternalOpen } from "@douyinfe/semi-icons";
import { useMediaQuery } from "../lib/hooks";
import type { TaskTab } from "../lib/taskForm";

interface TaskDetailDrawerProps {
  title: string;
  tab: TaskTab;
  onTabChange: (tab: TaskTab) => void;
  fullPageHref: string;
  onClose: () => void;
  children: ReactNode;
}

export function TaskDetailDrawer({
  title,
  tab,
  onTabChange,
  fullPageHref,
  onClose,
  children,
}: TaskDetailDrawerProps) {
  const isMobile = useMediaQuery("(max-width: 720px)");

  return (
    <SideSheet
      visible
      placement="right"
      width={isMobile ? "100%" : 740}
      zIndex={999 /* Semi modals use 1000 and can open alongside the drawer. */}
      className="task-detail-drawer"
      title={
        <div className="task-drawer-title">
          <span className="task-drawer-title__name">{title}</span>
          <Link to={fullPageHref} className="task-drawer-fullpage-link">
            <Button
              size="small"
              theme="borderless"
              icon={<IconExternalOpen aria-hidden="true" />}
              iconPosition="right"
            >
              完整页面打开
            </Button>
          </Link>
        </div>
      }
      onCancel={onClose}
    >
      <div className="task-drawer-body">
        <Tabs activeKey={tab} onChange={(key) => onTabChange(key as TaskTab)} keepDOM={false}>
          <Tabs.TabPane tab="任务概览" itemKey="overview" />
          <Tabs.TabPane tab="同步记录" itemKey="records" />
        </Tabs>
        <div className="task-drawer-content">{children}</div>
      </div>
    </SideSheet>
  );
}
