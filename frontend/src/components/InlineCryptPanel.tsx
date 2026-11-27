import Input from "@douyinfe/semi-ui/lib/es/input";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Switch from "@douyinfe/semi-ui/lib/es/switch";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import { IconChevronRight, IconInfoCircle, IconLockStroked } from "@douyinfe/semi-icons";
import { Field } from "./common";
import {
  canCreateCryptFromRemote,
  composeCryptUnderlyingRemote,
  findReusableCryptRemotes,
  isContainerRequiredBackend,
  normalizeRelativeSubDir,
  suggestCryptRemoteName,
  validateQuickCryptForm,
  type QuickCryptFormValues,
  type QuickCryptValidationErrors,
} from "../lib/quickCrypt";
import { resolveRemoteChain } from "../lib/taskForm";
import { extractRemoteRootAndSubPath } from "../lib/taskWizard";
import type { NativeRemoteItem, SyncDirectionMode } from "../types";

export interface InlineCryptDraft {
  enabled: boolean;
  mode: "create" | "reuse";
  reuseCryptName: string;
  reuseEncryptedPath: string;
  values: QuickCryptFormValues;
  showAdvanced: boolean;
}

export function createInitialInlineCryptDraft(
  remotePath: string,
  remotes?: readonly NativeRemoteItem[],
): InlineCryptDraft {
  const { remoteName, subPath } = extractRemoteRootAndSubPath(remotePath);
  const existing = remotes || [];
  const suggestedName = remoteName
    ? suggestCryptRemoteName(remoteName, subPath, existing)
    : "sync-crypt";
  const reusable = remoteName
    ? findReusableCryptRemotes(remoteName, subPath, existing).reusable
    : [];
  const firstReusable = reusable[0];

  return {
    enabled: false,
    mode: "create",
    reuseCryptName: firstReusable?.cryptName ?? "",
    reuseEncryptedPath: firstReusable?.encryptedSyncPath ?? "",
    values: {
      name: suggestedName,
      subDir: "",
      password: "",
      confirmPassword: "",
      password2: "",
      filenameEncryption: "standard",
      filenameEncoding: "base32",
      directoryNameEncryption: "true",
      noDataEncryption: false,
    },
    showAdvanced: false,
  };
}

export function syncInlineCryptTarget(
  prev: InlineCryptDraft,
  remotePath: string,
  remotes?: readonly NativeRemoteItem[],
): InlineCryptDraft {
  const { remoteName, subPath } = extractRemoteRootAndSubPath(remotePath);
  const existing = remotes || [];
  const reusable = remoteName
    ? findReusableCryptRemotes(remoteName, subPath, existing).reusable
    : [];
  const firstReusable = reusable[0];
  const keepReuse = reusable.find((r) => r.cryptName === prev.reuseCryptName);

  return {
    ...prev,
    mode:
      prev.mode === "reuse" && !firstReusable ? "create" : prev.mode,
    reuseCryptName: keepReuse?.cryptName ?? firstReusable?.cryptName ?? "",
    reuseEncryptedPath:
      keepReuse?.encryptedSyncPath ?? firstReusable?.encryptedSyncPath ?? "",
    values: {
      ...prev.values,
      name: remoteName
        ? suggestCryptRemoteName(remoteName, subPath, existing)
        : prev.values.name,
    },
  };
}

export interface InlineCryptTargetAnalysis {
  remoteName: string;
  subPath: string;
  remoteItem?: NativeRemoteItem;
  alreadyEncrypted: boolean;
  readOnly: boolean;
  supported: boolean;
  requiresSubDir: boolean;
  reusable: ReturnType<typeof findReusableCryptRemotes>["reusable"];
  overlapping: string[];
}

