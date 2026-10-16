import { ChevronDown, LoaderCircle } from "lucide-react";
import { type KeyboardEvent, useEffect, useId, useState } from "react";
import { bucketListDenied, type DiscoveredBucket, listBuckets } from "./api";
import type { EngineInfo } from "./engine";
import { ErrorBanner } from "./Feedback";
import { useI18n } from "./i18n";

/**
 * The S3 bucket of a target: an editable combobox (ARIA 1.2, list
 * autocomplete) filled with the buckets of the profile in the region. What
 * is typed is the value, so a bucket that is not listed, or a listing that
 * fails (403 without s3:ListAllMyBuckets), never blocks the form.
 */
export function BucketField({
  id,
  engine,
  profile,
  region,
  value,
  onChange,
  hint,
  error,
}: {
  id: string;
  engine: EngineInfo;
  profile: string;
  region: string;
  value: string;
  onChange: (value: string) => void;
  hint: string;
  error?: string;
}) {
  const { t } = useI18n();
  const listId = useId();
  const [buckets, setBuckets] = useState<DiscoveredBucket[] | null>(null);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [open, setOpen] = useState(false);
  // True once the user types: only then the list is filtered by the value.
  const [filtering, setFiltering] = useState(false);
  const [active, setActive] = useState(-1);

  useEffect(() => {
    const controller = new AbortController();
    setBuckets(null);
    setLoadError(null);
    listBuckets(engine, profile, region, controller.signal).then(
      (list) => !controller.signal.aborted && setBuckets(list),
      (e: unknown) => !controller.signal.aborted && setLoadError(e),
    );
    return () => controller.abort();
  }, [engine, profile, region]);

  const loading = buckets === null && loadError === null;
  const needle = value.trim().toLowerCase();
  const shown = (buckets ?? []).filter((b) => !filtering || b.name.toLowerCase().includes(needle));
  const expanded = open && shown.length > 0;
  const optionId = (index: number) => `${listId}-${index}`;

  function choose(name: string) {
    onChange(name);
    setOpen(false);
    setFiltering(false);
    setActive(-1);
  }

  function move(step: number) {
    if (shown.length === 0) {
      return;
    }
    setOpen(true);
    setActive((a) =>
      a < 0 ? (step > 0 ? 0 : shown.length - 1) : (a + step + shown.length) % shown.length,
    );
  }

  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        move(1);
        break;
      case "ArrowUp":
        event.preventDefault();
        move(-1);
        break;
      case "Enter":
        // Only a highlighted option is taken: otherwise Enter submits the form.
        if (expanded && active >= 0) {
          event.preventDefault();
          choose(shown[active].name);
        }
        break;
      case "Escape":
        // The first Escape closes the list; the dialog stays.
        if (expanded) {
          event.preventDefault();
          event.stopPropagation();
          setOpen(false);
          setActive(-1);
        }
        break;
      case "Tab":
        setOpen(false);
        break;
    }
  }

  const denied = loadError !== null && bucketListDenied(loadError);
  let status: string | undefined;
  if (loading) {
    status = t("add.bucketsLoading", { region });
  } else if (denied) {
    status = t("add.bucketsDenied");
  } else if (loadError !== null) {
    status = t("add.bucketsFailed");
  } else if (buckets && buckets.length === 0) {
    status = t("add.bucketsNone", { region });
  } else if (open && filtering && shown.length === 0) {
    status = t("add.bucketsNoMatch");
  } else if (buckets) {
    status = t("add.bucketsFound", { count: buckets.length, region });
  }

  const described = [`${id}-hint`, `${id}-status`, error && `${id}-error`]
    .filter(Boolean)
    .join(" ");

  return (
    <div className="form-field" aria-busy={loading}>
      <label htmlFor={id}>{t("add.bucket")}</label>
      <div className="combo">
        <input
          id={id}
          className="input mono"
          role="combobox"
          aria-expanded={expanded}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={expanded && active >= 0 ? optionId(active) : undefined}
          aria-required="true"
          aria-invalid={error ? true : undefined}
          aria-describedby={described}
          value={value}
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => {
            onChange(e.target.value);
            setFiltering(true);
            setOpen(true);
            setActive(-1);
          }}
          onKeyDown={onKeyDown}
          onBlur={() => setOpen(false)}
        />
        {loading ? (
          <LoaderCircle size={16} className="spin combo-icon" aria-hidden="true" />
        ) : (
          buckets &&
          buckets.length > 0 && (
            // Out of the tab order, as the ARIA authoring practices ask: the input does it all by keyboard.
            <button
              type="button"
              className="btn btn-ghost btn-sm combo-toggle"
              tabIndex={-1}
              aria-label={t("add.bucketToggle")}
              aria-expanded={expanded}
              aria-controls={listId}
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => {
                setFiltering(false);
                setOpen(!expanded);
                document.getElementById(id)?.focus();
              }}
            >
              <ChevronDown size={16} aria-hidden="true" />
            </button>
          )
        )}
      </div>
      <div
        id={listId}
        role="listbox"
        aria-label={t("add.bucket")}
        className="combo-list"
        hidden={!expanded}
      >
        {shown.map((b, index) => (
          // biome-ignore lint/a11y/useKeyWithClickEvents: the keyboard goes through the input (aria-activedescendant).
          <div
            key={b.name}
            id={optionId(index)}
            role="option"
            tabIndex={-1}
            aria-selected={b.name === value}
            className={index === active ? "combo-option is-active" : "combo-option"}
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => choose(b.name)}
          >
            {b.name}
          </div>
        ))}
      </div>
      <p id={`${id}-hint`} className="hint">
        {hint}
      </p>
      <p id={`${id}-status`} role="status" className="hint">
        {status}
      </p>
      {loadError !== null && !denied && <ErrorBanner error={loadError} />}
      {error && (
        <p id={`${id}-error`} className="field-error">
          {error}
        </p>
      )}
    </div>
  );
}
