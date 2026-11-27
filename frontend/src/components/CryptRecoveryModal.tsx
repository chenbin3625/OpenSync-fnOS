import { useCallback, useEffect, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Modal from "@douyinfe/semi-ui/lib/es/modal";
import Spin from "@douyinfe/semi-ui/lib/es/spin";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconAlertTriangle,
  IconCopy,
  IconDownload,
  IconFolderStroked,
  IconKey,
  IconLockStroked,
  IconTickCircle,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import { Field, errorToast } from "./common";
import type { NativeCryptRecoveryResult } from "../types";

export interface CryptRecoveryModalProps {
  remoteName: string;
  initialPassword?: string;
  initialPassword2?: string;
  onClose: () => void;
  onOpenRestore?: (remoteName: string) => void;
}

function downloadTextFile(filename: string, content: string, mimeType: string) {
  const blob = new Blob([content], { type: mimeType });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}

export function CryptRecoveryModal({
  remoteName,
  initialPassword = "",
  initialPassword2 = "",
  onClose,
  onOpenRestore,
}: CryptRecoveryModalProps) {
  const [loading, setLoading] = useState(true);
  const [verifying, setVerifying] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<NativeCryptRecoveryResult | null>(null);
  const [verifyPassword, setVerifyPassword] = useState(initialPassword);
  const [verifyPassword2, setVerifyPassword2] = useState(initialPassword2);
  const [includeVerifiedPlaintext, setIncludeVerifiedPlaintext] = useState(false);

  const loadBundle = useCallback(
    async (pw?: string, pw2?: string, isVerify = false) => {
      if (isVerify) {
        setVerifying(true);
      } else {
        setLoading(true);
      }
      setError("");
      try {
        const res = await api.getCryptRecovery({
          name: remoteName,
          verifyPassword: pw || undefined,
          verifyPassword2: pw2 || undefined,
        });
        setResult(res);
        if (isVerify) {
          const pwOk = !res.passwordChecked || res.passwordVerified === true;
          const pw2Ok = !res.password2Checked || res.password2Verified === true;
          if (!pwOk || !pw2Ok) {
            setIncludeVerifiedPlaintext(false);
          }
        }
      } catch (err) {
        const msg = err instanceof Error ? err.message : "读取密钥恢复包失败";
        setError(msg);
        errorToast(err);
      } finally {
        setLoading(false);
        setVerifying(false);
      }
    },
    [remoteName],
  );

  useEffect(() => {
    void loadBundle(
      initialPassword || undefined,
      initialPassword2 || undefined,
      false,
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [remoteName]);

  const handleVerify = async () => {
    if (!verifyPassword && !verifyPassword2) {
      Toast.warning("请至少输入主加密密码或加密盐后再验证");
      return;
    }
    await loadBundle(verifyPassword, verifyPassword2, true);
  };

  const canIncludePlaintext = Boolean(
    result &&
      result.passwordChecked &&
      result.passwordVerified === true &&
      verifyPassword &&
      (!result.bundle.hasPassword2 ||
        (result.password2Checked &&
          result.password2Verified === true &&
          verifyPassword2)),
  );

  const effectiveCanEmbedPlaintext =
    canIncludePlaintext && includeVerifiedPlaintext;

  const buildDownloadableJson = () => {
    if (!result) return "";
    const payload: Record<string, unknown> = {
      ...result.bundle,
      plaintextIncluded: effectiveCanEmbedPlaintext,
    };
    if (effectiveCanEmbedPlaintext) {
      payload.verifiedCredentials = {
        password: verifyPassword,
        ...(verifyPassword2 ? { password2: verifyPassword2 } : {}),
      };
    }
    return JSON.stringify(payload, null, 2);
  };

  const buildDownloadableText = () => {
    if (!result) return "";
    if (!effectiveCanEmbedPlaintext) {
      return result.recoveryText;
    }
    return [
      result.recoveryText,
      "",
      "--- 已验证的明文恢复凭据（请离线加密或写入密码管理器后删除临时文件）---",
      `主加密密码 (password): ${verifyPassword}`,
      ...(verifyPassword2
        ? [`加密盐 (password2): ${verifyPassword2}`]
        : []),
      "",
    ].join("\n");
  };

  const handleCopyText = async () => {
    const content = buildDownloadableText();
    if (!content) return;
    try {
      await navigator.clipboard.writeText(content);
      Toast.success("已复制密钥恢复包内容");
    } catch {
      Toast.warning("剪贴板权限受限，请点击「下载恢复指南」保存文件");
    }
  };

  const handleDownloadJson = () => {
    if (!result) return;
    downloadTextFile(
      `${remoteName}-crypt-recovery.json`,
      buildDownloadableJson(),
      "application/json;charset=utf-8",
    );
    Toast.success(`已导出 ${remoteName}-crypt-recovery.json`);
  };

  const handleDownloadText = () => {
    if (!result) return;
    downloadTextFile(
      `${remoteName}-crypt-recovery.txt`,
      buildDownloadableText(),
      "text/plain;charset=utf-8",
    );
    Toast.success(`已导出 ${remoteName}-crypt-recovery.txt`);
  };

  return (
    <Modal
      visible
      centered
      width={620}
      title={
        <span className="quick-crypt-modal-title">
          <IconKey aria-hidden="true" />
          <span>导出密钥恢复包 · {remoteName}</span>
        </span>
      }
      onCancel={onClose}
      footer={
        <div className="editor-actions">
          {onOpenRestore && (
            <Button
              type="tertiary"
              icon={<IconFolderStroked aria-hidden="true" />}
              onClick={() => {
                onClose();
                onOpenRestore(remoteName);
              }}
            >
              从加密存储一键还原到本地
            </Button>
          )}
          <Button
            type="tertiary"
            icon={<IconCopy aria-hidden="true" />}
            disabled={!result}
            onClick={() => void handleCopyText()}
          >
            复制恢复文本
          </Button>
          <Button
            type="tertiary"
            icon={<IconDownload aria-hidden="true" />}
            disabled={!result}
            onClick={handleDownloadText}
          >
            下载恢复指南 (.txt)
          </Button>
          <Button
            type="primary"
            theme="solid"
            icon={<IconDownload aria-hidden="true" />}
            disabled={!result}
            onClick={handleDownloadJson}
          >
            导出密钥恢复包 (.json)
          </Button>
        </div>
      }
    >
      {loading ? (
        <div className="native-loading-panel">
          <Spin />
        </div>
      ) : error && !result ? (
        <div className="inline-error">{error}</div>
      ) : result ? (
        <div className="crypt-recovery-body">
          <div className="sync-method-banner is-warning" role="status">
            <IconAlertTriangle aria-hidden="true" size="small" />
            <span>{result.bundle.warning}</span>
          </div>

          <div className="crypt-recovery-meta-grid">
            <div className="crypt-recovery-kv">
              <span className="muted">加密存储</span>
              <span className="crypt-recovery-val">
                <IconLockStroked aria-hidden="true" size="extra-small" />{" "}
                <strong>{result.bundle.remoteName}</strong>
                {result.bundle.managedByJob && (
                  <Tag size="small" type="light" color="cyan">
                    任务专属加密通道
                    {result.bundle.managedJobLabel
                      ? ` · ${result.bundle.managedJobLabel}`
                      : ""}
                  </Tag>
                )}
              </span>
            </div>
            <div className="crypt-recovery-kv">
              <span className="muted">底层存储路径</span>
              <code>{result.bundle.remote}</code>
            </div>
            <div className="crypt-recovery-kv">
              <span className="muted">文件名加密</span>
              <span>
                {result.bundle.filenameEncryption} /{" "}
                {result.bundle.filenameEncoding}（目录名加密：
                {result.bundle.directoryNameEncryption}）
              </span>
            </div>
            <div className="crypt-recovery-kv">
              <span className="muted">数据加密与盐</span>
              <span>
                {result.bundle.noDataEncryption
                  ? "仅加密文件名（未加密内容）"
                  : "完整文件内容加密"}
                {" · "}
                {result.bundle.hasPassword2
                  ? "已配置加密盐 (password2)"
                  : "未配置加密盐"}
              </span>
            </div>
          </div>

          <div className="crypt-recovery-verify-box">
            <div className="crypt-recovery-section-title">
              输入明文密码核对与备份（可选）
            </div>
            <div className="form-grid form-grid--two">
              <Field label="主加密密码 (password)">
                <Input
                  mode="password"
                  aria-label="验证主加密密码"
                  value={verifyPassword}
                  disabled={verifying}
                  onChange={(v) => {
                    setVerifyPassword(v);
                    setIncludeVerifiedPlaintext(false);
                  }}
                  placeholder="输入以验证是否与当前配置一致"
                />
              </Field>
              {result.bundle.hasPassword2 && (
                <Field label="加密盐 (password2)">
                  <Input
                    mode="password"
                    aria-label="验证加密盐"
                    value={verifyPassword2}
                    disabled={verifying}
                    onChange={(v) => {
                      setVerifyPassword2(v);
                      setIncludeVerifiedPlaintext(false);
                    }}
                    placeholder="输入加密盐进行核对"
                  />
                </Field>
              )}
            </div>
            <div className="crypt-recovery-verify-actions">
              <Button
                size="small"
                type="secondary"
                loading={verifying}
                onClick={() => void handleVerify()}
              >
                验证密码是否匹配
              </Button>
              {result.verifyMessage && (
                <span
                  className={
                    result.passwordVerified === false ||
                    result.password2Verified === false
                      ? "inline-error"
                      : "crypt-recovery-verify-ok"
                  }
                >
                  {result.passwordVerified !== false &&
                    result.password2Verified !== false && (
                      <IconTickCircle aria-hidden="true" size="extra-small" />
                    )}{" "}
                  {result.verifyMessage}
                </span>
              )}
            </div>
            {canIncludePlaintext && (
              <Checkbox
                checked={includeVerifiedPlaintext}
                onChange={(e) =>
                  setIncludeVerifiedPlaintext(Boolean(e.target.checked))
                }
              >
                将已验证的明文密码一并写入本次导出的 JSON / TXT 恢复包（仅限保存在离线安全介质或密码管理器）
              </Checkbox>
            )}
          </div>

          <div className="crypt-recovery-snippet-box">
            <div className="crypt-recovery-section-title">
              rclone.conf 恢复模板与异机命令行
            </div>
            <pre className="crypt-recovery-pre">
              {result.bundle.rcloneSnippetTemplate}
            </pre>
            <div className="crypt-recovery-cmd">
              <span className="muted">异机还原命令：</span>
              <code>{result.bundle.rcloneCommand}</code>
            </div>
          </div>
        </div>
      ) : null}
    </Modal>
  );
}
