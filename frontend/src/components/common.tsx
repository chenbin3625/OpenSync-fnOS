import {
  cloneElement,
  isValidElement,
  useEffect,
  useId,
  type ReactNode,
} from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Dropdown from "@douyinfe/semi-ui/lib/es/dropdown";
import Empty from "@douyinfe/semi-ui/lib/es/empty";
import Modal from "@douyinfe/semi-ui/lib/es/modal";
import Spin from "@douyinfe/semi-ui/lib/es/spin";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import { actionSkipped } from "../lib/hooks";
import Tooltip from "@douyinfe/semi-ui/lib/es/tooltip";
import { IconAlertTriangle, IconRefresh, IconCrossStroked, IconMoreStroked, IconHelpCircleStroked } from "@douyinfe/semi-icons";
import { getHost } from "../lib/host";

export function getErrorMessage(error: unknown, fallback = "操作失败"): string {
  return error instanceof Error ? error.message : fallback;
}

export function errorToast(error: unknown, fallback = "操作失败") {
  Toast.error(getErrorMessage(error, fallback));
}
export function Header({
  title,
  tabs,
  actions,
}: {
  title: string;
  tabs?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <header className="page-toolbar" aria-label={`${title}操作`}>
      {tabs ? <div className="page-tabs">{tabs}</div> : <h1 className="page-title">{title}</h1>}
      <div className="page-actions">{actions}</div>
    </header>
  );
}
export function IconButton({
  label,
  icon,
  onClick,
  disabled,
  danger,
}: {
  label: string;
  icon: ReactNode;
  onClick: () => void;
  disabled?: boolean;
  danger?: boolean;
}) {
  return (
    <Tooltip content={label} trigger="hover" disableFocusListener>
      <Button
        aria-label={label}
        icon={icon}
        theme="borderless"
        type={danger ? "danger" : "tertiary"}
        disabled={disabled}
        onClick={onClick}
      />
    </Tooltip>
  );
}
export interface MenuAction {
  label: string;
  icon?: ReactNode;
  onClick: () => void;
  danger?: boolean;
  disabled?: boolean;
}
/** 卡片右上角的「⋯」操作菜单，与参考稿的卡片操作入口保持一致。 */
export function ActionMenu({
  actions,
  disabled,
  label = "更多操作",
}: {
  actions: MenuAction[];
  disabled?: boolean;
  label?: string;
}) {
  return (
    <Dropdown
      trigger="click"
      position="bottomRight"
      menu={actions.map((action) => ({
        node: "item" as const,
        name: action.label,
        icon: action.icon,
        type: action.danger ? ("danger" as const) : ("tertiary" as const),
        disabled: action.disabled,
        onClick: action.onClick,
      }))}
    >
      <Button
        title={label}
        aria-label={label}
        icon={<IconMoreStroked aria-hidden="true" />}
        theme="borderless"
        type="tertiary"
        disabled={disabled}
      />
    </Dropdown>
  );
}
export function EmptyState() {
  return (
    <div className="fnos-empty" role="status" aria-label="空空如也">
      <Empty
        image={<img src="/app/opensync/fnos-empty.png" alt="" />}
        description="空空如也"
      />
    </div>
  );
}
export function LoadState({
  loading,
  error,
  retry,
  empty,
  children,
}: {
  loading: boolean;
  error?: string;
  retry: () => unknown;
  empty?: boolean;
  children?: ReactNode;
}) {
  if (error)
    return (
      <div className="state-panel">
        <p className="inline-error">{error}</p>
        <Button
          icon={<IconRefresh aria-hidden="true" />}
          onClick={() => retry()}
        >
          重试
        </Button>
      </div>
    );
  if (loading)
    return (
      <div className="state-panel">
        <Spin size="large" />
      </div>
    );
  if (empty) return <EmptyState />;
  return <>{children}</>;
}
export function Field({
  label,
  ariaLabel,
  required,
  children,
  hint,
}: {
  label: ReactNode;
  ariaLabel?: string;
  required?: boolean;
  children: ReactNode;
  hint?: string;
}) {
  const id = useId();
  const controlLabel =
    ariaLabel || (typeof label === "string" ? label : undefined);
  return (
    <div className="field">
      <label id={id + "-label"} htmlFor={id}>
        {label}
        {required && (
          <span aria-hidden="true" className="required">
            {" "}
            *
          </span>
        )}
      </label>
      {isValidElement<{
        id?: string;
        "aria-label"?: string;
        "aria-labelledby"?: string;
      }>(children)
        ? cloneElement(children, {
            id: children.props.id || id,
            "aria-label": controlLabel,
            "aria-labelledby": id + "-label",
          })
        : children}
      {hint && <div className="field-hint">{hint}</div>}
    </div>
  );
}
export function SettingRow({
  label,
  children,
  variant,
  tip,
}: {
  label: string;
  children: ReactNode;
  variant?: "bordered" | "compact";
  tip?: string;
}) {
  return (
    <div className={`setting-row${variant ? ` setting-row--${variant}` : ""}`}>
      <span className="setting-row-label">
        {label}
        {tip && (
          <Tooltip content={tip} trigger="hover">
            <span
              className="field-tip-icon"
              aria-label={`${label}说明`}
              tabIndex={0}
            >
              <IconHelpCircleStroked aria-hidden="true" />
            </span>
          </Tooltip>
        )}
      </span>
      {children}
    </div>
  );
}
export function Info({
  label,
  children,
  mono,
}: {
  label: string;
  children: ReactNode;
  mono?: boolean;
}) {
  return (
    <div className="info-row">
      <span>{label}</span>
      <div className={mono ? "mono" : ""}>{children || "—"}</div>
    </div>
  );
}
export function Status({
  status,
  label,
  error,
}: {
  status?: number;
  label: string;
  error?: string | null;
}) {
  const color =
    status === 2
      ? "green"
      : status !== undefined && [3, 5, 6, 7].includes(status)
        ? "red"
        : status === 1
          ? "blue"
          : "grey";
  return (
    <span className="status-group">
      <Tag color={color} size="small" type="light">
        {label}
      </Tag>
      {error && (
        <Tooltip content={<div className="error-detail">{error}</div>}>
          <IconAlertTriangle className="error-indicator" aria-label="查看错误原因" size="small" />
        </Tooltip>
      )}
    </span>
  );
}
function confirmAction({
  title,
  content,
  okText,
  okLabel,
  onOk,
  variant = "confirm",
}: {
  title: string;
  content?: string;
  okText: string;
  okLabel?: string;
  onOk: () => void | Promise<unknown>;
  variant?: "error" | "confirm";
}) {
  const method = variant === "error" ? Modal.error : Modal.confirm;
  method({
    width: 454,
    className: "fnos-confirm",
    centered: true,
    closable: variant !== "error" ? undefined : false,
    title,
    content,
    okText,
    cancelText: "取消",
    okButtonProps: {
      type: "danger",
      ...(variant === "error" ? { theme: "solid" as const } : {}),
      "aria-label": okLabel || okText,
    },
    cancelButtonProps: { "aria-label": "取消" },
    onOk,
  });
}
export function confirmDelete(
  title: string,
  action: () => Promise<unknown>,
  content = "删除后无法恢复，是否继续？",
) {
  confirmAction({
    title,
    content,
    okText: "删除",
    variant: "error",
    onOk: async () => {
      try {
        // A run dropped by useAction (double-click while the first request is
        // still open) did nothing, so claiming "已删除" would be a lie.
        if ((await action()) === actionSkipped) return;
        Toast.success("已删除");
      } catch (error) {
        errorToast(error);
        throw error;
      }
    },
  });
}
export function confirmStopTask(action: () => Promise<unknown>) {
  confirmAction({
    title: "停止当前任务？",
    content: "已完成的文件不会撤销。",
    okText: "停止",
    okLabel: "停止任务",
    onOk: async () => {
      try {
        if ((await action()) === actionSkipped) return;
        Toast.success("已提交停止");
      } catch (error) {
        errorToast(error);
        throw error;
      }
    },
  });
}
export function Editor({
  title,
  visible,
  busy,
  onClose,
  onSave,
  children,
  dirty = false,
  saveLabel = "保存",
  width = 454,
  className,
  footer,
}: {
  title: string;
  visible: boolean;
  busy: boolean;
  onClose: () => void;
  onSave: () => void;
  children: ReactNode;
  dirty?: boolean;
  saveLabel?: string;
  width?: number;
  className?: string;
  footer?: ReactNode | ((controls: { close: () => void }) => ReactNode);
}) {
  useEffect(() => {
    if (!visible || !dirty) return;
    const host = getHost();
    if (!host.isStandaloneWeb)
      void host
        .setExitPageTips({ title: "尚未保存", content: "是否放弃当前修改？" })
        .catch(() => {});
    const leave = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", leave);
    return () => {
      window.removeEventListener("beforeunload", leave);
      if (!host.isStandaloneWeb) void host.setExitPageTips().catch(() => {});
    };
  }, [visible, dirty]);
  const close = () => {
    if (busy) return;
    if (dirty)
      confirmAction({
        title: "放弃未保存的修改？",
        okText: "放弃",
        onOk: onClose,
      });
    else onClose();
  };
  return (
    <Modal
      title={title}
      visible={visible}
      width={width}
      className={["editor-modal", className].filter(Boolean).join(" ")}
      centered
      maskClosable={!busy}
      closable={!busy}
      closeOnEsc={!busy}
      onCancel={close}
      closeIcon={<IconCrossStroked aria-hidden="true" />}
      footer={
        typeof footer === "function" ? (
          footer({ close })
        ) : (
          footer ?? (
            <div className="editor-actions">
              <Button onClick={close} disabled={busy}>
                取消
              </Button>
              <Button
                type="primary"
                theme="solid"
                loading={busy}
                onClick={onSave}
              >
                {saveLabel}
              </Button>
            </div>
          )
        )
      }
    >
      {children}
    </Modal>
  );
}
