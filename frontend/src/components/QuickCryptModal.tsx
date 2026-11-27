import { useEffect, useMemo, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Modal from "@douyinfe/semi-ui/lib/es/modal";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconAlertTriangle,
  IconChevronRight,
  IconFolderStroked,
  IconInfoCircle,
  IconKey,
  IconLockStroked,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import { Field, errorToast } from "./common";
import { CryptRecoveryModal } from "./CryptRecoveryModal";
import { NativeStorageEditor } from "./NativeStorageEditor";
import {
  buildQuickCryptOptions,
  composeCryptUnderlyingRemote,
  normalizeRelativeSubDir,
  suggestCryptRemoteName,
  validateQuickCryptForm,
  type EncryptableDstTarget,
  type QuickCryptFormValues,
  type QuickCryptValidationErrors,
} from "../lib/quickCrypt";
import type { NativeRemoteItem } from "../types";

function createInitialValues(
  target: EncryptableDstTarget,
  remotes: readonly NativeRemoteItem[],
): QuickCryptFormValues {
  return {
    name: suggestCryptRemoteName(target.remoteName, target.subPath, remotes),
    subDir: "",
    password: "",
    confirmPassword: "",
    password2: "",
    filenameEncryption: "standard",
    filenameEncoding: "base32",
    directoryNameEncryption: "true",
    noDataEncryption: false,
  };
}

export function QuickCryptModal({
  targets,
  remotes,
  isEditingJob = false,
  mode = "task",
  requireSubDir = false,
  remoteTypeLabel,
  subDirPlaceholder,
  managedByJob,
  managedJobLabel,
  onClose,
  onReuseExisting,
  onOpenRestore,
  onCreated,
}: {
  targets: readonly EncryptableDstTarget[];
  remotes: readonly NativeRemoteItem[];
  isEditingJob?: boolean;
  mode?: "task" | "storage";
  requireSubDir?: boolean;
  remoteTypeLabel?: string;
  subDirPlaceholder?: string;
  managedByJob?: boolean;
  managedJobLabel?: string;
  onClose: () => void;
  onReuseExisting?: (result: {
    targetSyncPath: string;
    encryptedSyncPath: string;
    cryptName: string;
  }) => void;
  onOpenRestore?: (cryptName: string) => void;
  onCreated: (result: {
    targetSyncPath: string;
    encryptedSyncPath: string;
    cryptName: string;
  }) => Promise<void> | void;
}) {
  const isStorageMode = mode === "storage";
  const effectiveManagedByJob =
    managedByJob !== undefined ? managedByJob : !isStorageMode;
  const [selectedPath, setSelectedPath] = useState<string>(
    () => targets[0]?.syncPath ?? "",
  );
  const activeTarget = useMemo(
    () =>
      targets.find((item) => item.syncPath === selectedPath) ?? targets[0],
    [targets, selectedPath],
  );
  const [nameEdited, setNameEdited] = useState(false);
  const [values, setValues] = useState<QuickCryptFormValues>(() =>
    activeTarget
      ? createInitialValues(activeTarget, remotes)
      : {
          name: "crypt-vault",
          subDir: "",
          password: "",
          confirmPassword: "",
          password2: "",
          filenameEncryption: "standard",
          filenameEncoding: "base32",
          directoryNameEncryption: "true",
          noDataEncryption: false,
        },
  );
  const [errors, setErrors] = useState<QuickCryptValidationErrors>({});
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [recoveryRemoteName, setRecoveryRemoteName] = useState<string | null>(
    null,
  );
  const [browseRestorePath, setBrowseRestorePath] = useState<string | null>(
    null,
  );

  useEffect(() => {
    if (!activeTarget) return;
    if (selectedPath !== activeTarget.syncPath) {
      setSelectedPath(activeTarget.syncPath);
    }
  }, [activeTarget, selectedPath]);

  const handleSelectTarget = (nextSyncPath: string) => {
    setSelectedPath(nextSyncPath);
    const nextTarget = targets.find((item) => item.syncPath === nextSyncPath);
    if (nextTarget && !nameEdited) {
      setValues((prev) => ({
        ...prev,
        name: suggestCryptRemoteName(
          nextTarget.remoteName,
          nextTarget.subPath,
          remotes,
        ),
      }));
    }
    setErrors((prev) => ({ ...prev, name: undefined }));
  };

  const updateField = <K extends keyof QuickCryptFormValues>(
    key: K,
    value: QuickCryptFormValues[K],
  ) => {
    setValues((prev) => ({ ...prev, [key]: value }));
    if (key in errors) {
      setErrors((prev) => ({ ...prev, [key]: undefined }));
    }
  };

  const subDirCheck = useMemo(
    () => normalizeRelativeSubDir(values.subDir),
    [values.subDir],
  );

  const previewUnderlyingRemote = useMemo(() => {
    if (!activeTarget) return "";
    const extraSub = subDirCheck.error ? "" : subDirCheck.normalized;
    if (requireSubDir && !activeTarget.subPath && !extraSub) {
      return `${activeTarget.remoteName}:<待填写存储桶或共享路径>`;
    }
    return composeCryptUnderlyingRemote(
      activeTarget.remoteName,
      activeTarget.subPath,
      extraSub,
    );
  }, [activeTarget, requireSubDir, subDirCheck]);

  const activeAdvancedCount =
    (values.filenameEncryption !== "standard" ? 1 : 0) +
    (values.filenameEncoding !== "base32" ? 1 : 0) +
    (values.directoryNameEncryption !== "true" ? 1 : 0) +
    (values.noDataEncryption ? 1 : 0);

  const handleSubmit = async () => {
    if (!activeTarget || saving) return;
    const nextErrors = validateQuickCryptForm(values, remotes, {
      requireSubDir,
      subDirRequiredMessage: subDirPlaceholder
        ? `请填写存储桶或共享目录路径（${subDirPlaceholder}）`
        : undefined,
    });
    if (Object.keys(nextErrors).length > 0) {
      setErrors(nextErrors);
      const firstErr = Object.values(nextErrors).find(Boolean);
      if (firstErr) Toast.warning(firstErr);
      return;
    }

    const trimmedName = values.name.trim();
    const options = buildQuickCryptOptions(activeTarget, values, {
      managedByJob: effectiveManagedByJob,
      managedJobLabel,
    });
    setSaving(true);
    try {
      await api.saveNativeStorage({
        name: trimmedName,
        options,
      });
      await onCreated({
        targetSyncPath: activeTarget.syncPath,
        encryptedSyncPath: `/${trimmedName}`,
        cryptName: trimmedName,
      });
    } catch (err) {
      errorToast(err);
    } finally {
      setSaving(false);
    }
  };

  if (!activeTarget) return null;

  const existingCryptNamesOnStorage = isStorageMode
    ? Array.from(
        new Set([
          ...activeTarget.reusableCrypts.map((m) => m.cryptName),
          ...activeTarget.overlappingCrypts,
        ]),
      )
    : [];
  const primaryExistingCrypt =
    activeTarget.reusableCrypts[0]?.cryptName || existingCryptNamesOnStorage[0] || "";
  const triggerOpenRestore = (cryptName?: string) => {
    const targetPath = cryptName ? `/${cryptName}` : "/";
    if (onOpenRestore && cryptName) {
      onClose();
      onOpenRestore(cryptName);
      return;
    }
    setBrowseRestorePath(targetPath);
  };

  return (
    <Modal
      visible
      centered
      width={560}
      className="quick-crypt-modal"
      title={
        <span className="quick-crypt-modal-title">
          <IconLockStroked aria-hidden="true" />
          <span>{isStorageMode ? "创建加密存储" : "启用端到端加密"}</span>
        </span>
      }
      maskClosable={!saving}
      closeOnEsc={!saving}
      onCancel={() => {
        if (!saving) onClose();
      }}
      footer={
        <div className="editor-actions">
          <Button type="tertiary" disabled={saving} onClick={onClose}>
            取消
          </Button>
          <Button
            type="primary"
            theme="solid"
            loading={saving}
            onClick={() => void handleSubmit()}
          >
            {isStorageMode ? "创建加密存储" : "创建并启用加密"}
          </Button>
        </div>
      }
    >
      <div className="quick-crypt-body">
        {!isStorageMode &&
          onReuseExisting &&
          activeTarget.reusableCrypts.length > 0 && (
          <div className="quick-crypt-reuse-box" role="region" aria-label="复用已有加密存储">
            <div className="quick-crypt-reuse-head">
              <IconInfoCircle aria-hidden="true" size="small" />
              <span>检测到已有加密存储覆盖该目录，可直接切换复用：</span>
            </div>
            <div className="quick-crypt-reuse-list">
              {activeTarget.reusableCrypts.map((match) => (
                <div key={match.cryptName} className="quick-crypt-reuse-item">
                  <div className="quick-crypt-reuse-meta">
                    <div className="quick-crypt-reuse-name">
                      <strong>{match.cryptName}</strong>
                      <Tag size="small" color="green" type="light">
                        {match.exact ? "完全匹配" : "父目录覆盖"}
                      </Tag>
                    </div>
                    <div className="quick-crypt-reuse-sub">
                      底层 <code>{match.underlyingRemote}</code> → 目标将切换为{" "}
                      <code>{match.encryptedSyncPath}</code>
                    </div>
                  </div>
                  <Button
                    size="small"
                    type="primary"
                    theme="light"
                    disabled={saving}
                    onClick={() =>
                      onReuseExisting({
                        targetSyncPath: activeTarget.syncPath,
                        encryptedSyncPath: match.encryptedSyncPath,
                        cryptName: match.cryptName,
                      })
                    }
                  >
                    直接切换
                  </Button>
                </div>
              ))}
            </div>
          </div>
        )}

        {isStorageMode ? (
          <div className="quick-crypt-target-summary">
            <span className="quick-crypt-target-label">底层远程存储</span>
            <span className="quick-crypt-target-value">
              <code>{activeTarget.remoteName}</code>
              {remoteTypeLabel && (
                <Tag size="small" type="light" color="blue">
                  {remoteTypeLabel}
                </Tag>
              )}
            </span>
          </div>
        ) : targets.length > 1 ? (
          <Field label="要加密的目标路径">
            <Select
              aria-label="要加密的目标路径"
              value={activeTarget.syncPath}
              optionList={targets.map((item) => ({
                value: item.syncPath,
                label: `${item.syncPath}（${item.rcloneRemote}）`,
              }))}
              onChange={(v) => handleSelectTarget(String(v))}
              disabled={saving}
            />
          </Field>
        ) : (
          <div className="quick-crypt-target-summary">
            <span className="quick-crypt-target-label">当前目标路径</span>
            <code>{activeTarget.syncPath}</code>
          </div>
        )}

        {!isStorageMode && isEditingJob && (
          <div className="sync-method-banner is-warning" role="status">
            <IconAlertTriangle aria-hidden="true" size="small" />
            <span>
              正在编辑已有任务：如果目标目录中已存在明文文件，建议填写下方的「加密子目录」（如{" "}
              <code>encrypted</code>）将密文与原有明文隔离，避免混存。
            </span>
          </div>
        )}

        {isStorageMode && existingCryptNamesOnStorage.length > 0 && !subDirCheck.normalized && (
          <div className="sync-method-banner is-warning" role="status">
            <IconAlertTriangle aria-hidden="true" size="small" />
            <span>
              该存储已关联加密存储（{existingCryptNamesOnStorage.join("、")}
              ），建议填写独立的「{requireSubDir ? "存储桶 / 共享路径" : "加密子目录"}」避免与已有密文目录重叠。
            </span>
          </div>
        )}

        {!isStorageMode && activeTarget.overlappingCrypts.length > 0 && !subDirCheck.normalized && (
          <div className="sync-method-banner is-warning" role="status">
            <IconAlertTriangle aria-hidden="true" size="small" />
            <span>
              该目录下已存在加密存储（{activeTarget.overlappingCrypts.join("、")}
              ），建议填写独立的「加密子目录」避免与已有密文目录树重叠。
            </span>
          </div>
        )}

        <div className="form-grid form-grid--two">
          <Field
            label="加密存储名称"
            required
            error={errors.name}
            hint="保存后可在「存储管理」与同步任务中使用"
          >
            <Input
              aria-label="加密存储名称"
              value={values.name}
              disabled={saving}
              placeholder="例如 aliyun-crypt"
              onChange={(v) => {
                setNameEdited(true);
                updateField("name", v);
              }}
            />
          </Field>

          <Field
            label={requireSubDir ? "存储桶 / 共享路径" : "加密子目录（可选）"}
            required={requireSubDir}
            error={errors.subDir}
            hint={
              requireSubDir
                ? "对象存储或 SMB 需指定存储桶 / 共享名及可选子目录"
                : isStorageMode
                  ? "留空则加密整个存储根目录，建议填写独立子目录隔离密文"
                  : "留空则直接加密当前目标目录"
            }
          >
            <Input
              aria-label={requireSubDir ? "存储桶 / 共享路径" : "加密子目录"}
              value={values.subDir}
              disabled={saving}
              placeholder={
                subDirPlaceholder ||
                (requireSubDir ? "例如 my-bucket/encrypted" : "例如 encrypted")
              }
              onChange={(v) => updateField("subDir", v)}
            />
          </Field>
        </div>

        <div className="quick-crypt-mapping-preview">
          <span>
            底层写入位置：<code>{previewUnderlyingRemote}</code>
          </span>
          <span>
            {isStorageMode ? "加密存储路径：" : "任务目标将切换为："}
            <code>/{values.name.trim() || "加密存储名称"}</code>
          </span>
        </div>

        <div className="form-grid form-grid--two">
          <Field label="加密密码" required error={errors.password}>
            <Input
              aria-label="加密密码"
              mode="password"
              value={values.password}
              disabled={saving}
              placeholder="输入加密密码"
              onChange={(v) => updateField("password", v)}
            />
          </Field>
          <Field label="确认加密密码" required error={errors.confirmPassword}>
            <Input
              aria-label="确认加密密码"
              mode="password"
              value={values.confirmPassword}
              disabled={saving}
              placeholder="再次输入加密密码"
              onChange={(v) => updateField("confirmPassword", v)}
            />
          </Field>
        </div>

        <Field
          label="加密盐 password2（可选）"
          error={errors.password2}
          hint="可选，建议设置独立随机短语以进一步增强密钥强度"
        >
          <Input
            aria-label="加密盐"
            mode="password"
            value={values.password2}
            disabled={saving}
            placeholder="可选，留空使用默认盐"
            onChange={(v) => updateField("password2", v)}
          />
        </Field>

        <div className="advanced-options">
          <button
            type="button"
            className={`advanced-options-toggle${advancedOpen ? " is-open" : ""}`}
            aria-expanded={advancedOpen}
            disabled={saving}
            onClick={() => setAdvancedOpen((open) => !open)}
          >
            <IconChevronRight
              className="advanced-options-chevron"
              aria-hidden="true"
            />
            <span>高级加密选项</span>
            {activeAdvancedCount > 0 && (
              <Tag size="small" color="blue" type="light">
                已定制 {activeAdvancedCount} 项
              </Tag>
            )}
          </button>
          {advancedOpen && (
            <div className="advanced-options-body">
              <div className="form-grid form-grid--two">
                <Field label="文件名加密方式">
                  <Select
                    aria-label="文件名加密方式"
                    value={values.filenameEncryption}
                    disabled={saving}
                    optionList={[
                      { value: "standard", label: "standard（AES 加密，默认）" },
                      { value: "obfuscate", label: "obfuscate（轻量混淆）" },
                      { value: "off", label: "off（不加密文件名）" },
                    ]}
                    onChange={(v) =>
                      updateField(
                        "filenameEncryption",
                        String(v) as QuickCryptFormValues["filenameEncryption"],
                      )
                    }
                  />
                </Field>
                <Field label="文件名编码">
                  <Select
                    aria-label="文件名编码"
                    value={values.filenameEncoding}
                    disabled={saving}
                    optionList={[
                      { value: "base32", label: "base32（默认，通用）" },
                      { value: "base64", label: "base64（更短文件名）" },
                      {
                        value: "base32768",
                        label: "base32768（最短，需 Unicode 支持）",
                      },
                    ]}
                    onChange={(v) =>
                      updateField(
                        "filenameEncoding",
                        String(v) as QuickCryptFormValues["filenameEncoding"],
                      )
                    }
                  />
                </Field>
              </div>
              <Field label="目录名加密">
                <Select
                  aria-label="目录名加密"
                  value={values.directoryNameEncryption}
                  disabled={saving}
                  optionList={[
                    { value: "true", label: "开启（默认）" },
                    { value: "false", label: "关闭" },
                  ]}
                  onChange={(v) =>
                    updateField(
                      "directoryNameEncryption",
                      String(v) as QuickCryptFormValues["directoryNameEncryption"],
                    )
                  }
                />
              </Field>
              <Checkbox
                checked={values.noDataEncryption}
                disabled={saving}
                onChange={(e) =>
                  updateField("noDataEncryption", Boolean(e.target.checked))
                }
              >
                仅加密文件名、不加密文件内容（no_data_encryption）
              </Checkbox>
            </div>
          )}
        </div>

        <div className="sync-method-banner is-warning" role="status">
          <IconAlertTriangle aria-hidden="true" size="small" />
          <span>
            {isStorageMode
              ? "请妥善保管加密密码与盐，丢失后将无法解密云端文件。创建后可在「存储管理」中导出密钥恢复包，并在同步任务中选作源或目标路径。"
              : "请妥善保管加密密码与盐，丢失后将无法解密云端文件。创建后可在「存储管理」或「任务专属加密通道」中导出密钥恢复包，或直接从加密存储一键还原到本地。"}
          </span>
        </div>

        <div className="quick-crypt-guidance-bar" role="region" aria-label="密钥恢复与解密还原引导">
          <Button
            size="small"
            type="tertiary"
            icon={<IconKey aria-hidden="true" />}
            disabled={saving}
            onClick={() => {
              if (primaryExistingCrypt) {
                setRecoveryRemoteName(primaryExistingCrypt);
              } else {
                Toast.info(
                  "请先点击下方按钮完成加密存储创建，随后即可导出完整的密钥恢复包（包含加密参数与异机还原命令）",
                );
              }
            }}
          >
            导出密钥恢复包
          </Button>
          <Button
            size="small"
            type="tertiary"
            icon={<IconFolderStroked aria-hidden="true" />}
            disabled={saving}
            onClick={() => triggerOpenRestore(primaryExistingCrypt || undefined)}
          >
            从加密存储一键还原到本地
          </Button>
        </div>
      </div>

      {recoveryRemoteName && (
        <CryptRecoveryModal
          remoteName={recoveryRemoteName}
          onClose={() => setRecoveryRemoteName(null)}
          onOpenRestore={(name) => triggerOpenRestore(name)}
        />
      )}

      {browseRestorePath !== null && (
        <NativeStorageEditor
          mode="browse"
          initialBrowsePath={browseRestorePath}
          onClose={() => setBrowseRestorePath(null)}
        />
      )}
    </Modal>
  );
}
