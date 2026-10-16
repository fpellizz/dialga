import { LoaderCircle, Search, TriangleAlert, X } from "lucide-react";
import { type FormEvent, useEffect, useRef, useState } from "react";
import {
  type AwsProfile,
  type DiscoveredInstance,
  discoverInstances,
  type InstanceDiscovery,
  listProfiles,
  saveTarget,
  type TargetSummary,
} from "./api";
import { engineLabel, instanceBadge, instanceStatusKey } from "./databases";
import type { EngineInfo } from "./engine";
import { ErrorBanner } from "./Feedback";
import { useI18n } from "./i18n";
import { Segmented } from "./SettingsPage";
import {
  applyInstance,
  CONNECT_MODES,
  type EditedFields,
  emptyForm,
  type FormErrors,
  type FormField,
  firstInvalid,
  initialProfile,
  regionFor,
  type TargetForm,
  toTargetInput,
  validateForm,
} from "./targetForm";

const ID = "add-instance";
const fieldId = (field: FormField) => `${ID}-${field}`;

/** The ids that describe a field: its hint and its error, when shown. */
function describedBy(field: FormField, hint: boolean, error: boolean): string | undefined {
  const ids = [hint && `${fieldId(field)}-hint`, error && `${fieldId(field)}-error`].filter(
    Boolean,
  );
  return ids.length > 0 ? ids.join(" ") : undefined;
}

function FieldMessages({
  field,
  hint,
  error,
}: {
  field: FormField;
  hint?: string;
  error?: string;
}) {
  return (
    <>
      {hint && (
        <p id={`${fieldId(field)}-hint`} className="hint">
          {hint}
        </p>
      )}
      {error && (
        <p id={`${fieldId(field)}-error`} className="field-error">
          {error}
        </p>
      )}
    </>
  );
}

function TextField({
  field,
  label,
  value,
  onChange,
  hint,
  error,
  mono = false,
  numeric = false,
}: {
  field: FormField;
  label: string;
  value: string;
  onChange: (value: string) => void;
  hint?: string;
  error?: string;
  mono?: boolean;
  numeric?: boolean;
}) {
  return (
    <div className="form-field">
      <label htmlFor={fieldId(field)}>{label}</label>
      {/* Every field of this form is required. */}
      <input
        id={fieldId(field)}
        className={mono ? "input mono" : "input"}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        inputMode={numeric ? "numeric" : undefined}
        autoComplete="off"
        spellCheck={false}
        aria-required="true"
        aria-invalid={error ? true : undefined}
        aria-describedby={describedBy(field, Boolean(hint), Boolean(error))}
      />
      <FieldMessages field={field} hint={hint} error={error} />
    </div>
  );
}

/** The RDS status of a discovered instance, translated when known, as on the Instances cards. */
function InstanceStatus({ status }: { status: string }) {
  const { t } = useI18n();
  const key = instanceStatusKey(status);
  return <span className={`badge ${instanceBadge(status)}`}>{key ? t(key) : status}</span>;
}

/**
 * The guided form that adds a target: AWS profile and region, discovery of
 * the SQL Server instances, then bucket, SQL login, password source,
 * connection and name. A native modal dialog: it keeps the focus inside and
 * closes on Escape; the focus goes back where it was.
 */
