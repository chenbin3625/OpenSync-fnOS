import { useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconArchive,
  IconChevronRight,
  IconCodeStroked,
  IconDesktop,
  IconFile,
  IconImageStroked,
  IconMusicNoteStroked,
  IconPlusStroked,
  IconVideoStroked,
} from "@douyinfe/semi-icons";
import {
  addCustomFileTypeFilter,
  fileTypeFilterGroups,
  isExcludePatternEnabled,
  parseOtherFileTypeFilters,
  removeCustomFileTypeFilter,
  updateExcludePatterns,
  validateCustomFilePattern,
} from "../lib/taskForm";

function GroupIcon({ groupKey }: { groupKey: string }) {
  switch (groupKey) {
    case "system":
      return <IconDesktop aria-hidden="true" />;
    case "audio":
      return <IconMusicNoteStroked aria-hidden="true" />;
    case "video":
      return <IconVideoStroked aria-hidden="true" />;
    case "image":
      return <IconImageStroked aria-hidden="true" />;
    case "archive":
      return <IconArchive aria-hidden="true" />;
    case "executable":
      return <IconCodeStroked aria-hidden="true" />;
    default:
      return <IconFile aria-hidden="true" />;
  }
}

export function FileTypeFilter({
  value,
  onChange,
  id,
  "aria-label": ariaLabel,
  "aria-labelledby": ariaLabelledBy,
}: {
  value: string;
  onChange: (value: string) => void;
  id?: string;
  "aria-label"?: string;
  "aria-labelledby"?: string;
}) {
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [otherExpanded, setOtherExpanded] = useState(true);
  const [customInput, setCustomInput] = useState("");
  const [customError, setCustomError] = useState("");
  const otherFilters = parseOtherFileTypeFilters(value).filter(
    (item) => item.enabled,
  );

  const toggleExpanded = (key: string) => {
    setExpanded((current) => {
      const next = new Set(current);
      next.has(key) ? next.delete(key) : next.add(key);
      return next;
    });
  };
  const addCustom = () => {
    const { pattern, error } = validateCustomFilePattern(customInput);
    if (!pattern) {
      setCustomError(error || "请输入有效的文件名或文件后缀");
      return;
    }
    setCustomError("");
    if (isExcludePatternEnabled(value, pattern)) {
      Toast.info("该规则已启用");
      setCustomInput("");
      return;
    }
    const nextValue = addCustomFileTypeFilter(value, pattern);
    const matchedPreset = fileTypeFilterGroups.find((group) =>
      group.patterns.some(
        (preset) => preset.toLowerCase() === pattern.toLowerCase(),
      ),
    );
    onChange(nextValue);
    setCustomInput("");
    if (matchedPreset) {
      Toast.success(`已启用「${matchedPreset.label}」中的预设规则 ${pattern}`);
    } else {
      setOtherExpanded(true);
    }
  };

  const activePresetCount = fileTypeFilterGroups.reduce(
    (sum, group) =>
      sum +
      group.patterns.filter((pattern) =>
        isExcludePatternEnabled(value, pattern),
      ).length,
    0,
  );
  const totalActiveCount = activePresetCount + otherFilters.length;

  return (
    <div
      className="file-type-filter"
      id={id}
      // A labelled plain div has no accessible role, so its name was never
      // announced; group makes the Field label apply to the checkbox set.
      role="group"
      aria-label={ariaLabel}
      aria-labelledby={ariaLabelledBy}
    >
      <div className="file-type-summary">
        <span className="muted">
          {totalActiveCount > 0
            ? `已启用 ${totalActiveCount} 条文件排除规则`
            : "当前未启用文件类型过滤"}
        </span>
      </div>
      <div className="file-type-groups">
        {fileTypeFilterGroups.map((group) => {
          const selected = group.patterns.filter((pattern) =>
            isExcludePatternEnabled(value, pattern),
          );
          const allSelected = selected.length === group.patterns.length;
          const isExpanded = expanded.has(group.key);
          return (
            <div className="file-type-group" key={group.key}>
              <div className="file-type-group-row">
                <Button
                  className={`file-type-expand${isExpanded ? " expanded" : ""}`}
                  size="small"
                  type="tertiary"
                  theme="borderless"
                  icon={<IconChevronRight aria-hidden="true" />}
                  aria-label={`${isExpanded ? "收起" : "展开"}${group.label}`}
                  aria-expanded={isExpanded}
                  onClick={() => toggleExpanded(group.key)}
                />
                <Checkbox
                  aria-label={group.label}
                  checked={allSelected}
                  indeterminate={selected.length > 0 && !allSelected}
                  onChange={(event) =>
                    onChange(
                      updateExcludePatterns(
                        value,
                        group.patterns,
                        Boolean(event.target.checked),
                      ),
                    )
                  }
                />
                <span className={`file-type-icon ${group.key}`}>
                  <GroupIcon groupKey={group.key} />
                </span>
                <button
                  className="file-type-group-label"
                  type="button"
                  onClick={() => toggleExpanded(group.key)}
                >
                  <span>{group.label}</span>
                  <span
                    className={`file-type-count${selected.length > 0 ? " is-active" : ""}`}
                  >
                    {selected.length}/{group.patterns.length}
                  </span>
                </button>
              </div>
              {isExpanded && (
                <div
                  className="file-type-patterns"
                  role="group"
                  aria-label={`${group.label}文件规则`}
                >
                  {group.patterns.map((pattern) => (
                    <Checkbox
                      key={pattern}
                      aria-label={pattern}
                      checked={isExcludePatternEnabled(value, pattern)}
                      onChange={(event) =>
                        onChange(
                          updateExcludePatterns(
                            value,
                            [pattern],
                            Boolean(event.target.checked),
                          ),
                        )
                      }
                    >
                      {pattern}
                    </Checkbox>
                  ))}
                </div>
              )}
            </div>
          );
        })}
        {otherFilters.length > 0 && (
          <div className="file-type-group" key="other">
            <div className="file-type-group-row">
              <Button
                className={`file-type-expand${otherExpanded ? " expanded" : ""}`}
                size="small"
                type="tertiary"
                theme="borderless"
                icon={<IconChevronRight aria-hidden="true" />}
                aria-label={`${otherExpanded ? "收起" : "展开"}其他`}
                aria-expanded={otherExpanded}
                onClick={() => setOtherExpanded((current) => !current)}
              />
              <span className="file-type-group-spacer" aria-hidden="true" />
              <span className="file-type-icon other">
                <IconFile aria-hidden="true" />
              </span>
              <button
                className="file-type-group-label"
                type="button"
                onClick={() => setOtherExpanded((current) => !current)}
              >
                <span>自定义规则</span>
                <span className="file-type-count is-active">
                  {otherFilters.length} 条
                </span>
              </button>
            </div>
            {otherExpanded && (
              <div
                className="custom-file-tags"
                role="group"
                aria-label="自定义文件规则列表"
              >
                {otherFilters.map((item) => (
                  <Tag
                    key={item.pattern}
                    className="custom-file-tag"
                    color="blue"
                    type="light"
                    size="large"
                    closable
                    aria-label={`已添加规则 ${item.pattern}`}
                    onClose={(_tag, event) => {
                      event.preventDefault();
                      onChange(removeCustomFileTypeFilter(value, item.pattern));
                    }}
                  >
                    {item.pattern}
                  </Tag>
                ))}
              </div>
            )}
          </div>
        )}
      </div>
      <div className="file-type-add-block">
        <div className="file-type-add">
          <Input
            value={customInput}
            aria-label="自定义文件规则"
            placeholder="文件名或文件后缀，如 ipc-socket、a.bcd、*.log"
            onChange={(next) => {
              setCustomInput(next);
              if (customError) setCustomError("");
            }}
            onEnterPress={addCustom}
          />
          <Button
            type="primary"
            theme="solid"
            icon={<IconPlusStroked aria-hidden="true" />}
            aria-label="新增文件规则"
            onClick={addCustom}
            disabled={!customInput.trim()}
          >
            新增
          </Button>
        </div>
        {customError && (
          <div className="field-error" role="alert">
            {customError}
          </div>
        )}
      </div>
    </div>
  );
}
