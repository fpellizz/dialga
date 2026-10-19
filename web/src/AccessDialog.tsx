import { Check, LoaderCircle, Lock, RefreshCw, TriangleAlert, UserPlus } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import { type DatabaseAccess, type DatabaseUser, getDatabaseAccess, LEVEL_KEY } from "./access";
import type { EngineInfo } from "./engine";
import { ErrorBanner } from "./Feedback";
import { useI18n } from "./i18n";
import { Modal } from "./Modal";
import { usePassword } from "./PasswordProvider";
import { PlanDialog, type PlanResult } from "./PlanDialog";
import { planHint, type RemoveWhat } from "./plans";

/** The operation dialog open on top of the panel. */
type Operation =
  | { kind: "grant" }
  | { kind: "revoke"; user: string; login: string; remove: RemoveWhat }
  | { kind: "fix-orphan"; user: string };

/**
 * Who can use a database: its SQL users with login, level, backup and roles.
 * Orphans (a user whose login is gone) offer "Fix orphan"; the users Dialga
 * never changes (dbo, the master user...) are read-only. Every change is a
 * plan (PlanDialog: preview, confirm, execute); the panel reads again when
 * one closes after running.
 */
export function AccessDialog({
  engine,
  target,
  database,
  onClose,
}: {
  engine: EngineInfo;
  target: string;
  database: string;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const { epoch } = usePassword();
  const ids = useId();
  const [access, setAccess] = useState<DatabaseAccess | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(true);
  const [reload, setReload] = useState(0);
  const [operation, setOperation] = useState<Operation | null>(null);
  // The user whose revoke is being chosen (inline, before the preview).
  const [revoking, setRevoking] = useState<DatabaseUser | null>(null);

  // biome-ignore lint/correctness/useExhaustiveDependencies: reload and epoch are the refresh triggers.
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    getDatabaseAccess(engine, target, database, controller.signal).then(
      (a) => {
        if (!controller.signal.aborted) {
          setAccess(a);
          setLoading(false);
        }
      },
      (e: unknown) => {
        if (!controller.signal.aborted) {
          setError(e);
          setLoading(false);
        }
      },
    );
    return () => controller.abort();
  }, [engine, target, database, reload, epoch]);

  function operationClosed(result: PlanResult, focusUser?: string) {
    setOperation(null);
    setRevoking(null);
    if (result.changed) {
      setReload((n) => n + 1);
    }
    // The button that asked may be gone: the focus goes to its row, else to the grant button.
    setTimeout(() => {
      const root = document.querySelector(`[data-access="${ids}"]`);
      const buttons = [...(root?.querySelectorAll<HTMLElement>("[data-action]") ?? [])];
      (
        buttons.find((b) => b.dataset.user === focusUser) ??
        buttons.find((b) => b.dataset.action === "grant")
      )?.focus();
    }, 0);
  }

  const users = access?.users ?? [];
  return (
    <>
      <Modal
        title={t("access.title", { database })}
        onClose={onClose}
        busy={loading}
        className="access-dialog"
        footer={
          <>
            <button type="button" className="btn btn-outline" onClick={onClose}>
              {t("plan.close")}
            </button>
            <button
              type="button"
              className="btn btn-ghost"
              disabled={loading}
              onClick={() => setReload((n) => n + 1)}
            >
              <RefreshCw size={16} className={loading ? "spin" : undefined} aria-hidden="true" />
              {t("access.refresh")}
            </button>
            <button
              type="button"
              className="btn btn-primary"
              data-action="grant"
              onClick={() => setOperation({ kind: "grant" })}
              disabled={!access}
            >
              <UserPlus size={16} aria-hidden="true" />
              {t("access.grant")}
            </button>
          </>
        }
      >
        <div data-access={ids}>
          <p className="lead">{t("access.lead", { target })}</p>
          {!access && loading && (
            <div className="plan-loading" role="status">
              <LoaderCircle size={16} className="spin tone-info" aria-hidden="true" />
              <span>{t("access.loading")}</span>
            </div>
          )}
          {error !== null && <ErrorBanner error={error} fallback={planHint(error)} />}
          {access && (
            <>
              {users.length === 0 && <p className="hint">{t("access.empty")}</p>}
              {users.length > 0 && (
                <div className="table-wrap">
                  <table className="db-table access-table">
                    <caption className="sr-only">{t("access.caption", { database })}</caption>
                    <thead>
                      <tr>
                        <th scope="col">{t("access.col.user")}</th>
                        <th scope="col">{t("access.col.login")}</th>
                        <th scope="col">{t("access.col.level")}</th>
                        <th scope="col">{t("access.col.backup")}</th>
                        <th scope="col">{t("access.col.roles")}</th>
                        <th scope="col" className="row-actions-head">
                          {t("access.col.actions")}
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {users.map((u) => (
                        <UserLine
                          key={u.name}
                          user={u}
                          onFix={() => setOperation({ kind: "fix-orphan", user: u.name })}
                          onRevoke={() => setRevoking(u)}
                        />
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
              {access.windowsPrincipals > 0 && (
                <p className="hint">{t("access.windows", { count: access.windowsPrincipals })}</p>
              )}
            </>
          )}
          {revoking && (
            <RevokeChoice
              key={revoking.name}
              user={revoking}
              onCancel={() => {
                const name = revoking.name;
                setRevoking(null);
                setTimeout(() => {
                  const root = document.querySelector(`[data-access="${ids}"]`);
                  [...(root?.querySelectorAll<HTMLElement>("[data-action='revoke']") ?? [])]
                    .find((b) => b.dataset.user === name)
                    ?.focus();
                }, 0);
              }}
              onContinue={(remove) =>
                setOperation({
                  kind: "revoke",
                  user: revoking.name,
                  login: revoking.login ?? revoking.name,
                  remove,
                })
              }
            />
          )}
        </div>
      </Modal>

      {operation?.kind === "grant" && (
        <PlanDialog
          engine={engine}
          target={target}
          operation="grant"
          request={{ database, level: "read" }}
          fields="grant"
          onClose={(r) => operationClosed(r)}
        />
      )}
      {operation?.kind === "revoke" && (
        <PlanDialog
          engine={engine}
          target={target}
          operation="revoke"
          request={{ database, login: operation.login, remove: operation.remove }}
          onClose={(r) => operationClosed(r, operation.user)}
        />
      )}
      {operation?.kind === "fix-orphan" && (
        <PlanDialog
          engine={engine}
          target={target}
          operation="fix-orphan"
          request={{ database, user: operation.user }}
          onClose={(r) => operationClosed(r, operation.user)}
        />
      )}
    </>
  );
}

function UserLine({
  user: u,
  onFix,
  onRevoke,
}: {
  user: DatabaseUser;
  onFix: () => void;
  onRevoke: () => void;
}) {
  const { t } = useI18n();
  const canRevoke = u.managed && !u.orphan && !!u.login;
  return (
    <tr className={u.orphan ? "row-orphan" : undefined}>
      <th scope="row" className="db-name">
        <span className="mono">{u.name}</span>
        {u.orphan && (
          <span className="badge badge-warning" title={t("access.orphanHint")}>
            <TriangleAlert size={12} aria-hidden="true" />
            {t("access.orphan")}
            <span className="sr-only">: {t("access.orphanHint")}</span>
          </span>
        )}
        {!u.managed && (
          <span className="badge badge-skipped" title={t("access.notManagedHint")}>
            <Lock size={12} aria-hidden="true" />
            {t("access.notManaged")}
            <span className="sr-only">: {t("access.notManagedHint")}</span>
          </span>
        )}
      </th>
      <td className="mono">{u.login ?? <span className="muted">{t("access.noLogin")}</span>}</td>
      <td>{t(LEVEL_KEY[u.level])}</td>
      <td>
        {u.backup ? (
          <span>
            <Check size={14} className="tone-success" aria-hidden="true" />
            <span className="sr-only">{t("access.backupYes")}</span>
          </span>
        ) : u.backupPartial ? (
          <span className="badge badge-warning" title={t("access.backupPartialHint")}>
            {t("access.backupPartial")}
            <span className="sr-only">: {t("access.backupPartialHint")}</span>
          </span>
        ) : (
          <span className="muted">{t("access.backupNo")}</span>
        )}
      </td>
      <td className="mono roles">
        {u.roles.length > 0 ? (
          u.roles.join(", ")
        ) : (
          <span className="muted">{t("access.noRoles")}</span>
        )}
      </td>
      <td className="row-actions">
        {u.managed && u.orphan && (
          <button
            type="button"
            className="btn btn-outline btn-sm"
            data-action="fix"
            data-user={u.name}
            aria-label={t("access.fixOrphanOf", { user: u.name })}
            onClick={onFix}
          >
            {t("access.fixOrphan")}
          </button>
        )}
        {canRevoke && (
          <button
            type="button"
            className="btn btn-ghost btn-sm btn-danger-text"
            data-action="revoke"
            data-user={u.name}
            aria-label={t("access.revokeOf", { user: u.name })}
            onClick={onRevoke}
          >
            {t("access.revoke")}
          </button>
        )}
      </td>
    </tr>
  );
}

/** What a revoke takes back, before its preview: the level, the permission to back up, or the user. */
function RevokeChoice({
  user,
  onContinue,
  onCancel,
}: {
  user: DatabaseUser;
  onContinue: (remove: RemoveWhat) => void;
  onCancel: () => void;
}) {
  const { t } = useI18n();
  const ids = useId();
  const options: {
    what: RemoveWhat;
    label: "revoke.level" | "revoke.backup" | "revoke.user";
    hint: "revoke.levelHint" | "revoke.backupHint" | "revokeUser.hint";
    off: boolean;
  }[] = [
    { what: "level", label: "revoke.level", hint: "revoke.levelHint", off: user.level === "none" },
    {
      what: "backup",
      label: "revoke.backup",
      hint: "revoke.backupHint",
      off: !user.backup && !user.backupPartial,
    },
    { what: "user", label: "revoke.user", hint: "revokeUser.hint", off: false },
  ];
  const first = options.find((o) => !o.off)?.what ?? "user";
  const [what, setWhat] = useState<RemoveWhat>(first);
  const section = useRef<HTMLElement>(null);

  // The question appears below the table: take the focus to it.
  useEffect(() => {
    section.current?.querySelector<HTMLInputElement>("input:checked")?.focus();
  }, []);

  return (
    <section
      ref={section}
      className="form-section revoke-choice"
      aria-labelledby={`${ids}-title`}
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          // Closes the question, not the panel.
          e.preventDefault();
          e.stopPropagation();
          onCancel();
        }
      }}
    >
      <h3 id={`${ids}-title`} className="plan-heading">
        {t("revoke.title", { user: user.name })}
      </h3>
      <div role="radiogroup" aria-label={t("revoke.legend")}>
        {options.map((o) => (
          <div key={o.what} className="check">
            <input
              id={`${ids}-${o.what}`}
              type="radio"
              name={`${ids}-what`}
              checked={what === o.what}
              disabled={o.off}
              aria-describedby={`${ids}-${o.what}-hint`}
              onChange={() => setWhat(o.what)}
            />
            <div>
              <label htmlFor={`${ids}-${o.what}`}>{t(o.label)}</label>
              <p id={`${ids}-${o.what}-hint`} className="hint">
                {t(o.hint)}
              </p>
            </div>
          </div>
        ))}
      </div>
      <div className="actions actions-start">
        <button type="button" className="btn btn-outline" onClick={onCancel}>
          {t("plan.cancel")}
        </button>
        <button type="button" className="btn btn-primary" onClick={() => onContinue(what)}>
          {t("revoke.preview")}
        </button>
      </div>
    </section>
  );
}