export function analyzeInlineCryptTarget(
  remotePath: string,
  remotes?: readonly NativeRemoteItem[],
): InlineCryptTargetAnalysis {
  const list = remotes || [];
  const { remoteName, subPath } = extractRemoteRootAndSubPath(remotePath);
  const remoteItem = list.find(
    (r) => r.name.toLowerCase() === remoteName.toLowerCase(),
  );
  if (!remoteName || !remoteItem) {
    return {
      remoteName,
      subPath,
      remoteItem: undefined,
      alreadyEncrypted: false,
      readOnly: false,
      supported: false,
      requiresSubDir: false,
      reusable: [],
      overlapping: [],
    };
  }
  const chain = resolveRemoteChain(remoteName, list, new Set(), subPath);
  const alreadyEncrypted = chain.hasCrypt;
  const readOnly = chain.readOnly;
  const supported =
    !alreadyEncrypted &&
    !readOnly &&
    canCreateCryptFromRemote(remoteItem, list);
  const requiresSubDir =
    isContainerRequiredBackend(remoteItem.type) && !subPath;
  const matched = findReusableCryptRemotes(remoteName, subPath, list);

  return {
    remoteName,
    subPath,
    remoteItem,
    alreadyEncrypted,
    readOnly,
    supported,
    requiresSubDir,
    reusable: matched.reusable,
    overlapping: matched.overlapping,
  };
}

export function validateInlineCryptDraft(
  draft: InlineCryptDraft,
  remotePath: string,
  direction: SyncDirectionMode,
  remotes?: readonly NativeRemoteItem[],
): QuickCryptValidationErrors & { _form?: string } {
  if (!draft.enabled || direction === "download") return {};
  const target = analyzeInlineCryptTarget(remotePath, remotes);
  if (target.alreadyEncrypted) return {};
  if (!target.supported) {
    return { _form: "当前所选云端存储暂不支持内联创建加密包装" };
  }
  if (draft.mode === "reuse") {
    if (!draft.reuseCryptName || !draft.reuseEncryptedPath) {
      return { _form: "请选择要复用的已有加密存储" };
    }
    return {};
  }
  return validateQuickCryptForm(draft.values, remotes || [], {
    requireSubDir: target.requiresSubDir,
  });
}

