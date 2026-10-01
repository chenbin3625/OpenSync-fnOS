import { createContext, useContext } from "react";
import { IconAlertCircle, IconInfoCircle } from "@douyinfe/semi-icons";
import type { LatestVersion } from "../api/client";

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
  return (
    <a
      className="nav-link version-link"
      href={latest?.releaseURL || latestReleaseURL}
      target="_blank"
      rel="noopener noreferrer"
      aria-label={`当前版本 ${version}`}
    >
      <IconInfoCircle aria-hidden="true" />
      <span className="nav-label">{version}</span>
      {latest?.hasUpdate && (
        <span
          className="version-update"
          role="status"
          aria-label={`有新版本 ${latest.latestVersion}`}
          title={`有新版本 ${latest.latestVersion}`}
        >
          <IconAlertCircle aria-hidden="true" />
        </span>
      )}
    </a>
  );
}
