import type { Dispatch, SetStateAction } from "react";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Switch from "@douyinfe/semi-ui/lib/es/switch";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import { IconChevronRight } from "@douyinfe/semi-icons";
import { BwLimitEditor } from "./BwLimitEditor";
import { Field, SettingRow } from "./common";
import {
  applyTaskScenarioPreset,
  taskScenarioPresets,
  type CompareModeSource,
  type JobForm,
} from "../lib/taskForm";
import type { JobItem } from "../types";

export function countActiveExpertOptions(form: JobForm): number {
  return (
    (form.bwLimit.trim() ? 1 : 0) +
    (Number(form.tpsLimit || 0) > 0 ? 1 : 0) +
    (Number(form.tpsLimitBurst || 0) > 0 ? 1 : 0) +
    (form.modifyWindow.trim() ? 1 : 0) +
    (form.fastList ? 1 : 0) +
    (Number(form.multiThreadStreams || 0) > 0 ? 1 : 0) +
    (form.createEmptySrcDirs ? 1 : 0) +
    (form.followSymlinks ? 1 : 0) +
    (form.extraFlags?.noUpdateModtime ? 1 : 0) +
    (form.extraFlags?.inplace ? 1 : 0) +
    (form.extraFlags?.checkFirst ? 1 : 0) +
    (form.extraFlags?.s3NoHead ? 1 : 0) +
    (form.extraFlags?.ignoreSize ? 1 : 0) +
    (form.extraFlags?.trackRenames ? 1 : 0) +
    (form.extraFlags?.useMmap ? 1 : 0) +
    (form.extraFlags?.maxTransfer?.trim() ? 1 : 0) +
    (form.extraFlags?.cutoffMode?.trim() ? 1 : 0) +
    (form.extraFlags?.maxDuration?.trim() ? 1 : 0) +
    (form.extraFlags?.orderBy?.trim() ? 1 : 0) +
    (Number(form.extraFlags?.maxBacklog || 0) > 0 ? 1 : 0) +
    (form.extraFlags?.bufferSize?.trim() ? 1 : 0) +
    (form.extraFlags?.multiThreadCutoff?.trim() ? 1 : 0) +
    (form.extraFlags?.multiThreadChunkSize?.trim() ? 1 : 0) +
    (form.extraFlags?.userAgent?.trim() ? 1 : 0) +
    (form.extraFlags?.ioTimeout?.trim() ? 1 : 0) +
    (form.extraFlags?.conTimeout?.trim() ? 1 : 0) +
    (Number(form.extraFlags?.lowLevelRetries || 0) > 0 ? 1 : 0) +
    (form.extraFlags?.retriesSleep?.trim() ? 1 : 0) +
    (form.extraFlags?.resumeEnable ? 1 : 0) +
    (Number(form.extraFlags?.autoRetryMax || 0) > 0 ? 1 : 0) +
    (Number(form.extraFlags?.postSyncJobId || 0) > 0 ? 1 : 0) +
    (form.extraFlags?.checkSchedule?.trim() ? 1 : 0) +
    (form.extraFlags?.directStream ? 1 : 0) +
    (Number(form.extraFlags?.bisyncMaxDelete || 0) > 0 ? 1 : 0) +
    (form.extraFlags?.bisyncForce ? 1 : 0) +
    (Number(form.copyConcurrency || 0) > 0 ? 1 : 0) +
    (Number(form.scanConcurrency || 0) > 0 ? 1 : 0) +
    (Number(form.maxRetries) >= 0 ? 1 : 0)
  );
}

