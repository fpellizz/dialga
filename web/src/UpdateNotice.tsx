import { invoke, isTauri } from "@tauri-apps/api/core";
import { ArrowUpCircle } from "lucide-react";
import type { MouseEvent, ReactNode } from "react";
import { useI18n } from "./i18n";
import { useUpdates } from "./UpdateProvider";
import type { Release } from "./updates";

/** In the desktop app a link would not reach the browser: the shell opens the release. */
export function openRelease(event: MouseEvent<HTMLAnchorElement>, release: Release) {
  if (isTauri()) {
    event.preventDefault();
    void invoke("open_release", { tag: release.tag });
  }
}

/** Link to the release page; a plain link in the browser. */
export function ReleaseLink({
  release,
  className,
  title,
  children,
}: {
  release: Release;
  className?: string;
  title?: string;
  children?: ReactNode;
}) {
  const { t } = useI18n();
  return (
    <a
      href={release.url}
      target="_blank"
      rel="noreferrer"
      className={className}
      title={title}
      onClick={(e) => openRelease(e, release)}
    >
      {children ?? t("update.view")}
    </a>
  );
}

/**
 * The sidebar notice of a new release, above the engine status. Collapsed it
 * is a dot with an accessible name. The text of the announcement lives in an
 * always-present status region that changes only when a new version shows up,
 * so the periodic checks never repeat it.
 */
export function UpdateNotice({ collapsed }: { collapsed: boolean }) {
  const { t } = useI18n();
  const { notice, dismiss } = useUpdates();
  const text = notice ? t("update.available", { version: notice.version }) : "";
  return (
    <>
      <div role="status" className="sr-only">
        {text}
      </div>
      {notice &&
        (collapsed ? (
          <ReleaseLink release={notice} className="update-dot" title={text}>
            <ArrowUpCircle size={16} aria-hidden="true" />
            <span className="sr-only">{`${text}. ${t("update.view")}`}</span>
          </ReleaseLink>
        ) : (
          <section className="update-notice" aria-label={t("update.region")}>
            <p className="update-text">{text}</p>
            <div className="update-actions">
              <ReleaseLink release={notice} className="btn btn-outline btn-sm" />
              <button type="button" className="btn btn-ghost btn-sm" onClick={dismiss}>
                {t("update.dismiss")}
              </button>
            </div>
          </section>
        ))}
    </>
  );
}
