import { useMemo } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import { IconAlertTriangle } from "@douyinfe/semi-icons";
import {
  buildSyncPathPreview,
  formatDisplayPath,
  type ExistingJobPathSummary,
  type SyncPathPreviewItem,
} from "../lib/syncPathPreview";
import { Field, HelpTip } from "./common";

const syncPreviewHelpTip = (
  <div className="sync-method-tip">
    <div>
      <strong>单个源目录</strong>：目录内文件直接同步到目标目录下，不额外建源目录同名文件夹。
    </div>
    <div>
      <strong>多个源目录（有共同父目录）</strong>：自动省略共同父目录，保留其后的相对目录层级。
    </div>
    <div>
      <strong>多个源目录（无共同父目录）</strong>：按末级目录名建子目录；若末级同名冲突，同名项保留完整路径。
    </div>
    <div>
      <strong>多个目标目录</strong>：每个源目录分别按以上规则同步到各个目标目录。
    </div>
  </div>
);

function PreviewRow({
  item,
  siblingPaths,
  showDirectExample,
}: {
  item: SyncPathPreviewItem;
  siblingPaths: readonly string[];
  showDirectExample: boolean;
}) {
  const displaySrcDir = formatDisplayPath(item.srcDir, siblingPaths);
  const displayDstBasePrefix = formatDisplayPath(
    item.dstBasePrefix,
    siblingPaths,
  );
  const displayResolvedDst = formatDisplayPath(
    item.resolvedDst,
    siblingPaths,
  );
  return (
    <div
      className={`sync-path-preview-item${item.hasConflict ? " is-conflict" : ""}`}
    >
      <div className="sync-path-preview-row">
        <span className="sync-path-preview-role">源</span>
        <span className="sync-path-preview-path">{displaySrcDir}</span>
      </div>
      <div className="sync-path-preview-row sync-path-preview-row--dst">
        <span className="sync-path-preview-role sync-path-preview-role--dst">
          至
        </span>
        <span className="sync-path-preview-path">
          <span className="sync-path-preview-dst-base">
            {displayDstBasePrefix}
          </span>
          {item.appendedSuffix && (
            <mark className="sync-path-preview-dst-suffix">
              {item.appendedSuffix}
            </mark>
          )}
        </span>
        {item.preservedFromStoredMap && (
          <span className="sync-path-preview-tag">沿用原位置</span>
        )}
      </div>
      {showDirectExample && (
        <div className="sync-path-preview-example">
          例：{displaySrcDir}a.jpg → {displayResolvedDst}a.jpg
        </div>
      )}
    </div>
  );
}

export function SyncPathPreview({
  srcPaths,
  dstPaths,
  storedDstMap,
  method = 0,
  existingJobs,
  currentJobId,
  engineId,
  onKeepFirstDst,
}: {
  srcPaths: readonly string[];
  dstPaths: readonly string[];
  storedDstMap?: string | null;
  method?: number;
  existingJobs?: readonly ExistingJobPathSummary[];
  currentJobId?: number;
  engineId?: number;
  onKeepFirstDst?: () => void;
}) {
  const preview = useMemo(
    () =>
      buildSyncPathPreview({
        srcPaths,
        dstPaths,
        storedDstMap,
        method,
        existingJobs,
        currentJobId,
        engineId,
      }),
    [srcPaths, dstPaths, storedDstMap, method, existingJobs, currentJobId, engineId],
  );
  const allPaths = useMemo(() => [...srcPaths, ...dstPaths], [srcPaths, dstPaths]);
  const multipleDestinations = preview.groups.length > 1;
  const singleDirectRoot =
    preview.rule === "single-src" && preview.items.length > 0;
  const showKeepFirstDstAction =
    (method === 2 || method === 3) && dstPaths.length > 1 && Boolean(onKeepFirstDst);

  return (
    <Field
      label={
        <span className="field-label-with-tip">
          <span>同步位置预览</span>
          <HelpTip content={syncPreviewHelpTip} label="同步位置预览说明" />
        </span>
      }
      ariaLabel="同步位置预览"
    >
      <div
        role="region"
        className={`sync-path-preview${preview.items.length === 0 ? " is-empty" : ""}${preview.conflicts.length > 0 ? " has-conflict" : ""}`}
        aria-live="polite"
      >
        {preview.items.length === 0 ? (
          <div className="sync-path-preview-empty">{preview.summary}</div>
        ) : (
          <>
            <div className="sync-path-preview-head">
              <span className="sync-path-preview-summary">
                {preview.summary}
              </span>
              <span className="sync-path-preview-count">
                共 {preview.items.length} 组
              </span>
            </div>
            {preview.hasPreservedMapping && (
              <div className="sync-path-preview-note">
                未修改目标路径时，已有源目录会保持原目标位置；如需按新规则重新分配，请调整目标路径。
              </div>
            )}
            {preview.conflicts.length > 0 && (
              <div className="sync-path-preview-alert" role="alert">
                <IconAlertTriangle aria-hidden="true" size="small" />
                <div className="sync-path-preview-alert-list">
                  {preview.conflicts.map((message) => (
                    <div key={message}>{message}</div>
                  ))}
                  {showKeepFirstDstAction && (
                    <div className="sync-path-preview-alert-action">
                      <Button
                        size="small"
                        type="danger"
                        theme="borderless"
                        onClick={onKeepFirstDst}
                      >
                        仅保留首个目标目录
                      </Button>
                    </div>
                  )}
                </div>
              </div>
            )}
            <div className="sync-path-preview-list">
              {multipleDestinations
                ? preview.groups.map((group) => (
                    <div
                      className="sync-path-preview-group"
                      key={group.dstBaseDir}
                    >
                      <div className="sync-path-preview-group-title">
                        <span>目标目录</span>
                        <span className="sync-path-preview-group-path">
                          {formatDisplayPath(group.dstBaseDir, allPaths)}
                        </span>
                      </div>
                      {group.items.map((item) => (
                        <PreviewRow
                          key={item.key}
                          item={item}
                          siblingPaths={allPaths}
                          showDirectExample={
                            singleDirectRoot &&
                            !item.appendedSuffix &&
                            !item.hasConflict
                          }
                        />
                      ))}
                    </div>
                  ))
                : preview.items.map((item) => (
                    <PreviewRow
                      key={item.key}
                      item={item}
                      siblingPaths={allPaths}
                      showDirectExample={
                        singleDirectRoot &&
                        !item.appendedSuffix &&
                        !item.hasConflict
                      }
                    />
                  ))}
            </div>
          </>
        )}
      </div>
    </Field>
  );
}
