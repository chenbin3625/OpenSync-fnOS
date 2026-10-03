import { createContext, useContext } from "react";
import {
  IconArrowUp,
  IconGithubLogo,
  IconRefresh,
} from "@douyinfe/semi-icons";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Tooltip from "@douyinfe/semi-ui/lib/es/tooltip";
import type { LatestVersion } from "../api/client";

export const repositoryURL = "https://github.com/chenbin3625/OpenSync-fnOS";
export const latestReleaseURL =
  "https://github.com/chenbin3625/OpenSync-fnOS/releases/latest";

/** Installed versions arrive as "0.0.28" while releases are tagged "v0.0.28". */
export function formatVersion(version: string) {
  return /^\d+\.\d+\.\d+$/.test(version) ? `v${version}` : version;
}

export const VersionContext = createContext<{
  currentVersion: string;
  latest: LatestVersion | null;
  checking: boolean;
  checkUpdate: () => Promise<void>;
}>({
  currentVersion: "dev",
  latest: null,
  checking: false,
  checkUpdate: async () => {},
});

export function VersionLink() {
  const { currentVersion, latest, checking, checkUpdate } =
    useContext(VersionContext);
  const version = formatVersion(currentVersion);
  const hasUpdate = latest?.hasUpdate === true;
  return (
    <div className="version-entry">
      <a
        className="nav-link version-link"
        href={hasUpdate ? latest.releaseURL || latestReleaseURL : repositoryURL}
        target="_blank"
        rel="noopener noreferrer"
        aria-label={`GitHub 当前版本 ${version}${hasUpdate ? `，有新版本 ${latest.latestVersion}` : ""}`}
      >
        <IconGithubLogo aria-hidden="true" />
        <span className="nav-label">GitHub</span>
        <Tag className="version-tag" color="grey" size="small" type="light">
          {version}
        </Tag>
        {hasUpdate && (
          /* 链接的 aria-label 已经包含新版本信息，这里只做视觉提示。 */
          <span
            className="version-update"
            title={`有新版本 ${latest.latestVersion}`}
          >
            <IconArrowUp aria-hidden="true" />
          </span>
        )}
      </a>
      <Tooltip content="检查更新" trigger="hover" disableFocusListener>
        <button
          type="button"
          className="version-check"
          onClick={() => void checkUpdate()}
          disabled={checking}
          aria-busy={checking}
          aria-label="检查更新"
        >
          <IconRefresh spin={checking} aria-hidden="true" />
        </button>
      </Tooltip>
    </div>
  );
}
