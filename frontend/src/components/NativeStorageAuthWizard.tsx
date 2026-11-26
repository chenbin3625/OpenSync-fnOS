import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import TextArea from "@douyinfe/semi-ui/lib/es/input/textarea";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconAlertTriangle,
  IconChainStroked,
  IconInfoCircle,
  IconRefresh,
  IconTickCircle,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import { Field, errorToast } from "./common";
import type { NativeAuthQuestion, NativeAuthStepSnapshot } from "../types";

export interface CompletedAuthSession {
  sessionId: string;
  publicOptions: Record<string, string>;
  configuredSecrets: string[];
}

export interface NativeStorageAuthWizardProps {
  remoteType: string;
  remoteLabel: string;
  remoteName?: string;
  editing: boolean;
  draftOptions: Record<string, string>;
  clearSecrets?: string[];
  hasExistingAuth: boolean;
  completedSession: CompletedAuthSession | null;
  variant?: "oauth" | "quark_qr" | "jottacloud" | "icloud" | "seafile";
  disabled?: boolean;
  onSessionActiveChange?: (activeSessionId: string | null) => void;
  onCompleted: (result: CompletedAuthSession) => void;
}

interface LocalizedQuestion {
  title: string;
  description?: string;
  placeholder?: string;
  multiline?: boolean;
  linkUrl?: string;
  linkLabel?: string;
  exampleLabels?: Record<string, { label: string; desc?: string }>;
}