export function ExpertTuningDrawer({
  form,
  open,
  onToggleOpen,
  change,
  setForm,
  setDirty,
  setCompareModeSource,
  bwLimitError,
  setBwLimitError,
  bwLimitFieldError,
  systemDefaultBwLimit,
  existingJobs,
  showPresetsOutside = false,
}: {
  form: JobForm;
  open: boolean;
  onToggleOpen: () => void;
  change: <K extends keyof JobForm>(key: K, value: JobForm[K]) => void;
  setForm: Dispatch<SetStateAction<JobForm>>;
  setDirty: (dirty: boolean) => void;
  setCompareModeSource: (src: CompareModeSource) => void;
  bwLimitError: string | null;
  setBwLimitError: (err: string | null) => void;
  bwLimitFieldError?: string;
  systemDefaultBwLimit: string;
  existingJobs?: readonly JobItem[];
  showPresetsOutside?: boolean;
}) {
  const activeCount = countActiveExpertOptions(form);

  const presetStrip = (
    <Field
      label="场景一键预设"
      hint="点击自动填入推荐的断点续传、并发与重试参数，应用后仍可在下方专家选项微调"
    >
      <div className="schedule-chips" role="group" aria-label="场景一键预设">
        {taskScenarioPresets.map((preset) => (
          <button
            key={preset.key}
            type="button"
            className="schedule-chip"
            title={preset.desc}
            onClick={() => {
              if (preset.key === "cold_archive") {
                setCompareModeSource("manual");
              }
              setForm((prev) => applyTaskScenarioPreset(prev, preset.key));
              setDirty(true);
              Toast.success(`已应用「${preset.label}」推荐配置`);
            }}
          >
            {preset.label}
          </button>
        ))}
      </div>
    </Field>
  );

  return (
    <div className="expert-tuning-section">
      {showPresetsOutside && presetStrip}
      <div className="advanced-options">
        <button
          type="button"
          className={`advanced-options-toggle${open ? " is-open" : ""}`}
          aria-expanded={open}
          onClick={onToggleOpen}
        >
          <IconChevronRight
            className="advanced-options-chevron"
            aria-hidden="true"
          />
          <span>高级引擎调优（rclone 专家选项）</span>
          {activeCount > 0 && (
            <Tag size="small" color="blue" type="light">
              已配置 {activeCount} 项
            </Tag>
          )}
        </button>
        {open && (
          <div className="advanced-options-body">
            <div className="advanced-option-group">
              <div className="advanced-option-group-title">
                场景预设与传输增强
              </div>
              {!showPresetsOutside && presetStrip}
              <SettingRow
                label="开启大文件断点续传"
                variant="bordered"
                tip="支持远端到本地下载（分片续传）与本地到 S3 分片上传续传；中断后再次运行或重试将从已传字节/分片继续"
              >
                <Switch
                  aria-label="开启大文件断点续传"
                  checked={Boolean(form.extraFlags?.resumeEnable)}
                  onChange={(v) =>
                    change("extraFlags", {
                      ...form.extraFlags,
                      resumeEnable: v,
                    })
                  }
                />
              </SettingRow>
              {form.extraFlags?.resumeEnable && (
                <Field
                  label="断点续传最小文件阈值（MB）"
                  hint="0 表示默认 50 MB；仅超过该大小的文件走断点续传通道，小文件继续高速并发传输"
                >
                  <Input
                    inputMode="numeric"
                    value={String(form.extraFlags?.resumeMinSizeMB || 0)}
                    onChange={(v) =>
                      change("extraFlags", {
                        ...form.extraFlags,
                        resumeMinSizeMB: Math.max(
                          0,
                          Math.min(10240, Math.trunc(Number(v) || 0)),
                        ),
                      })
                    }
                  />
                </Field>
              )}
            </div>

            <div className="advanced-option-group">
              <div className="advanced-option-group-title">
                容错重试与联动策略
              </div>
              <div className="form-grid form-grid--three">
                <Field
                  label="弱网底层重试（--low-level-retries）"
                  hint="0 默认 10 次（0–50）"
                >
                  <Input
                    inputMode="numeric"
                    value={String(form.extraFlags?.lowLevelRetries || 0)}
                    onChange={(v) =>
                      change("extraFlags", {
                        ...form.extraFlags,
                        lowLevelRetries: Math.max(
                          0,
                          Math.min(50, Math.trunc(Number(v) || 0)),
                        ),
                      })
                    }
                  />
                </Field>
                <Field
                  label="重试间隔（--retries-sleep）"
                  hint="如 5s / 30s / 1m（留空默认）"
                >
                  <Input
                    value={form.extraFlags?.retriesSleep || ""}
                    placeholder="如 10s / 30s"
                    showClear
                    onChange={(v) =>
                      change("extraFlags", {
                        ...form.extraFlags,
                        retriesSleep: v,
                      })
                    }
                  />
                </Field>
                <Field
                  label="定时一致性校验"
                  hint="周期后台校验并在发现差异时告警"
                >
                  <Select
                    value={form.extraFlags?.checkSchedule || ""}
                    optionList={[
                      { label: "关闭", value: "" },
                      { label: "每日自动校验", value: "daily" },
                      { label: "每周自动校验", value: "weekly" },
                      { label: "每月自动校验", value: "monthly" },
                    ]}
                    onChange={(v) =>
                      change("extraFlags", {
                        ...form.extraFlags,
                        checkSchedule: String(v || ""),
                      })
                    }
                  />
                </Field>
              </div>
              <div className="form-grid form-grid--three">
                <Field
                  label="失败自动重试次数"
                  hint="0 关闭；1–10 次，仅重跑失败项"
                >
                  <Input
                    inputMode="numeric"
                    value={String(form.extraFlags?.autoRetryMax || 0)}
                    onChange={(v) =>
                      change("extraFlags", {
                        ...form.extraFlags,
                        autoRetryMax: Math.max(
                          0,
                          Math.min(10, Math.trunc(Number(v) || 0)),
                        ),
                      })
                    }
                  />
                </Field>
                <Field
                  label="自动重试延迟（分钟）"
                  hint="默认 5 分钟（1–1440）"
                >
                  <Input
                    inputMode="numeric"
                    value={String(form.extraFlags?.autoRetryDelayMin || 0)}
                    onChange={(v) =>
                      change("extraFlags", {
                        ...form.extraFlags,
                        autoRetryDelayMin: Math.max(
                          0,
                          Math.min(1440, Math.trunc(Number(v) || 0)),
                        ),
                      })
                    }
                  />
                </Field>
                <Field
                  label="成功后串联触发任务"
                  hint="同步批次完成后自动触发另一个任务"
                >
                  <Select
                    value={Number(form.extraFlags?.postSyncJobId || 0)}
                    optionList={[
                      { label: "不串联", value: 0 },
                      ...(existingJobs || [])
                        .filter((j) => j.id !== form.id)
                        .map((j) => ({
                          label: j.remark?.trim() || `任务 #${j.id}`,
                          value: j.id,
                        })),
                    ]}
                    onChange={(v) =>
                      change("extraFlags", {
                        ...form.extraFlags,
                        postSyncJobId: Math.max(
                          0,
                          Math.trunc(Number(v) || 0),
                        ),
                      })
                    }
                  />
                </Field>
              </div>
              <div className="native-checkbox-group">
                <Checkbox
                  checked={Boolean(form.extraFlags?.directStream)}
                  onChange={(e) =>
                    change("extraFlags", {
                      ...form.extraFlags,
                      directStream: Boolean(e.target.checked),
                    })
                  }
                >
                  海量文件直接流式同步（跳过前端预扫描与待传清单，大幅降低内存与扫描耗时）
                </Checkbox>
              </div>
            </div>

            <div className="advanced-option-group">
              <div className="advanced-option-group-title">并发与带宽流控</div>
              <div className="form-grid form-grid--three">
                <Field
                  label="操作并发覆盖"
                  hint="0 = 跟随全局设置 (1–100)"
                >
                  <Input
                    inputMode="numeric"
                    value={String(form.copyConcurrency)}
                    onChange={(v) =>
                      change(
                        "copyConcurrency",
                        Math.max(0, Math.min(100, Math.trunc(Number(v) || 0))),
                      )
                    }
                  />
                </Field>
                <Field
                  label="扫描并发覆盖"
                  hint="0 = 跟随全局设置 (1–50)"
                >
                  <Input
                    inputMode="numeric"
                    value={String(form.scanConcurrency)}
                    onChange={(v) =>
                      change(
                        "scanConcurrency",
                        Math.max(0, Math.min(50, Math.trunc(Number(v) || 0))),
                      )
                    }
                  />
                </Field>
                <Field label="失败重试覆盖" hint="-1 = 跟随全局 (0–10)">
                  <Input
                    inputMode="numeric"
                    value={String(form.maxRetries)}
                    onChange={(v) => {
                      const trimmed = v.trim();
                      if (trimmed === "-" || trimmed === "") {
                        change("maxRetries", -1);
                        return;
                      }
                      const n = Math.trunc(Number(trimmed));
                      if (Number.isFinite(n)) {
                        change(
                          "maxRetries",
                          Math.max(-1, Math.min(10, n)),
                        );
                      }
                    }}
                  />
                </Field>
              </div>
              <Field
                label="带宽限制"
                fieldId="bwLimit"
                error={bwLimitError || bwLimitFieldError}
                hint="留空跟随系统全局默认；支持单任务固定限速、定时分时段限速或强制不限速"
              >
                <BwLimitEditor
                  allowInherit
                  compact
                  globalDefault={systemDefaultBwLimit}
                  value={form.bwLimit}
                  onChange={(v) => change("bwLimit", v)}
                  onValidityChange={setBwLimitError}
                />
              </Field>
              <div className="form-grid form-grid--two">
                <Field
                  label="API 限流（--tpslimit）"
                  hint="每秒请求上限（0–1000），0 表示不限（防云盘 429）"
                >
                  <Input
                    inputMode="decimal"
                    value={String(form.tpsLimit)}
                    onChange={(v) => {
                      const n = Number(v);
                      change(
                        "tpsLimit",
                        Number.isFinite(n) && n >= 0 ? Math.min(1000, n) : 0,
                      );
                    }}
                  />
                </Field>
                <Field
                  label="API 突发上限（--tpslimit-burst）"
                  hint="0–1000，0 表示使用 rclone 默认值"
                >
                  <Input
                    inputMode="numeric"
                    value={String(form.tpsLimitBurst)}
                    onChange={(v) =>
                      change(
                        "tpsLimitBurst",
                        Math.max(0, Math.min(1000, Math.trunc(Number(v) || 0))),
                      )
                    }
                  />
                </Field>
              </div>
              <div className="form-grid form-grid--two">
                <Field
                  label="时间精度容差（--modify-window）"
                  hint="留空默认；FAT32/SMB/部分网盘可填 1s 或 2s"
                >
                  <Input
                    value={form.modifyWindow}
                    placeholder="如 1s / 2s / 1m"
                    showClear
                    onChange={(v) => change("modifyWindow", v)}
                  />
                </Field>
                <Field
                  label="单文件多线程流数（--multi-thread-streams）"
                  hint="0 使用默认（4）；1–64，加速单一大文件传输"
                >
                  <Input
                    inputMode="numeric"
                    value={String(form.multiThreadStreams)}
                    onChange={(v) =>
                      change(
                        "multiThreadStreams",
                        Math.max(0, Math.min(64, Math.trunc(Number(v) || 0))),
                      )
                    }
                  />
                </Field>
              </div>
            </div>

            <div className="advanced-option-group">
              <div className="advanced-option-group-title">
                rclone 底层行为与性能调优
              </div>
              <SettingRow
                label="快速列表（--fast-list）"
                variant="bordered"
                tip="在支持的远端（S3 / B2 / GCS / Azure 等）批量递归列目录，大幅减少 API 事务次数，略微增加内存占用"
              >
                <Switch
                  aria-label="快速列表"
                  checked={form.fastList}
                  onChange={(v) => change("fastList", v)}
                />
              </SettingRow>
              <SettingRow
                label="同步空子目录（--create-empty-src-dirs）"
                variant="bordered"
                tip="在目标端保留源目录中不含任何文件的空目录结构"
              >
                <Switch
                  aria-label="同步空子目录"
                  checked={form.createEmptySrcDirs}
                  onChange={(v) => change("createEmptySrcDirs", v)}
                />
              </SettingRow>
              <SettingRow
                label="跟随本地符号链接"
                variant="bordered"
                tip="开启后跟随源端本地授权目录内指向同授权目录内部的符号链接（跨出授权目录的链接仍会被安全忽略）"
              >
                <Switch
                  aria-label="跟随本地符号链接"
                  checked={form.followSymlinks}
                  onChange={(v) => change("followSymlinks", v)}
                />
              </SettingRow>
              <div className="native-checkbox-group">
                <Checkbox
                  checked={Boolean(form.extraFlags?.noUpdateModtime)}
                  onChange={(e) =>
                    change("extraFlags", {
                      ...form.extraFlags,
                      noUpdateModtime: Boolean(e.target.checked),
                    })
                  }
                >
                  跳过仅修改时间变更的元数据更新（--no-update-modtime）
                </Checkbox>
                <Checkbox
                  checked={Boolean(form.extraFlags?.inplace)}
                  onChange={(e) =>
                    change("extraFlags", {
                      ...form.extraFlags,
                      inplace: Boolean(e.target.checked),
                    })
                  }
                >
                  原地覆盖目标文件，不创建临时文件（--inplace）
                </Checkbox>
                <Checkbox
                  checked={Boolean(form.extraFlags?.checkFirst)}
                  onChange={(e) =>
                    change("extraFlags", {
                      ...form.extraFlags,
                      checkFirst: Boolean(e.target.checked),
                    })
                  }
                >
                  先完成全部差异比对再传输（--check-first）
                </Checkbox>
                <Checkbox
                  checked={Boolean(form.extraFlags?.s3NoHead)}
                  onChange={(e) =>
                    change("extraFlags", {
                      ...form.extraFlags,
                      s3NoHead: Boolean(e.target.checked),
                    })
                  }
                >
                  跳过 S3 上传后 HEAD 校验以减少 API 调用（--s3-no-head）
                </Checkbox>
                <Checkbox
                  checked={Boolean(form.extraFlags?.ignoreSize)}
                  onChange={(e) =>
                    change("extraFlags", {
                      ...form.extraFlags,
                      ignoreSize: Boolean(e.target.checked),
                    })
                  }
                >
                  仅按修改时间比对，忽略文件大小变化（--ignore-size）
                </Checkbox>
                <Checkbox
                  checked={Boolean(form.extraFlags?.trackRenames)}
                  disabled={form.method !== 1 && form.method !== 3}
                  onChange={(e) =>
                    change("extraFlags", {
                      ...form.extraFlags,
                      trackRenames: Boolean(e.target.checked),
                    })
                  }
                >
                  启用重命名追踪，避免重命名后重传（--track-renames，开启同步删除或双向同步时生效）
                </Checkbox>
                <Checkbox
                  checked={Boolean(form.extraFlags?.useMmap)}
                  onChange={(e) =>
                    change("extraFlags", {
                      ...form.extraFlags,
                      useMmap: Boolean(e.target.checked),
                    })
                  }
                >
                  使用 mmap 零拷贝传输缓冲（--use-mmap）
                </Checkbox>
              </div>
              <div className="form-grid form-grid--three">
                <Field
                  label="单次传输总量上限（--max-transfer）"
                  hint="留空不限，支持如 50G / 500M"
                >
                  <Input
                    value={form.extraFlags?.maxTransfer || ""}
                    placeholder="如 100G / 500M"
                    showClear
                    onChange={(v) =>
                      change("extraFlags", {
                        ...form.extraFlags,
                        maxTransfer: v,
                      })
                    }
                  />
                </Field>
                <Field label="达限截断模式（--cutoff-mode）">
                  <Select
                    value={form.extraFlags?.cutoffMode || ""}
                    optionList={[
                      { label: "默认 (hard)", value: "" },
                      { label: "hard（立即停止）", value: "hard" },
                      { label: "soft（传完当前队列）", value: "soft" },
                      { label: "cautious（不再发起新传输）", value: "cautious" },
                    ]}
                    onChange={(v) =>
                      change("extraFlags", {
                        ...form.extraFlags,
                        cutoffMode: String(v || ""),
                      })
                    }
                  />
                </Field>
                <Field
                  label="最大运行时长（--max-duration）"
                  hint="超时后平滑收尾退出，如 2h"
                >
                  <Input
                    value={form.extraFlags?.maxDuration || ""}
                    placeholder="如 2h / 30m"
                    showClear
                    onChange={(v) =>
                      change("extraFlags", {
                        ...form.extraFlags,
                        maxDuration: v,
                      })
                    }
                  />
                </Field>
              </div>
              <div className="form-grid form-grid--three">
                <Field label="传输优先级排序（--order-by）">
                  <Select
                    value={form.extraFlags?.orderBy || ""}
                    optionList={[
                      { label: "默认顺序", value: "" },
                      { label: "小文件优先 (size,ascending)", value: "size,ascending" },
                      { label: "大文件优先 (size,descending)", value: "size,descending" },
                      { label: "最旧修改优先 (modtime,ascending)", value: "modtime,ascending" },
                      { label: "最新修改优先 (modtime,descending)", value: "modtime,descending" },
                      { label: "文件名升序 (name,ascending)", value: "name,ascending" },
                      { label: "文件名降序 (name,descending)", value: "name,descending" },
                    ]}
                    onChange={(v) =>
                      change("extraFlags", {
                        ...form.extraFlags,
                        orderBy: String(v || ""),
                      })
                    }
                  />
                </Field>
                <Field
                  label="预扫描队列上限（--max-backlog）"
                  hint="0 使用默认；控制内存排队数"
                >
                  <Input
                    inputMode="numeric"
                    value={String(form.extraFlags?.maxBacklog || 0)}
                    onChange={(v) =>
                      change("extraFlags", {
                        ...form.extraFlags,
                        maxBacklog: Math.max(
                          0,
                          Math.trunc(Number(v) || 0),
                        ),
                      })
                    }
                  />
                </Field>
                <Field
                  label="读写缓冲区（--buffer-size）"
                  hint="留空默认 16M，设为 0 禁用"
                >
                  <Input
                    value={form.extraFlags?.bufferSize || ""}
                    placeholder="如 16M / 32M / 0"
                    showClear
                    onChange={(v) =>
                      change("extraFlags", {
                        ...form.extraFlags,
                        bufferSize: v,
                      })
                    }
                  />
                </Field>
              </div>
              <div className="form-grid form-grid--three">
                <Field
                  label="多线程分流阈值（--multi-thread-cutoff）"
                  hint="默认 250M"
                >
                  <Input
                    value={form.extraFlags?.multiThreadCutoff || ""}
                    placeholder="如 250M / 100M"
                    showClear
                    onChange={(v) =>
                      change("extraFlags", {
                        ...form.extraFlags,
                        multiThreadCutoff: v,
                      })
                    }
                  />
                </Field>
                <Field
                  label="多线程分块（--multi-thread-chunk-size）"
                  hint="默认 64M"
                >
                  <Input
                    value={form.extraFlags?.multiThreadChunkSize || ""}
                    placeholder="如 64M / 32M"
                    showClear
                    onChange={(v) =>
                      change("extraFlags", {
                        ...form.extraFlags,
                        multiThreadChunkSize: v,
                      })
                    }
                  />
                </Field>
                <Field label="User-Agent（--user-agent）">
                  <Input
                    value={form.extraFlags?.userAgent || ""}
                    placeholder="可选 HTTP User-Agent"
                    showClear
                    onChange={(v) =>
                      change("extraFlags", {
                        ...form.extraFlags,
                        userAgent: v,
                      })
                    }
                  />
                </Field>
              </div>
              <div className="form-grid form-grid--two">
                <Field label="I/O 空闲超时（--timeout）" hint="默认 5m">
                  <Input
                    value={form.extraFlags?.ioTimeout || ""}
                    placeholder="如 5m / 30s"
                    showClear
                    onChange={(v) =>
                      change("extraFlags", {
                        ...form.extraFlags,
                        ioTimeout: v,
                      })
                    }
                  />
                </Field>
                <Field label="连接超时（--contimeout）" hint="默认 1m">
                  <Input
                    value={form.extraFlags?.conTimeout || ""}
                    placeholder="如 1m / 15s"
                    showClear
                    onChange={(v) =>
                      change("extraFlags", {
                        ...form.extraFlags,
                        conTimeout: v,
                      })
                    }
                  />
                </Field>
              </div>
            </div>

            {form.method === 3 && (
              <div className="advanced-option-group">
                <div className="advanced-option-group-title">
                  双向同步保护策略
                </div>
                <div className="form-grid form-grid--two">
                  <Field
                    label="单次删除比例上限（%）"
                    hint="0 表示使用默认值（50%），1–100 按单端删除占比触发安全保护，防止误删全盘"
                  >
                    <Input
                      inputMode="numeric"
                      value={String(form.extraFlags?.bisyncMaxDelete || 0)}
                      onChange={(v) =>
                        change("extraFlags", {
                          ...form.extraFlags,
                          bisyncMaxDelete: Math.max(
                            0,
                            Math.min(100, Math.trunc(Number(v) || 0)),
                          ),
                        })
                      }
                    />
                  </Field>
                </div>
                <div className="native-checkbox-group">
                  <Checkbox
                    checked={Boolean(form.extraFlags?.bisyncForce)}
                    onChange={(e) =>
                      change("extraFlags", {
                        ...form.extraFlags,
                        bisyncForce: Boolean(e.target.checked),
                      })
                    }
                  >
                    超限删除仍强制执行（谨慎启用）
                  </Checkbox>
                </div>
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
