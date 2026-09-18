import {
  cloneElement,
  isValidElement,
  useEffect,
  useId,
  type ReactNode,
} from "react";
import Banner from "@douyinfe/semi-ui/lib/es/banner";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Empty from "@douyinfe/semi-ui/lib/es/empty";
import Modal from "@douyinfe/semi-ui/lib/es/modal";
import Spin from "@douyinfe/semi-ui/lib/es/spin";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import Tooltip from "@douyinfe/semi-ui/lib/es/tooltip";
import { IconRefresh, IconClose } from "@douyinfe/semi-icons";
import { getHost } from "../lib/host";

export function errorToast(error: unknown) {
  Toast.error(error instanceof Error ? error.message : "操作失败");
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
      {tabs && <div className="page-tabs">{tabs}</div>}
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
}: {
  loading: boolean;
  error?: string;
  retry: () => unknown;
  empty?: boolean;
}) {
  if (error)
    return (
      <div className="state-panel">
        <Banner type="danger" description={error} closeIcon={null} />
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
  return null;
}
export function Field({
  label,
  required,
  children,
  hint,
}: {
  label: string;
  required?: boolean;
  children: ReactNode;
  hint?: string;
}) {
  const id = useId();
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
            "aria-label": label,
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
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <div className="setting-row">
      <span>{label}</span>
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
          <button className="error-indicator" aria-label="查看错误原因">
            !
          </button>
        </Tooltip>
      )}
    </span>
  );
}
export function confirmDelete(
  title: string,
  action: () => Promise<unknown>,
  content = "删除后无法恢复，是否继续？",
) {
  Modal.confirm({
    width: 454,
    className: "fnos-confirm",
    title,
    content,
    okText: "确认删除",
    cancelText: "取消",
    okButtonProps: { type: "danger", "aria-label": "确认删除" },
    cancelButtonProps: { "aria-label": "取消" },
    onOk: async () => {
      try {
        await action();
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
      Modal.confirm({
        width: 454,
        className: "fnos-confirm",
        title: "放弃未保存的修改？",
        okText: "放弃修改",
        cancelText: "继续编辑",
        okButtonProps: { "aria-label": "放弃修改" },
        cancelButtonProps: { "aria-label": "继续编辑" },
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
      closeIcon={<IconClose aria-hidden="true" />}
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
