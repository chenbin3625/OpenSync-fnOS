import { useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconArchive,
  IconChevronRight,
  IconCodeStroked,
  IconDeleteStroked,
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
  normalizeCustomFileTypePattern,
  parseOtherFileTypeFilters,
  removeCustomFileTypeFilter,
  updateExcludePatterns,
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
  const [otherExpanded, setOtherExpanded] = useState(false);
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
    const pattern = normalizeCustomFileTypePattern(customInput);
    if (!pattern) {
      Toast.warning("请输入有效的文件名或文件后缀");
      return;
    }
    if (isExcludePatternEnabled(value, pattern)) {
      // A valid rule that is already active is not an input error; the old
      // code showed "请输入有效的文件名或文件后缀" because addCustomFileTypeFilter
      // returned the text unchanged for an already-enabled rule.
      Toast.info("该规则已启用");
      setCustomInput("");
      return;
    }
    onChange(addCustomFileTypeFilter(value, pattern));
    setCustomInput("");
  };

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
                type="tertiary"
                theme="borderless"
                icon={<IconChevronRight aria-hidden="true" />}
                aria-label={`${otherExpanded ? "收起" : "展开"}其他`}
                aria-expanded={otherExpanded}
                onClick={() => setOtherExpanded((current) => !current)}
              />
              {/* 自定义规则没有"禁用"状态：取消勾选即删除（后端保存时会丢掉
                  "# " 注释），因此这里不提供"全部取消"的批量复选框，避免误删。 */}
              <span className="file-type-group-spacer" aria-hidden="true" />
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
              <div className="custom-file-types" aria-label="其他文件规则">
                {otherFilters.map((item) => (
                  <div className="custom-file-type-row" key={item.pattern}>
                    <Checkbox
                      aria-label={item.pattern}
                      checked={item.enabled}
                      onChange={(event) =>
                        // No persisted "disabled" state: unchecking removes the
                        // rule instead of parking a "# " comment the backend
                        // would drop on save.
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
          aria-label="自定义文件规则"
          placeholder="文件名或文件后缀，如 ipc-socket、a.bcd、*.doc"
          onChange={setCustomInput}
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
    </div>
  );
}
