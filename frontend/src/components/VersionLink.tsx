import { createContext, useContext } from "react";
import { IconArrowUp, IconGithubLogo } from "@douyinfe/semi-icons";
import type { LatestVersion } from "../api/client";

const repositoryURL = "https://github.com/chenbin3625/OpenSync-fnOS";
const latestReleaseURL =
  "https://github.com/chenbin3625/OpenSync-fnOS/releases/latest";

export const VersionContext = createContext<{
  currentVersion: string;
  latest: LatestVersion | null;
}>({ currentVersion: "dev", latest: null });

export function VersionLink() {
  const { currentVersion, latest } = useContext(VersionContext);
  const version = /^\d+\.\d+\.\d+$/.test(currentVersion)
    ? `v${currentVersion}`
    : currentVersion;
  const hasUpdate = latest?.hasUpdate === true;
  return (
    <a
      className="nav-link version-link"
      href={hasUpdate ? latest.releaseURL || latestReleaseURL : repositoryURL}
      target="_blank"
      rel="noopener noreferrer"
      aria-label={`GitHub 当前版本 ${version}${hasUpdate ? `，有新版本 ${latest.latestVersion}` : ""}`}
    >
      <IconGithubLogo aria-hidden="true" />
      <span className="nav-label">GitHub {version}</span>
      {hasUpdate && (
        <span
          className="version-update"
          role="status"
          aria-label={`有新版本 ${latest.latestVersion}`}
          title={`有新版本 ${latest.latestVersion}`}
        >
          <IconArrowUp aria-hidden="true" />
        </span>
      )}
    </a>
  );
}