const urlPattern = /https?:\/\/[^\s"'<>)]+/;

function extractFirstUrl(text?: string): string | undefined {
  if (!text) return undefined;
  const match = text.match(urlPattern);
  if (!match) return undefined;
  return match[0].replace(/[.,;!?]+$/, "");
}

function localizeAuthQuestion(
  remoteType: string,
  q: NativeAuthQuestion,
): LocalizedQuestion {
  const linkUrl = extractFirstUrl(q.help);

  if (remoteType === "onedrive") {
    if (q.name === "config_type") {
      return {
        title: "选择 OneDrive 接入方式",
        description: "请选择要连接的 OneDrive 或 SharePoint 空间类型。",
        exampleLabels: {
          onedrive: {
            label: "OneDrive 个人版或商业版（推荐）",
            desc: "自动读取账号下的 OneDrive 空间列表",
          },
          sharepoint: {
            label: "SharePoint 根站点",
            desc: "连接组织默认的 SharePoint 根站点",
          },
          url: {
            label: "通过 SharePoint 站点名称或 URL 接入",
            desc: "指定 SharePoint 站点链接",
          },
          search: {
            label: "搜索 SharePoint 站点",
            desc: "按关键词检索组织内的 SharePoint 站点",
          },
          driveid: {
            label: "手动输入 Drive ID",
            desc: "适用于已知目标 Drive ID 的场景",
          },
          siteid: {
            label: "手动输入 SharePoint Site ID",
          },
          pathid: {
            label: "按 SharePoint 路径查找",
          },
        },
      };
    }
    if (q.name === "config_driveid") {
      return {
        title: "选择要挂载的 OneDrive 空间",
        description: "已自动从微软账号获取到以下可用空间，请选择其中一个：",
        placeholder: "选择或输入 Drive ID",
      };
    }
    if (q.name === "config_drive_ok") {
      return {
        title: "确认选中的 OneDrive 空间",
        description: q.help || "请确认是否使用该 OneDrive 空间完成配置。",
        exampleLabels: {
          true: { label: "确认使用该空间" },
          false: { label: "返回重新选择" },
        },
      };
    }
    if (q.name === "config_site_url") {
      return {
        title: "输入 SharePoint 站点地址",
        description: "例如 https://contoso.sharepoint.com/sites/mysite",
        placeholder: "https://contoso.sharepoint.com/sites/...",
      };
    }
    if (q.name === "config_search_term") {
      return {
        title: "输入 SharePoint 站点搜索关键词",
        description: "输入站点名称关键词以搜索可用站点。",
        placeholder: "站点关键词",
      };
    }
  }

  if (remoteType === "drive") {
    if (q.name === "config_change_team_drive") {
      return {
        title: "选择 Google Drive 访问范围",
        description: "选择挂载您的个人云端硬盘，或选择某个共享云端硬盘（Team Drive）。",
        exampleLabels: {
          false: {
            label: "个人云端硬盘（我的云端硬盘，推荐）",
            desc: "直接访问账号的 My Drive",
          },
          true: {
            label: "共享云端硬盘（Team Drive）",
            desc: "从您的团队盘列表中选择一个挂载",
          },
        },
      };
    }
    if (q.name === "config_team_drive") {
      return {
        title: "选择共享云端硬盘（Team Drive）",
        description: "请选择要接入的 Google Shared Drive：",
      };
    }
  }

  if (remoteType === "jottacloud") {
    if (q.name === "config_type") {
      return {
        title: "选择 Jottacloud 授权方式",
        description: "推荐使用个人登录令牌（Personal Login Token），全程在浏览器中完成。",
        exampleLabels: {
          standard: {
            label: "个人登录令牌授权（推荐）",
            desc: "在 Jottacloud 账号安全页面生成一次性 Personal Login Token 并粘贴",
          },
          traditional: {
            label: "网页 OAuth 授权（支持官方与白标服务）",
            desc: "选择服务商后通过浏览器登录授权",
          },
          legacy: {
            label: "旧版账号密码认证（官方已停用）",
            desc: "仅兼容历史接口，不推荐使用",
          },
        },
      };
    }
    if (q.name === "config_login_token") {
      return {
        title: "粘贴 Jottacloud 个人登录令牌",
        description:
          "请点击下方按钮前往 Jottacloud 账号安全设置生成一次性 Personal Login Token，复制后粘贴到下方输入框。",
        placeholder: "粘贴 Personal Login Token",
        multiline: true,
        linkUrl: linkUrl || "https://www.jottacloud.com/web/secure",
        linkLabel: "前往 Jottacloud 安全设置生成令牌",
      };
    }
    if (q.name === "config_traditional") {
      return {
        title: "选择 Jottacloud 服务商",
        description: "请选择您使用的官方或白标服务。",
      };
    }
    if (
      q.name === "config_device_ok" ||
      q.name === "config_device" ||
      q.name === "config_mountpoint" ||
      q.state === "choose_device"
    ) {
      return {
        title: "选择 Jottacloud 设备与挂载点",
        description: q.help || "通常保持默认标准挂载点即可。",
        exampleLabels: {
          false: { label: "使用默认标准挂载点（推荐）" },
          true: { label: "自定义设备 / 挂载点" },
        },
      };
    }
  }

  if (remoteType === "iclouddrive") {
    if (q.name.includes("2fa") || q.help.toLowerCase().includes("code")) {
      return {
        title: "输入 Apple 两步验证码（2FA）",
        description:
          "请在已信任的 iPhone / iPad / Mac 上查看收到的 6 位验证码并输入。",
        placeholder: "6 位数字验证码",
      };
    }
  }

  if (remoteType === "seafile") {
    if (q.name === "config_2fa") {
      return {
        title: "输入 Seafile 两步验证码（2FA）",
        description: "请输入验证器应用生成的 6 位动态验证码以换取 Auth Token。",
        placeholder: "6 位验证码",
      };
    }
  }

  return {
    title: q.name ? `配置步骤：${q.name}` : "继续网盘配置",
    description: q.help || undefined,
    placeholder: q.default ? `默认：${q.default}` : "请输入配置项内容",
    linkUrl,
    linkLabel: linkUrl ? "打开相关页面" : undefined,
    exampleLabels:
      q.type === "bool"
        ? {
            true: { label: "是（Yes）" },
            false: { label: "否（No）" },
          }
        : undefined,
  };
}

function summarizeCompletedAuth(
  remoteType: string,
  completed: CompletedAuthSession,
): string[] {
  const details: string[] = [];
  if (completed.configuredSecrets.includes("token")) {
    details.push("已获取 OAuth 授权令牌");
  }
  if (
    completed.configuredSecrets.includes("trust_token") ||
    completed.configuredSecrets.includes("cookies")
  ) {
    details.push("已获取 iCloud 信任凭据与会话 Cookies");
  }
  if (completed.configuredSecrets.includes("auth_token")) {
    details.push("已获取 Seafile Auth Token");
  }
  if (completed.configuredSecrets.includes("cookie")) {
    details.push("已获取夸克网盘会话 Cookie（支持 __puus 自动续期）");
  }
  const pub = completed.publicOptions || {};
  if (remoteType === "quark" && pub.account_name) {
    details.push(`夸克账号：${pub.account_name}`);
  }
  if (remoteType === "onedrive" && (pub.drive_type || pub.drive_id)) {
    const parts = [pub.drive_type, pub.drive_id].filter(Boolean);
    details.push(`OneDrive 空间：${parts.join(" · ")}`);
  }
  if (remoteType === "drive" && pub.team_drive) {
    details.push(`团队盘 ID：${pub.team_drive}`);
  }
  if (remoteType === "pcloud" && pub.hostname) {
    details.push(`API 区域：${pub.hostname}`);
  }
  if (remoteType === "jottacloud" && (pub.device || pub.mountpoint)) {
    details.push(
      `挂载点：${[pub.device, pub.mountpoint].filter(Boolean).join(" / ")}`,
    );
  }
  return details;
}

export function NativeStorageAuthWizard({
  remoteType,
  remoteLabel,
  remoteName,
  editing,
  draftOptions,
  clearSecrets,
  hasExistingAuth,
  completedSession,
  variant = "oauth",
  disabled = false,
  onSessionActiveChange,
  onCompleted,
}: NativeStorageAuthWizardProps) {
  const [step, setStep] = useState<NativeAuthStepSnapshot | null>(null);
  const [starting, setStarting] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [callbackInput, setCallbackInput] = useState("");
  const [answerInput, setAnswerInput] = useState("");
  const [stepError, setStepError] = useState("");
  const activeSessionRef = useRef<string | null>(null);

  const syncActiveSession = useCallback(
    (sessionId: string | null) => {
      activeSessionRef.current = sessionId;
      onSessionActiveChange?.(sessionId);
    },
    [onSessionActiveChange],
  );

  const applySnapshot = useCallback(
    (snap: NativeAuthStepSnapshot) => {
      setStepError("");
      if (snap.status === "completed") {
        setStep(null);
        setCallbackInput("");
        setAnswerInput("");
        syncActiveSession(null);
        onCompleted({
          sessionId: snap.sessionId,
          publicOptions: snap.publicOptions || {},
          configuredSecrets: snap.configuredSecrets || [],
        });
        Toast.success(`${remoteLabel} 授权接入已完成，可测试连接后保存`);
        return;
      }
      if (snap.status === "failed" || snap.status === "cancelled") {
        setStep(snap);
        syncActiveSession(null);
        return;
      }
      setStep(snap);
      syncActiveSession(snap.sessionId);
      if (snap.status === "question" && snap.question) {
        const defaultVal =
          snap.question.default || snap.question.examples?.[0]?.value || "";
        setAnswerInput(defaultVal);
      }
    },
    [onCompleted, remoteLabel, syncActiveSession],
  );

  // Reset local wizard state when switching storage type.
  useEffect(() => {
    const prevId = activeSessionRef.current;
    if (prevId) {
      void api.cancelNativeStorageAuth(prevId).catch(() => undefined);
      syncActiveSession(null);
    }
    setStep(null);
    setCallbackInput("");
    setAnswerInput("");
    setStepError("");
  }, [remoteType, syncActiveSession]);

  // Cancel an unfinished backend auth session on unmount.
  useEffect(() => {
    return () => {
      const id = activeSessionRef.current;
      if (id) {
        void api.cancelNativeStorageAuth(id).catch(() => undefined);
      }
    };
  }, []);

  // Poll status while waiting for OAuth callback or QR scan confirmation.
  useEffect(() => {
    if (
      !step ||
      (step.status !== "oauth" && step.status !== "qrcode") ||
      !step.sessionId
    ) {
      return;
    }
    let stopped = false;
    const sessionId = step.sessionId;
    const currentStatus = step.status;
    const timer = window.setInterval(async () => {
      if (stopped) return;
      try {
        const latest = await api.getNativeStorageAuth(sessionId);
        if (stopped) return;
        if (latest.status !== currentStatus || latest.qrStatus !== step.qrStatus) {
          applySnapshot(latest);
        }
      } catch {
        // Ignore transient poll errors while waiting on user action.
      }
    }, 2000);
    return () => {
      stopped = true;
      window.clearInterval(timer);
    };
  }, [step, applySnapshot]);

  const handleStart = async () => {
    if (activeSessionRef.current) {
      void api.cancelNativeStorageAuth(activeSessionRef.current).catch(() => undefined);
      syncActiveSession(null);
    }
    setStarting(true);
    setStepError("");
    setCallbackInput("");
    setAnswerInput("");
    try {
      const snap = await api.startNativeStorageAuth({
        type: remoteType,
        name: editing ? remoteName : undefined,
        editing,
        options: { ...draftOptions, type: remoteType },
        clearSecrets: clearSecrets && clearSecrets.length > 0 ? clearSecrets : undefined,
      });
      applySnapshot(snap);
    } catch (err) {
      const message = err instanceof Error ? err.message : "启动网盘授权失败";
      setStepError(message);
      errorToast(err);
    } finally {
      setStarting(false);
    }
  };

  const handleCancel = async () => {
    const id = step?.sessionId || activeSessionRef.current;
    setStep(null);
    setCallbackInput("");
    setAnswerInput("");
    setStepError("");
    syncActiveSession(null);
    if (id) {
      void api.cancelNativeStorageAuth(id).catch(() => undefined);
    }
  };

  const handleSubmitCallback = async () => {
    if (!step?.sessionId) return;
    const trimmed = callbackInput.trim();
    if (!trimmed) {
      setStepError("请粘贴授权完成后浏览器地址栏中的回调网址");
      return;
    }
    setSubmitting(true);
    setStepError("");
    try {
      const next = await api.submitNativeStorageAuthCallback({
        sessionId: step.sessionId,
        callbackUrl: trimmed,
      });
      applySnapshot(next);
    } catch (err) {
      setStepError(err instanceof Error ? err.message : "提交回调地址失败");
    } finally {
      setSubmitting(false);
    }
  };

  const handleContinueQuestion = async () => {
    if (!step?.sessionId || !step.question) return;
    const answer = answerInput.trim();
    if (step.question.required && !answer) {
      setStepError("请先填写或选择当前步骤内容");
      return;
    }
    setSubmitting(true);
    setStepError("");
    try {
      const next = await api.continueNativeStorageAuth({
        sessionId: step.sessionId,
        state: step.question.state,
        answer,
      });
      applySnapshot(next);
    } catch (err) {
      setStepError(err instanceof Error ? err.message : "提交配置步骤失败");
    } finally {
      setSubmitting(false);
    }
  };

  const handleCopyAuthUrl = async (url: string) => {
    try {
      await navigator.clipboard?.writeText(url);
      Toast.success("已复制授权链接");
    } catch {
      Toast.info("复制失败，请点击「打开授权页面」直接前往");
    }
  };

  const localized = useMemo(
    () => (step?.question ? localizeAuthQuestion(remoteType, step.question) : null),
    [remoteType, step?.question],
  );

  const completedDetails = useMemo(
    () => (completedSession ? summarizeCompletedAuth(remoteType, completedSession) : []),
    [remoteType, completedSession],
  );

  const idleCopy = useMemo(() => {
    if (variant === "quark_qr") {
      return {
        title: "夸克 App 扫码登录（推荐）",
        desc: "使用手机夸克 App 扫一扫直接登录并获取会话 Cookie，同步期间自动续期 __puus，无需手动抓包。",
        btn: hasExistingAuth ? "重新扫码登录" : "生成登录二维码",
      };
    }
    if (variant === "jottacloud") {
      return {
        title: "Jottacloud 图形化授权接入",
        desc: "支持在 Jottacloud 网页端生成个人登录令牌直接接入，或通过浏览器 OAuth 登录，全程无需执行命令行。",
        btn: hasExistingAuth ? "重新授权 Jottacloud" : "开始 Jottacloud 授权配置",
      };
    }
    if (variant === "icloud") {
      return {
        title: "Apple ID 登录与两步验证（2FA）",
        desc: "填写上方 Apple ID 和密码后点击下方按钮，按界面提示输入 Apple 6 位验证码即可自动生成并保存信任凭据与 Cookies。",
        btn: hasExistingAuth ? "重新验证 iCloud 登录" : "验证登录并获取凭据",
      };
    }
    if (variant === "seafile") {
      return {
        title: "Seafile 两步验证（2FA）登录（可选）",
        desc: "开启 2FA 的账号可先填写上方地址、账号和密码，点击下方按钮输入动态验证码自动获取 Auth Token。",
        btn: hasExistingAuth ? "重新通过 2FA 获取 Token" : "通过 2FA 验证码获取 Auth Token",
      };
    }
    return {
      title: `网页授权接入 ${remoteLabel}`,
      desc:
        remoteType === "onedrive"
          ? "直接在浏览器登录微软账号完成授权，并自动读取 OneDrive / SharePoint 空间列表供点选，无需命令行。"
          : remoteType === "drive"
            ? "直接在浏览器登录 Google 账号完成授权，并可选择个人云端硬盘或团队盘，无需命令行。"
            : `直接在浏览器中登录 ${remoteLabel} 并同意授权，全程图形化完成配置接入，无需命令行。`,
      btn: hasExistingAuth ? `重新网页授权 ${remoteLabel}` : `开始网页授权登录`,
    };
  }, [variant, remoteLabel, remoteType, hasExistingAuth]);

  const busy = disabled || starting || submitting;

  // 0. Active QR code login step (e.g. Quark Drive)
  if (step && step.status === "qrcode" && step.qrCodeImage) {
    const expired = step.qrStatus === "expired";
    return (
      <div
        className="native-auth-card is-active"
        role="region"
        aria-label={`${remoteLabel} 扫码登录`}
      >
        <div className="native-auth-head">
          <div className="native-auth-title">
            <IconChainStroked aria-hidden="true" size="small" />
            <strong>扫码登录 · {remoteLabel}</strong>
            <Tag size="small" type="light" color={expired ? "orange" : "blue"}>
              {expired ? "已过期" : "等待扫码"}
            </Tag>
          </div>
          <Button
            size="small"
            type="tertiary"
            theme="borderless"
            disabled={starting}
            onClick={() => void handleCancel()}
          >
            取消
          </Button>
        </div>

        <div className="native-auth-qr">
          <div className="native-auth-qr-box">
            <img
              src={step.qrCodeImage}
              alt={`${remoteLabel} 登录二维码`}
              className="native-auth-qr-img"
              width={184}
              height={184}
            />
          </div>
          <div className="native-auth-qr-info">
            <ol className="native-auth-steps">
              <li>
                打开手机 <strong>夸克 App</strong>，点击「扫一扫」。
              </li>
              <li>扫描左侧二维码并在手机上点击确认登录。</li>
              <li>确认后将自动获取完整 Cookie 并验证账号，无需手动粘贴。</li>
            </ol>
            <div className="native-auth-actions">
              <Button
                size="small"
                type="primary"
                theme="light"
                icon={<IconRefresh aria-hidden="true" />}
                loading={starting}
                onClick={() => void handleStart()}
              >
                刷新二维码
              </Button>
              {step.qrCodeUrl && (
                <Button
                  size="small"
                  type="tertiary"
                  disabled={starting}
                  onClick={() => void handleCopyAuthUrl(step.qrCodeUrl!)}
                >
                  复制扫码链接
                </Button>
              )}
            </div>
          </div>
        </div>
      </div>
    );
  }

  // 1. Active OAuth browser login step
  if (step && step.status === "oauth" && step.authUrl) {
    const redirectHint = step.redirectUri || "http://localhost:53682/";
    return (
      <div className="native-auth-card is-active" role="region" aria-label={`${remoteLabel} 网页授权向导`}>
        <div className="native-auth-head">
          <div className="native-auth-title">
            <IconChainStroked aria-hidden="true" size="small" />
            <strong>网页授权登录 · {remoteLabel}</strong>
            <Tag size="small" type="light" color="blue">
              等待授权
            </Tag>
          </div>
          <Button
            size="small"
            type="tertiary"
            theme="borderless"
            disabled={submitting}
            onClick={() => void handleCancel()}
          >
            取消
          </Button>
        </div>

        <ol className="native-auth-steps">
          <li>
            点击下方按钮在新标签页登录 <strong>{remoteLabel}</strong> 并同意授权。
          </li>
          <li>
            授权成功后浏览器会自动跳转到 <code className="mono">{redirectHint}</code>
            （远程访问 NAS 时页面提示“无法访问此网站 / 拒绝连接”属于正常现象，授权码已包含在地址栏中）。
          </li>
          <li>
            复制跳转后浏览器地址栏中的<strong>完整网址</strong>粘贴到下方并提交；若在 NAS 本机浏览器操作，授权后将自动继续。
          </li>
        </ol>

        <div className="native-auth-actions">
          <Button
            type="primary"
            theme="solid"
            disabled={submitting}
            onClick={() =>
              window.open(step.authUrl, "_blank", "noopener,noreferrer")
            }
          >
            打开 {remoteLabel} 授权页面
          </Button>
          <Button
            type="tertiary"
            disabled={submitting}
            onClick={() => void handleCopyAuthUrl(step.authUrl!)}
          >
            复制授权链接
          </Button>
        </div>

        <Field
          label="粘贴授权后的浏览器回调网址"
          error={stepError || undefined}
        >
          <div className="native-password-row">
            <Input
              aria-label="粘贴授权后的浏览器回调网址"
              value={callbackInput}
              disabled={submitting}
              onChange={(v) => {
                setCallbackInput(v);
                if (stepError) setStepError("");
              }}
              onEnterPress={() => void handleSubmitCallback()}
              placeholder={`例如 ${redirectHint}?code=...&state=...`}
            />
            <Button
              type="primary"
              theme="solid"
              loading={submitting}
              disabled={!callbackInput.trim()}
              onClick={() => void handleSubmitCallback()}
            >
              完成授权
            </Button>
          </div>
        </Field>
      </div>
    );
  }

  // 2. Active interactive question step (Drive picker / Team Drive / Jottacloud token / 2FA)
  if (step && step.status === "question" && step.question && localized) {
    const q = step.question;
    const examples = q.examples || [];
    const useSelect = q.exclusive && examples.length > 6;
    const useCards = q.exclusive && examples.length > 0 && examples.length <= 6;

    return (
      <div className="native-auth-card is-active" role="region" aria-label={`${remoteLabel} 配置向导`}>
        <div className="native-auth-head">
          <div className="native-auth-title">
            <IconInfoCircle aria-hidden="true" size="small" />
            <strong>{localized.title}</strong>
            <Tag size="small" type="light" color="blue">
              配置向导
            </Tag>
          </div>
          <Button
            size="small"
            type="tertiary"
            theme="borderless"
            disabled={submitting}
            onClick={() => void handleCancel()}
          >
            取消
          </Button>
        </div>

        {localized.description && (
          <p className="native-auth-desc">{localized.description}</p>
        )}

        {localized.linkUrl && (
          <div className="native-auth-actions">
            <Button
              type="primary"
              theme="light"
              onClick={() =>
                window.open(localized.linkUrl, "_blank", "noopener,noreferrer")
              }
            >
              {localized.linkLabel || "打开页面"}
            </Button>
          </div>
        )}

        {(q.error || stepError) && (
          <div className="native-test-banner is-error" role="alert">
            <IconAlertTriangle aria-hidden="true" size="small" />
            <span>{stepError || q.error}</span>
          </div>
        )}

        {useCards ? (
          <div className="native-auth-options" role="radiogroup" aria-label={localized.title}>
            {examples.map((ex) => {
              const custom = localized.exampleLabels?.[ex.value];
              const selected = answerInput === ex.value;
              const label = custom?.label || ex.help || ex.value;
              const desc =
                custom?.desc ||
                (ex.help && ex.help !== ex.value && !custom ? ex.value : undefined);
              return (
                <button
                  key={ex.value}
                  type="button"
                  role="radio"
                  aria-checked={selected}
                  disabled={submitting}
                  className={`native-auth-option${selected ? " is-selected" : ""}`}
                  onClick={() => {
                    setAnswerInput(ex.value);
                    if (stepError) setStepError("");
                  }}
                >
                  <span className="native-auth-option-label">{label}</span>
                  {desc && <span className="native-auth-option-desc">{desc}</span>}
                </button>
              );
            })}
          </div>
        ) : useSelect ? (
          <Select
            aria-label={localized.title}
            value={answerInput}
            filter
            disabled={submitting}
            optionList={examples.map((ex) => {
              const custom = localized.exampleLabels?.[ex.value];
              return {
                value: ex.value,
                label: custom?.label || (ex.help ? `${ex.help} (${ex.value})` : ex.value),
              };
            })}
            onChange={(v) => {
              setAnswerInput(String(v ?? ""));
              if (stepError) setStepError("");
            }}
          />
        ) : (
          <>
            {localized.multiline ? (
              <TextArea
                aria-label={localized.title}
                autosize={{ minRows: 2, maxRows: 5 }}
                value={answerInput}
                disabled={submitting}
                onChange={(v) => {
                  setAnswerInput(v);
                  if (stepError) setStepError("");
                }}
                placeholder={localized.placeholder}
              />
            ) : (
              <Input
                aria-label={localized.title}
                mode={q.isPassword ? "password" : undefined}
                value={answerInput}
                disabled={submitting}
                onChange={(v) => {
                  setAnswerInput(v);
                  if (stepError) setStepError("");
                }}
                onEnterPress={() => void handleContinueQuestion()}
                placeholder={localized.placeholder}
              />
            )}
            {examples.length > 0 && (
              <div className="native-quick-picks">
                <span className="muted">可选项：</span>
                {examples.map((ex) => (
                  <button
                    key={ex.value}
                    type="button"
                    className="native-quick-chip"
                    onClick={() => setAnswerInput(ex.value)}
                  >
                    {ex.help || ex.value}
                  </button>
                ))}
              </div>
            )}
          </>
        )}

        <div className="native-auth-footer">
          <Button
            type="primary"
            theme="solid"
            loading={submitting}
            onClick={() => void handleContinueQuestion()}
          >
            下一步
          </Button>
        </div>
      </div>
    );
  }

  // 3. Failed state
  if (step && step.status === "failed") {
    return (
      <div className="native-auth-card is-error" role="alert">
        <div className="native-auth-head">
          <div className="native-auth-title">
            <IconAlertTriangle aria-hidden="true" size="small" />
            <strong>{remoteLabel} 授权未完成</strong>
          </div>
          <Button
            size="small"
            type="tertiary"
            theme="borderless"
            onClick={() => setStep(null)}
          >
            关闭
          </Button>
        </div>
        <p className="native-auth-desc">{step.error || "授权失败，请重试"}</p>
        <div className="native-auth-actions">
          <Button
            type="primary"
            theme="solid"
            loading={starting}
            disabled={busy}
            onClick={() => void handleStart()}
          >
            重新发起授权
          </Button>
        </div>
      </div>
    );
  }

  // 4. Completed in current form session
  if (completedSession) {
    return (
      <div className="native-auth-card is-done" role="status">
        <div className="native-auth-head">
          <div className="native-auth-title">
            <IconTickCircle aria-hidden="true" size="small" />
            <strong>已完成 {remoteLabel} 图形化授权接入</strong>
            <Tag size="small" type="light" color="green">
              已就绪
            </Tag>
          </div>
          <Button
            size="small"
            type="tertiary"
            icon={<IconRefresh aria-hidden="true" />}
            loading={starting}
            disabled={busy}
            onClick={() => void handleStart()}
          >
            重新授权
          </Button>
        </div>
        {completedDetails.length > 0 && (
          <div className="native-auth-meta">
            {completedDetails.map((line) => (
              <span key={line}>{line}</span>
            ))}
          </div>
        )}
        <p className="native-auth-desc">
          凭据已安全暂存于服务端，可直接点击下方「测试连接」验证后「保存」。
        </p>
      </div>
    );
  }

  // 5. Idle state (initial or already saved when editing)
  return (
    <div className="native-auth-card">
      <div className="native-auth-head">
        <div className="native-auth-title">
          <IconChainStroked aria-hidden="true" size="small" />
          <strong>{idleCopy.title}</strong>
          {editing && hasExistingAuth && (
            <Tag size="small" type="light" color="green">
              已配置授权
            </Tag>
          )}
        </div>
        <Button
          type="primary"
          theme={variant === "seafile" || (editing && hasExistingAuth) ? "light" : "solid"}
          size="small"
          loading={starting}
          disabled={busy}
          onClick={() => void handleStart()}
        >
          {idleCopy.btn}
        </Button>
      </div>
      <p className="native-auth-desc">{idleCopy.desc}</p>
      {remoteType === "drive" && !(draftOptions.client_id || "").trim() && (
        <p className="native-auth-note">
          提示：留空自定义 Client ID 将使用内置应用授权；如需长期大流量稳定同步，可在下方「高级凭据设置」填写自建 Google OAuth Client ID / Secret。
        </p>
      )}
      {stepError && (
        <div className="native-test-banner is-error" role="alert">
          <IconAlertTriangle aria-hidden="true" size="small" />
          <span>{stepError}</span>
        </div>
      )}
    </div>
  );
}
