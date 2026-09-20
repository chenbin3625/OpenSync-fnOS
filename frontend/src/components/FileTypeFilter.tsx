import { useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconArchive,
  IconChevronRight,
  IconDeleteStroked,
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
} from "../lib/taskForm";

function GroupIcon({ groupKey }: { groupKey: string }) {
  switch (groupKey) {
    case "audio":
      return <IconMusicNoteStroked aria-hidden="true" />;
    case "video":
      return <IconVideoStroked aria-hidden="true" />;
    case "image":
      return <IconImageStroked aria-hidden="true" />;
    case "archive":
      return <IconArchive aria-hidden="true" />;
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
  const otherFilters = parseOtherFileTypeFilters(value);

  const toggleExpanded = (key: string) => {
    setExpanded((current) => {
      const next = new Set(current);
      next.has(key) ? next.delete(key) : next.add(key);
      return next;
    });
  };
  const addCustom = () => {
    const next = addCustomFileTypeFilter(value, customInput);
    if (next === value) {
      Toast.warning("请输入新的有效文件扩展名");
      return;
    }
    onChange(next);
    setCustomInput("");
  };

  return (
    <div
      className="file-type-filter"
      id={id}
      aria-label={ariaLabel}
      aria-labelledby={ariaLabelledBy}
    >
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
                  <span className="file-type-count">
                    {selected.length}/{group.patterns.length}
                  </span>
                </button>
              </div>
              {isExpanded && (
                <div
                  className="file-type-patterns"
                  role="group"
                  aria-label={`${group.label}扩展名`}
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
                type="tertiary"
                theme="borderless"
                icon={<IconChevronRight aria-hidden="true" />}
                aria-label={`${otherExpanded ? "收起" : "展开"}其他`}
                aria-expanded={otherExpanded}
                onClick={() => setOtherExpanded((current) => !current)}
              />
              <Checkbox
                aria-label="其他"
                checked={otherFilters.every((item) => item.enabled)}
                indeterminate={
                  otherFilters.some((item) => item.enabled) &&
                  !otherFilters.every((item) => item.enabled)
                }
                onChange={(event) =>
                  onChange(
                    updateExcludePatterns(
                      value,
                      otherFilters.map((item) => item.pattern),
                      Boolean(event.target.checked),
                    ),
                  )
                }
              />
              <span className="file-type-icon other">
                <IconFile aria-hidden="true" />
              </span>
              <button
                className="file-type-group-label"
                type="button"
                onClick={() => setOtherExpanded((current) => !current)}
              >
                <span>其他</span>
                <span className="file-type-count">
                  {otherFilters.filter((item) => item.enabled).length}/
                  {otherFilters.length}
                </span>
              </button>
            </div>
            {otherExpanded && (
              <div className="custom-file-types" aria-label="其他扩展名">
                {otherFilters.map((item) => (
                  <div className="custom-file-type-row" key={item.pattern}>
                    <Checkbox
                      aria-label={item.pattern}
                      checked={item.enabled}
                      onChange={(event) =>
                        onChange(
                          updateExcludePatterns(
                            value,
                            [item.pattern],
                            Boolean(event.target.checked),
                          ),
                        )
                      }
                    >
                      {item.pattern}
                    </Checkbox>
                    <Button
                      type="tertiary"
                      theme="borderless"
                      icon={<IconDeleteStroked aria-hidden="true" />}
                      aria-label={`删除 ${item.pattern}`}
                      onClick={() =>
                        onChange(removeCustomFileTypeFilter(value, item.pattern))
                      }
                    />
                  </div>
                ))}
              </div>
            )}
          </div>
        )}
      </div>
      <div className="file-type-add">
        <Input
          value={customInput}
          aria-label="自定义文件扩展名"
          placeholder="文件扩展名，如 iso、tar.gz"
          onChange={setCustomInput}
          onEnterPress={addCustom}
        />
        <Button
          type="primary"
          theme="solid"
          icon={<IconPlusStroked aria-hidden="true" />}
          aria-label="新增文件类型"
          onClick={addCustom}
          disabled={!customInput.trim()}
        >
          新增
        </Button>
      </div>
    </div>
  );
}