export function InlineCryptPanel({
  draft,
  onChange,
  remotePath,
  direction,
  remotes,
  showErrors,
  createdCryptName,
}: {
  draft: InlineCryptDraft;
  onChange: (next: InlineCryptDraft) => void;
  remotePath: string;
  direction: SyncDirectionMode;
  remotes?: readonly NativeRemoteItem[];
  showErrors: boolean;
  createdCryptName?: string;
}) {
  const target = analyzeInlineCryptTarget(remotePath, remotes);
  const errors = showErrors
    ? validateInlineCryptDraft(draft, remotePath, direction, remotes)
    : {};

  if (createdCryptName && target.alreadyEncrypted) {
    return (
      <div className="sync-method-banner is-success" role="status">
        <IconLockStroked aria-hidden="true" size="small" />
        <span>
          已为本任务创建并启用端到端加密存储「<strong>{createdCryptName}</strong>
          」，目标目录为 <code>{remotePath}</code>。
        </span>
      </div>
    );
  }

  if (target.alreadyEncrypted) {
    return (
      <div className="sync-method-banner is-success" role="status">
        <IconLockStroked aria-hidden="true" size="small" />
        <span>
          所选云端目录（<code>{remotePath}</code>）已位于 Crypt 加密存储中，文件在传输前于本地自动加解密。
        </span>
      </div>
    );
  }

  if (direction === "download") {
    return (
      <div className="sync-method-banner" role="status">
        <IconInfoCircle aria-hidden="true" size="small" />
        <span>
          「仅下载云端更改」模式的目标端为 NAS 本地目录；若需从加密网盘解密下载，请在第 1 步直接选择已有的加密存储文件夹。
        </span>
      </div>
    );
  }

  if (!target.remoteItem || !target.supported) {
    return (
      <div className="sync-method-banner" role="status">
        <IconInfoCircle aria-hidden="true" size="small" />
        <span>
          {target.readOnly
            ? "当前所选云端存储为只读存储，无法创建端到端加密包装。"
            : "当前所选云端目录暂不支持内联创建加密包装（可在「存储管理」中管理独立加密存储）。"}
        </span>
      </div>
    );
  }

  const updateValues = <K extends keyof QuickCryptFormValues>(
    key: K,
    val: QuickCryptFormValues[K],
  ) => {
    onChange({
      ...draft,
      values: {
        ...draft.values,
        [key]: val,
      },
    });
  };

  const normalizedSub = normalizeRelativeSubDir(draft.values.subDir);
  const underlyingRemotePreview = composeCryptUnderlyingRemote(
    target.remoteName,
    target.subPath,
    normalizedSub.error ? draft.values.subDir.trim() : normalizedSub.normalized,
  );

  return (
    <div className="inline-crypt-panel">
      <div className="inline-crypt-head">
        <div className="inline-crypt-title-copy">
          <div className="inline-crypt-title">
            <IconLockStroked aria-hidden="true" size="small" />
            <span>目标端启用端到端加密</span>
          </div>
          <div className="muted inline-crypt-subtitle">
            在 NAS 本地使用 Rclone Crypt 加密文件内容与目录结构后再上传，网盘服务端无法读取明文；保存任务时自动创建并关联加密存储。
          </div>
        </div>
        <Switch
          aria-label="目标端启用端到端加密"
          checked={draft.enabled}
          onChange={(checked) =>
            onChange({
              ...draft,
              enabled: checked,
            })
          }
        />
      </div>

      {draft.enabled && (
        <div className="inline-crypt-body">
          {target.reusable.length > 0 && (
            <div className="inline-crypt-reuse-box">
              <div
                className="inline-crypt-mode-tabs"
                role="radiogroup"
                aria-label="加密存储方式"
              >
                <button
                  type="button"
                  role="radio"
                  aria-checked={draft.mode === "create"}
                  className={`schedule-chip${draft.mode === "create" ? " is-active" : ""}`}
                  onClick={() => onChange({ ...draft, mode: "create" })}
                >
                  新建任务专属加密存储
                </button>
                <button
                  type="button"
                  role="radio"
                  aria-checked={draft.mode === "reuse"}
                  className={`schedule-chip${draft.mode === "reuse" ? " is-active" : ""}`}
                  onClick={() => onChange({ ...draft, mode: "reuse" })}
                >
                  复用已有加密存储 ({target.reusable.length})
                </button>
              </div>
            </div>
          )}

          {draft.mode === "reuse" && target.reusable.length > 0 ? (
            <div className="inline-crypt-reuse-list">
              <Field label="选择已有加密存储" error={errors._form}>
                <Select
                  aria-label="选择复用的加密存储"
                  value={draft.reuseCryptName}
                  optionList={target.reusable.map((item) => ({
                    value: item.cryptName,
                    label: `${item.cryptName}（目标将切换为 ${item.encryptedSyncPath}）`,
                  }))}
                  onChange={(v) => {
                    const chosen = target.reusable.find(
                      (r) => r.cryptName === String(v),
                    );
                    if (chosen) {
                      onChange({
                        ...draft,
                        mode: "reuse",
                        reuseCryptName: chosen.cryptName,
                        reuseEncryptedPath: chosen.encryptedSyncPath,
                      });
                    }
                  }}
                />
              </Field>
              <div className="inline-crypt-preview" role="status">
                <span>保存任务后，云端路径将自动切换为：</span>
                <strong>{draft.reuseEncryptedPath}</strong>
                <Tag size="small" color="green" type="light">
                  复用已有配置
                </Tag>
              </div>
            </div>
          ) : (
            <div className="inline-crypt-create-form">
              <div className="form-grid form-grid--two">
                <Field
                  label="加密密码"
                  required
                  error={errors.password}
                  hint="请牢记密码；丢失密码将无法解密云端文件"
                >
                  <Input
                    mode="password"
                    autoComplete="new-password"
                    aria-label="加密密码"
                    placeholder="输入端到端加密密码"
                    value={draft.values.password}
                    onChange={(v) => updateValues("password", v)}
                  />
                </Field>
                <Field
                  label="确认密码"
                  required
                  error={errors.confirmPassword}
                >
                  <Input
                    mode="password"
                    autoComplete="new-password"
                    aria-label="确认密码"
                    placeholder="再次输入以确认"
                    value={draft.values.confirmPassword}
                    onChange={(v) => updateValues("confirmPassword", v)}
                  />
                </Field>
              </div>

              <div className="form-grid form-grid--two">
                <Field
                  label="加密存储名称"
                  required
                  error={errors.name}
                  hint="保存任务时自动在后端注册为 Crypt 包装存储"
                >
                  <Input
                    aria-label="加密存储名称"
                    value={draft.values.name}
                    onChange={(v) => updateValues("name", v)}
                  />
                </Field>
                {target.requiresSubDir && (
                  <Field
                    label="存储桶 / 子目录"
                    required
                    error={errors.subDir}
                    hint="对象存储 / 共享根目录需指定子目录"
                  >
                    <Input
                      aria-label="加密子目录"
                      placeholder="例如 encrypted-bucket/vault"
                      value={draft.values.subDir}
                      onChange={(v) => updateValues("subDir", v)}
                    />
                  </Field>
                )}
              </div>

              <div className="inline-crypt-advanced">
                <button
                  type="button"
                  className={`advanced-options-toggle inline-crypt-adv-toggle${draft.showAdvanced ? " is-open" : ""}`}
                  aria-expanded={draft.showAdvanced}
                  onClick={() =>
                    onChange({
                      ...draft,
                      showAdvanced: !draft.showAdvanced,
                    })
                  }
                >
                  <IconChevronRight
                    className="advanced-options-chevron"
                    aria-hidden="true"
                  />
                  <span>密码盐与文件名加密设置（可选）</span>
                </button>
                {draft.showAdvanced && (
                  <div className="inline-crypt-adv-body">
                    <div className="form-grid form-grid--two">
                      <Field
                        label="密码盐（Salt，可选）"
                        error={errors.password2}
                        hint="用于增强密钥派生的第二密码"
                      >
                        <Input
                          mode="password"
                          autoComplete="new-password"
                          aria-label="密码盐"
                          placeholder="留空则不使用独立密码盐"
                          value={draft.values.password2}
                          onChange={(v) => updateValues("password2", v)}
                        />
                      </Field>
                      <Field label="文件名加密方式">
                        <Select
                          aria-label="文件名加密方式"
                          value={draft.values.filenameEncryption}
                          optionList={[
                            { label: "标准加密（standard，推荐）", value: "standard" },
                            { label: "混淆加密（obfuscate，较短路径）", value: "obfuscate" },
                            { label: "不加密文件名（off，仅加密文件内容）", value: "off" },
                          ]}
                          onChange={(v) =>
                            updateValues(
                              "filenameEncryption",
                              String(v) as QuickCryptFormValues["filenameEncryption"],
                            )
                          }
                        />
                      </Field>
                    </div>
                    <div className="form-grid form-grid--two">
                      <Field label="目录名加密">
                        <Select
                          aria-label="目录名加密"
                          value={draft.values.directoryNameEncryption}
                          optionList={[
                            { label: "加密目录名（推荐）", value: "true" },
                            { label: "保留原始目录名", value: "false" },
                          ]}
                          onChange={(v) =>
                            updateValues(
                              "directoryNameEncryption",
                              String(v) as QuickCryptFormValues["directoryNameEncryption"],
                            )
                          }
                        />
                      </Field>
                      {!target.requiresSubDir && (
                        <Field
                          label="加密子目录（可选）"
                          error={errors.subDir}
                          hint="留空则直接加密所选云端目录"
                        >
                          <Input
                            aria-label="加密子目录"
                            placeholder="例如 encrypted"
                            value={draft.values.subDir}
                            onChange={(v) => updateValues("subDir", v)}
                          />
                        </Field>
                      )}
                    </div>
                  </div>
                )}
              </div>

              <div className="inline-crypt-preview" role="status">
                <span>
                  底层写入 <code>{underlyingRemotePreview}</code> · 保存后任务云端路径将自动切换为{" "}
                  <strong>/{draft.values.name.trim() || "..."}</strong>
                </span>
              </div>
              {errors._form && (
                <div className="field-error" role="alert">
                  {errors._form}
                </div>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
