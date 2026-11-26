import { lazy, Suspense, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Select from "@douyinfe/semi-ui/lib/es/select";
import { api } from "../api/client";
import { Editor, Field, InlineNotice, LoadingPanel, errorToast } from "../components/common";
import { RemotePaths } from "../components/RemotePaths";
import { createRemoteDirCache, RemoteDirCacheProvider } from "../lib/remoteDirCache";
import { useAction, useResource } from "../lib/hooks";
import { formatDisplayPath } from "../lib/syncPathPreview";
import {
  buildJobPayload,
  defaultJobForm,
  resolveAutoCompareModeForDst,
  validateJobFormFields,
  type JobForm,
} from "../lib/taskForm";
import type { JobItem } from "../types";
import type { TaskEditorSavedResult } from "./TaskEditor";

const StorageEditor = lazy(() =>
  import("../components/NativeStorageEditor").then(({ NativeStorageEditor }) => ({
    default: NativeStorageEditor,
  })),
);

const directions = [
  { value: "upload", label: "仅上传 · NAS → 网盘" },
  { value: "download", label: "仅下载 · NAS ← 网盘" },
  { value: "bidirectional", label: "双向同步 · NAS ⇄ 网盘" },
] as const;
type Direction = (typeof directions)[number]["value"];

export function QuickSyncEditor({
  initialRemote,
  existingJobs,
  onAdvanced,
  onClose,
  onSaved,
}: {
  initialRemote?: string;
  existingJobs?: JobItem[];
  onAdvanced: (draft: JobForm) => void;
  onClose: () => void;
  onSaved: (result: TaskEditorSavedResult) => void;
}) {
  const storage = useResource(api.nativeStorageState);
  const action = useAction();
  const [remoteName, setRemoteName] = useState(initialRemote || "");
  const [configuring, setConfiguring] = useState(Boolean(initialRemote));
  const [adding, setAdding] = useState(false);
  const [localPaths, setLocalPaths] = useState<string[]>([]);
  const [remotePaths, setRemotePaths] = useState<string[]>([]);
  const [direction, setDirection] = useState<Direction>("upload");
  const [name, setName] = useState<string | null>(null);
  const [validated, setValidated] = useState(false);
  const [treeVersion, setTreeVersion] = useState(0);
  const [cache] = useState(createRemoteDirCache);
  const remotes = storage.data?.remotes;
  const remote = remotes?.find((item) => item.name === remoteName);
  const selectedRemote = remoteName || (remotes?.length === 1 ? remotes[0].name : "");
  const engineId = storage.data?.engineId || undefined;
  const download = direction === "download";
  const bidirectional = direction === "bidirectional";
  const folder = formatDisplayPath(localPaths[0] || remotePaths[0] || "");
  const generatedName = remoteName && folder ? `${folder.split("/").filter(Boolean).at(-1)} · ${remoteName}` : "";
  const draft = defaultJobForm(engineId);
  draft.srcPath = download ? remotePaths : localPaths;
  draft.dstPath = download ? localPaths : remotePaths;
  draft.remark = name?.trim() || generatedName;
  draft.method = bidirectional ? 3 : 0;
  draft.conflictPolicy = bidirectional ? 2 : 0;
  draft.extraFlags.syncDirection = bidirectional ? "" : direction;
  draft.compareMode = resolveAutoCompareModeForDst({
    dstPaths: draft.dstPath,
    remotes,
    currentCompareMode: 0,
    source: "default",
  }).compareMode;

  const chooseRemote = (next: string) => {
    if (remoteName !== next) setRemotePaths([]);
    setRemoteName(next);
    setValidated(false);
    setConfiguring(true);
  };
  const refreshFolders = async () => {
    cache.invalidateEngine(engineId);
    setTreeVersion((version) => version + 1);
    await storage.refresh();
  };
  const validation = validateJobFormFields(draft, { remotes, existingJobs });
  const save = () => {
    if (!remote || !storage.data?.localRoots.length) return;
    setValidated(true);
    if (validation.stepErrors.some(Boolean)) return;
    void action.run(async () => {
      try {
        const result = await api.saveJob(buildJobPayload(draft));
        onSaved({ createdId: result?.id, method: draft.method });
      } catch (err) {
        errorToast(err);
      }
    });
  };
  const showFolders = configuring && Boolean(remote);
  const localError = validated ? validation.fields[download ? "dstPath" : "srcPath"] : "";
  const remoteError = validated ? validation.fields[download ? "srcPath" : "dstPath"] : "";
  const settingsError = validated
    ? validation.fields.alistId || validation.stepErrors[1] || validation.stepErrors[2]
    : "";

  return (
    <RemoteDirCacheProvider cache={cache}>
      <Editor
        title={showFolders ? "设置同步" : "选择网盘"}
        visible={!adding}
        busy={action.busy}
        onClose={onClose}
        onSave={save}
        dirty={Boolean(localPaths.length || remotePaths.length || name)}
        width={560}
        footer={({ close }) => (
          <div className="editor-actions">
            {showFolders && (
              <Button
                className="editor-actions-start"
                onClick={() => setConfiguring(false)}
                disabled={action.busy}
              >
                更换网盘
              </Button>
            )}
            <Button
              type="tertiary"
              disabled={action.busy}
              onClick={() => onAdvanced(localPaths.length || remotePaths.length ? draft : {
                ...draft,
                extraFlags: { ...draft.extraFlags, syncDirection: "" },
              })}
            >
              高级设置
            </Button>
            <Button type="tertiary" onClick={close} disabled={action.busy}>取消</Button>
            {showFolders ? (
              <Button
                type="primary"
                theme="solid"
                disabled={!storage.data?.localRoots.length}
                loading={action.busy}
                onClick={save}
              >
                创建并开始同步
              </Button>
            ) : Boolean(remotes?.length) && (
              <Button
                type="primary"
                theme="solid"
                disabled={!remotes?.some((item) => item.name === selectedRemote)}
                onClick={() => chooseRemote(selectedRemote)}
              >
                下一步
              </Button>
            )}
          </div>
        )}
      >
        <div className="editor-form quick-sync-form">
          {storage.error && (
            <InlineNotice role="alert" message={storage.error} onRetry={storage.refresh} />
          )}
          {storage.loading && !storage.data ? <LoadingPanel /> : showFolders ? (
            <>
              {!storage.data?.localRoots.length && (
                <InlineNotice
                  role="status"
                  message="在飞牛「应用设置 → 文件夹访问权限」中为 OpenSync 授权文件夹，然后刷新目录。"
                  action={<Button onClick={() => void refreshFolders()} loading={storage.loading}>刷新目录</Button>}
                />
              )}
              <Field label="NAS 文件夹" error={localError}>
                <RemotePaths
                  key={`local-${treeVersion}`}
                  engineId={storage.data?.localRoots.length ? engineId : undefined}
                  storageRoot="/本地"
                  remotes={remotes}
                  value={localPaths}
                  onChange={setLocalPaths}
                  ariaLabel="NAS 文件夹"
                />
              </Field>
              <Field label={`网盘文件夹 · ${remoteName}`} error={remoteError}>
                <RemotePaths
                  key={`remote-${remoteName}-${treeVersion}`}
                  engineId={engineId}
                  storageRoot={`/${remoteName}`}
                  remotes={remotes}
                  value={remotePaths}
                  onChange={setRemotePaths}
                  ariaLabel="网盘文件夹"
                />
              </Field>
              <Field label="同步方向">
                <Select
                  aria-label="同步方向"
                  value={direction}
                  optionList={[...directions]}
                  onChange={(value) => setDirection(value as Direction)}
                />
              </Field>
              <Field label="任务名称" error={validated ? validation.fields.remark : ""}>
                <Input
                  aria-label="任务名称"
                  placeholder="根据文件夹和网盘自动生成"
                  value={name ?? generatedName}
                  onChange={setName}
                />
              </Field>
              {draft.srcPath[0] && draft.dstPath[0] && (
                <p className="quick-sync-path">
                  {formatDisplayPath(localPaths[0])} {bidirectional ? "⇄" : download ? "←" : "→"} {formatDisplayPath(remotePaths[0])}
                </p>
              )}
              <InlineNotice
                role="status"
                message={bidirectional
                  ? "两端的新增、修改和删除会互相同步。同名文件发生冲突时保留双方副本。"
                  : `保留${download ? "NAS" : "网盘"}独有的文件。同名文件可能更新。`}
              />
              {settingsError && <InlineNotice role="alert" message={settingsError} />}
            </>
          ) : (
            <>
              {Boolean(remotes?.length) && (
                <Field label="已连接的网盘">
                  <Select
                    aria-label="已连接的网盘"
                    value={selectedRemote || undefined}
                    placeholder="选择一个连接"
                    optionList={remotes?.map((item) => ({ value: item.name, label: item.name }))}
                    onChange={(value) => {
                      if (remoteName !== value) setRemotePaths([]);
                      setRemoteName(String(value));
                    }}
                  />
                </Field>
              )}
              <Button
                theme={remotes?.length ? "light" : "solid"}
                onClick={() => setAdding(true)}
                disabled={!storage.data}
              >
                添加网盘连接
              </Button>
            </>
          )}
        </div>
      </Editor>
      {adding && (
        <Suspense fallback={<LoadingPanel />}>
          <StorageEditor
            mode="form"
            onClose={() => setAdding(false)}
            onSaved={(savedName) => {
              setAdding(false);
              if (savedName) chooseRemote(savedName);
              void refreshFolders();
            }}
          />
        </Suspense>
      )}
    </RemoteDirCacheProvider>
  );
}