export function AddInstanceDialog({
  engine,
  existing,
  onClose,
  onSaved,
}: {
  engine: EngineInfo;
  /** Names already in the configuration. */
  existing: string[];
  onClose: () => void;
  onSaved: (target: TargetSummary) => void;
}) {
  const { t } = useI18n();
  const dialogRef = useRef<HTMLDialogElement>(null);
  const discoverAbort = useRef<AbortController | null>(null);
  const [form, setForm] = useState<TargetForm>(emptyForm);
  const [edited, setEdited] = useState<EditedFields>(new Set());
  const [profiles, setProfiles] = useState<AwsProfile[] | null>(null);
  const [profilesError, setProfilesError] = useState<unknown>(null);
  const [discovery, setDiscovery] = useState<InstanceDiscovery | null>(null);
  const [discovering, setDiscovering] = useState(false);
  const [discoveryError, setDiscoveryError] = useState<unknown>(null);
  const [errors, setErrors] = useState<FormErrors>({});
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<unknown>(null);

  // The control that opened the dialog, read while rendering: by the time an
  // effect runs again (React strict mode) showModal has moved the focus inside.
  const [opener] = useState(() =>
    document.activeElement instanceof HTMLElement ? document.activeElement : null,
  );

  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog && !dialog.open) {
      dialog.showModal();
    }
    return () => {
      discoverAbort.current?.abort();
      opener?.focus();
    };
  }, [opener]);

  useEffect(() => {
    let live = true;
    listProfiles(engine).then(
      (list) => {
        if (!live) {
          return;
        }
        setProfiles(list);
        const profile = initialProfile(list);
        setForm((f) => ({ ...f, profile, region: regionFor(list, profile) }));
      },
      (e: unknown) => live && setProfilesError(e),
    );
    return () => {
      live = false;
    };
  }, [engine]);

  // The first field takes the focus once it can be used (the profiles loaded).
  const ready = profiles !== null || profilesError !== null;
  useEffect(() => {
    if (ready) {
      document.getElementById(fieldId("profile"))?.focus();
    }
  }, [ready]);

  function set<K extends keyof TargetForm>(
    key: K,
    value: TargetForm[K],
    mark?: FormField | "protected",
  ) {
    setForm((f) => ({ ...f, [key]: value }));
    if (mark) {
      setEdited((e) => new Set(e).add(mark));
    }
    setErrors((errs) =>
      key in errs
        ? (Object.fromEntries(Object.entries(errs).filter(([k]) => k !== key)) as FormErrors)
        : errs,
    );
  }

  function focusField(field: FormField) {
    const id =
      field === "instance" && !discovery?.instances.length ? `${ID}-discover` : fieldId(field);
    document.getElementById(id)?.focus();
  }

  function changeProfile(profile: string) {
    set("profile", profile, "profile");
    if (!edited.has("region")) {
      setForm((f) => ({ ...f, region: regionFor(profiles ?? [], profile) }));
    }
    // Another profile can be another account: discover again.
    setDiscovery(null);
    setForm((f) => ({ ...f, instance: "", accountId: "" }));
  }

  function pickInstance(instance: DiscoveredInstance, accountId: string) {
    setForm((f) => applyInstance(f, instance, accountId, edited));
    setErrors(({ instance: _, ...rest }) => rest);
  }

  async function discover() {
    const { profile, region } = validateForm(form, existing);
    if (profile || region) {
      setErrors((e) => ({ ...e, profile, region }));
      focusField(profile ? "profile" : "region");
      return;
    }
    discoverAbort.current?.abort();
    const controller = new AbortController();
    discoverAbort.current = controller;
    setDiscovering(true);
    setDiscoveryError(null);
    try {
      const found = await discoverInstances(engine, form.profile, form.region, controller.signal);
      if (controller.signal.aborted) {
        return;
      }
      setDiscovery(found);
      const keep =
        found.instances.find((i) => i.identifier === form.instance) ??
        (found.instances.length === 1 ? found.instances[0] : undefined);
      if (keep) {
        pickInstance(keep, found.accountId);
      } else {
        setForm((f) => ({ ...f, instance: "", accountId: "" }));
      }
    } catch (e) {
      if (!controller.signal.aborted) {
        setDiscoveryError(e);
      }
    } finally {
      if (discoverAbort.current === controller) {
        discoverAbort.current = null;
        setDiscovering(false);
      }
    }
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (saving) {
      return;
    }
    const found = validateForm(form, existing);
    setErrors(found);
    const first = firstInvalid(found);
    if (first) {
      focusField(first);
      return;
    }
    setSaving(true);
    setSaveError(null);
    try {
      onSaved(await saveTarget(engine, toTargetInput(form)));
    } catch (e) {
      setSaveError(e);
      setSaving(false);
    }
  }

  const selected = discovery?.instances.find((i) => i.identifier === form.instance);
  const err = (field: FormField) => {
    const key = errors[field];
    return key ? t(key) : undefined;
  };
  const nameTaken = existing.includes(form.name);

  return (
    <dialog
      ref={dialogRef}
      className="dialog"
      aria-labelledby={`${ID}-title`}
      aria-describedby={`${ID}-lead`}
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
    >
      <form className="dialog-form" onSubmit={submit} noValidate>
        {/* Divs, not header/footer: inside a dialog those would add banner and contentinfo landmarks. */}
        <div className="dialog-head">
          <div className="dialog-title">
            <h2 id={`${ID}-title`}>{t("add.title")}</h2>
            <button
              type="button"
              className="btn btn-ghost btn-sm"
              onClick={onClose}
              aria-label={t("add.close")}
              title={t("add.close")}
            >
              <X size={16} aria-hidden="true" />
            </button>
          </div>
          <p id={`${ID}-lead`} className="muted">
            {t("add.lead")}
          </p>
        </div>

        <div className="dialog-body">
          <fieldset className="form-section">
            <legend>{t("add.aws")}</legend>
            {profilesError !== null && <ErrorBanner error={profilesError} />}
            <div className="form-row">
              <div className="form-field">
                <label htmlFor={fieldId("profile")}>{t("add.profile")}</label>
                {profiles && profiles.length > 0 ? (
                  <select
                    id={fieldId("profile")}
                    className="input"
                    value={form.profile}
                    onChange={(e) => changeProfile(e.target.value)}
                    aria-required="true"
                    aria-invalid={errors.profile ? true : undefined}
                    aria-describedby={describedBy("profile", true, Boolean(errors.profile))}
                  >
                    {profiles.map((p) => (
                      <option key={p.name} value={p.name}>
                        {p.region ? `${p.name} (${p.region})` : p.name}
                      </option>
                    ))}
                  </select>
                ) : (
                  <input
                    id={fieldId("profile")}
                    className="input"
                    value={form.profile}
                    onChange={(e) => changeProfile(e.target.value)}
                    disabled={profiles === null && profilesError === null}
                    autoComplete="off"
                    spellCheck={false}
                    aria-required="true"
                    aria-invalid={errors.profile ? true : undefined}
                    aria-describedby={describedBy("profile", true, Boolean(errors.profile))}
                  />
                )}
                <FieldMessages
                  field="profile"
                  hint={profiles?.length === 0 ? t("add.noProfiles") : t("add.profileHint")}
                  error={err("profile")}
                />
              </div>
              <TextField
                field="region"
                label={t("add.region")}
                value={form.region}
                onChange={(v) => {
                  set("region", v, "region");
                  setDiscovery(null);
                }}
                error={err("region")}
                mono
              />
            </div>
            <div className="actions actions-start">
              <button
                id={`${ID}-discover`}
                type="button"
                className="btn btn-outline"
                onClick={discover}
                disabled={profiles === null}
              >
                {discovering ? (
                  <LoaderCircle size={16} className="spin" aria-hidden="true" />
                ) : (
                  <Search size={16} aria-hidden="true" />
                )}
                {discovering ? t("add.discovering") : t("add.discover")}
              </button>
            </div>
            {discoveryError !== null && <ErrorBanner error={discoveryError} />}
          </fieldset>

          <fieldset className="form-section" aria-busy={discovering}>
            <legend>{t("add.instance")}</legend>
            <div role="status" className="muted">
              {discovery
                ? discovery.instances.length > 0
                  ? t("add.account", {
                      account: discovery.accountId,
                      count: discovery.instances.length,
                    })
                  : t("add.noInstances", { region: discovery.region })
                : !discovering && t("add.discoverFirst")}
            </div>
            {discovering && (
              <div aria-hidden="true">
                <div className="skeleton skeleton-row" />
                <div className="skeleton skeleton-row" />
              </div>
            )}
            {discovery && discovery.instances.length > 0 && (
              <div className="choices">
                {discovery.instances.map((inst, index) => (
                  <label key={inst.identifier} className="choice">
                    <input
                      id={index === 0 ? fieldId("instance") : undefined}
                      type="radio"
                      name={`${ID}-instance`}
                      value={inst.identifier}
                      checked={form.instance === inst.identifier}
                      onChange={() => pickInstance(inst, discovery.accountId)}
                      aria-describedby={
                        errors.instance ? `${fieldId("instance")}-error` : undefined
                      }
                    />
                    <span className="choice-body">
                      <span className="mono choice-title">{inst.identifier}</span>
                      <span className="choice-meta">
                        <span title={`${inst.engine} ${inst.engineVersion}`}>
                          {engineLabel(inst.engine, inst.engineVersion)}
                        </span>
                        <span className="mono">{inst.class}</span>
                        <InstanceStatus status={inst.status} />
                        <span
                          className={`badge ${inst.backupRestore ? "badge-ok" : "badge-warning"}`}
                        >
                          {inst.backupRestore ? t("add.backupOk") : t("add.backupMissing")}
                        </span>
                      </span>
                    </span>
                  </label>
                ))}
              </div>
            )}
            {selected && !selected.backupRestore && (
              <div className="banner banner-warn" role="status">
                <TriangleAlert size={16} aria-hidden="true" />
                <span className="banner-text">{t("add.backupMissingHint")}</span>
              </div>
            )}
            <FieldMessages field="instance" error={err("instance")} />
          </fieldset>

          {selected && (
            <fieldset className="form-section">
              <legend>{t("add.details")}</legend>
              <TextField
                field="bucket"
                label={t("add.bucket")}
                value={form.bucket}
                onChange={(v) => set("bucket", v)}
                hint={t("add.bucketHint")}
                error={err("bucket")}
                mono
              />
              <TextField
                field="sqlUser"
                label={t("add.sqlUser")}
                value={form.sqlUser}
                onChange={(v) => set("sqlUser", v, "sqlUser")}
                hint={t("add.sqlUserHint")}
                error={err("sqlUser")}
                mono
              />

              <fieldset className="field" aria-describedby={`${ID}-password-note`}>
                <legend>{t("add.password")}</legend>
                <p id={`${ID}-password-note`} className="hint">
                  {t("add.passwordNote")}
                </p>
                <div className="check">
                  <input
                    id={`${ID}-prompt`}
                    type="radio"
                    name={`${ID}-password`}
                    checked={form.passwordKind === "prompt"}
                    onChange={() => set("passwordKind", "prompt")}
                    aria-describedby={`${ID}-prompt-hint`}
                  />
                  <div>
                    <label htmlFor={`${ID}-prompt`}>{t("add.passwordPrompt")}</label>
                    <p id={`${ID}-prompt-hint`} className="hint">
                      {t("add.passwordPromptHint")}
                    </p>
                  </div>
                </div>
                <label className="check">
                  <input
                    type="radio"
                    name={`${ID}-password`}
                    checked={form.passwordKind === "env"}
                    onChange={() => set("passwordKind", "env")}
                  />
                  <span>{t("add.passwordEnv")}</span>
                </label>
                {form.passwordKind === "env" && (
                  <div className="indent">
                    <TextField
                      field="envVar"
                      label={t("add.envVar")}
                      value={form.envVar}
                      onChange={(v) => set("envVar", v)}
                      error={err("envVar")}
                      mono
                    />
                  </div>
                )}
              </fieldset>

              <Segmented
                legend={t("add.connection")}
                name={`${ID}-mode`}
                value={form.connectMode}
                options={CONNECT_MODES.map((value) => ({ value, label: t(`add.mode.${value}`) }))}
                onChange={(mode) => set("connectMode", mode)}
              />
              <p className="hint">{t(`add.modeHint.${form.connectMode}`)}</p>
              {form.connectMode === "external" && (
                <TextField
                  field="localPort"
                  label={t("add.localPort")}
                  value={form.localPort}
                  onChange={(v) => set("localPort", v)}
                  error={err("localPort")}
                  mono
                  numeric
                />
              )}

              <TextField
                field="name"
                label={t("add.name")}
                value={form.name}
                onChange={(v) => set("name", v, "name")}
                hint={t("add.nameHint")}
                error={err("name")}
                mono
              />
              {nameTaken && (
                <label className="check">
                  <input
                    type="checkbox"
                    checked={form.replace}
                    onChange={(e) => {
                      set("replace", e.target.checked);
                      setErrors(({ name: _, ...rest }) => rest);
                    }}
                  />
                  <span>{t("add.replace", { name: form.name })}</span>
                </label>
              )}
              <div className="check">
                <input
                  id={`${ID}-protected`}
                  type="checkbox"
                  checked={form.protected}
                  onChange={(e) => set("protected", e.target.checked, "protected")}
                  aria-describedby={`${ID}-protected-hint`}
                />
                <div>
                  <label htmlFor={`${ID}-protected`}>{t("add.protected")}</label>
                  <p id={`${ID}-protected-hint`} className="hint">
                    {t("add.protectedHint")}
                  </p>
                </div>
              </div>
            </fieldset>
          )}
        </div>

        <div className="dialog-foot">
          {saveError !== null && <ErrorBanner error={saveError} preformatted />}
          {saveError === null && Object.keys(errors).some((k) => errors[k as FormField]) && (
            <p className="field-error" role="alert">
              {t("add.invalid")}
            </p>
          )}
          <div className="actions">
            <button type="button" className="btn btn-outline" onClick={onClose}>
              {t("add.cancel")}
            </button>
            {/* Busy, not disabled: a disabled button would drop the focus out of the form. */}
            <button type="submit" className="btn btn-primary" aria-disabled={saving || undefined}>
              {saving && <LoaderCircle size={16} className="spin" aria-hidden="true" />}
              {saving ? t("add.saving") : t("add.save")}
            </button>
          </div>
        </div>
      </form>
    </dialog>
  );
}
