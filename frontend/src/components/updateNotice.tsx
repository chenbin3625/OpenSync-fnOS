import Notification from "@douyinfe/semi-ui/lib/es/notification";
import { IconArrowUp } from "@douyinfe/semi-icons";
import type { LatestVersion } from "../api/client";
import { formatVersion, latestReleaseURL } from "./VersionLink";

/**
 * Remembers which release the user has already been told about, so a reload
 * does not pop the same reminder again inside one browser session.
 */
export const updateNoticeKey = "opensync:update-notice";

/**
 * Top-right upgrade reminder. Stays until dismissed (duration 0) because a few
 * seconds is exactly what the sidebar badge already fails to communicate; the
 * whole card opens the release page, where the .fpk assets are.
 */
export function notifyUpdateAvailable(
  currentVersion: string,
  latest: LatestVersion,
) {
  const url = latest.releaseURL || latestReleaseURL;
  Notification.info({
    title: `发现新版本 ${latest.latestVersion}`,
    content: (
      <span className="update-notice-content">
        当前版本 {formatVersion(currentVersion)}，
        <a
          className="update-notice-link"
          href={url}
          target="_blank"
          rel="noopener noreferrer"
        >
          前往 GitHub 下载升级
        </a>
      </span>
    ),
    position: "topRight",
    duration: 0,
    showClose: true,
    className: "update-notice",
    icon: (
      <span className="update-notice-icon" aria-hidden="true">
        <IconArrowUp />
      </span>
    ),
    // 卡片整体可点击；点在链接上时让浏览器自己处理，避免开两个标签页。
    onClick: (event: MouseEvent) => {
      if ((event?.target as HTMLElement | null)?.closest("a")) return;
      window.open(url, "_blank", "noopener,noreferrer");
    },
  });
}
