import { ChevronDown, LoaderCircle } from "lucide-react";
import { type KeyboardEvent, useEffect, useId, useMemo, useState } from "react";
import { type CollationInfo, listCollations, traitsLabel } from "./collation";
import type { EngineInfo } from "./engine";
import { ErrorBanner } from "./Feedback";
import { useI18n } from "./i18n";
import { usePassword } from "./PasswordProvider";

/** A collation offered first, with why. */
export interface Suggestion {
  name: string;
  kind: "server" | "database";
}

/** At most this many options are drawn: the list has about 5,500 and a filter narrows it. */
const MAX_OPTIONS = 100;

/**
 * A collation of the instance: an editable combobox (ARIA 1.2, list
 * autocomplete, like the bucket field) filled with sys.fn_helpcollations.
 * The server and database collations come first as suggestions; each option
 * shows its traits (CI·AS·UTF8). What is typed is the value, but a name that
 * the loaded list does not have is marked invalid (the form does not go on).
 */
export function CollationPicker({
  id,
  engine,
  target,
  value,
  onChange,
  label,
  hint,
  suggestions = [],
  disabled,
}: {
  id: string;
  engine: EngineInfo;
  target: string;
  value: string;
  onChange: (value: string) => void;
  label: string;
  hint?: string;
  suggestions?: Suggestion[];
  disabled?: boolean;
}) {
  const { t } = useI18n();
  const { epoch } = usePassword();
  const listId = useId();
  const [list, setList] = useState<CollationInfo[] | null>(null);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [open, setOpen] = useState(false);
  const [filtering, setFiltering] = useState(false);
  const [active, setActive] = useState(-1);

  // epoch: a SQL password was saved, so a list that failed (428) is read again.
  useEffect(() => {
    const controller = new AbortController();
    setLoadError(null);
    listCollations(engine, target, controller.signal, epoch).then(
      (l) => !controller.signal.aborted && setList(l),
      (e: unknown) => !controller.signal.aborted && setLoadError(e),
    );
    return () => controller.abort();
  }, [engine, target, epoch]);

  const loading = list === null && loadError === null;
  const needle = value.trim().toLowerCase();
  const options = useMemo(() => {
    if (!list) {
      return [];
    }
    const byName = new Map(list.map((c) => [c.name, c]));
    const top: { info: CollationInfo; kind?: Suggestion["kind"] }[] = [];
    if (!filtering) {
      for (const s of suggestions) {
        const info = byName.get(s.name);
        if (info && !top.some((o) => o.info.name === s.name)) {
          top.push({ info, kind: s.kind });
        }
      }
    }
    const rest = list
      .filter(
        (c) =>
          (!filtering || c.name.toLowerCase().includes(needle)) &&
          !top.some((o) => o.info.name === c.name),
      )
      .map((info) => ({ info, kind: undefined }));
    return [...top, ...rest].slice(0, MAX_OPTIONS);
  }, [list, filtering, needle, suggestions]);

  const expanded = open && options.length > 0;
  const optionId = (index: number) => `${listId}-${index}`;
  const known = list ? list.some((c) => c.name.toLowerCase() === needle) : true;
  const invalid = !!list && needle !== "" && !known;

  function choose(name: string) {
    onChange(name);
    setOpen(false);
    setFiltering(false);
    setActive(-1);
  }

  function move(step: number) {
    if (options.length === 0) {
      return;
    }
    setOpen(true);
    setActive((a) =>
      a < 0 ? (step > 0 ? 0 : options.length - 1) : (a + step + options.length) % options.length,
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
          // The form behind must not take this Enter as "preview".
          event.stopPropagation();
          choose(options[active]?.info.name ?? value);
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

  let status: string | undefined;
  if (loading) {
    status = t("collation.picker.loading");
  } else if (invalid) {
    status = t("collation.picker.none");
  } else if (list && open && filtering) {
    status = t("collation.picker.found", { count: options.length });
  }

  const described = [hint && `${id}-hint`, `${id}-status`].filter(Boolean).join(" ");
  return (
    <div className="form-field collation-picker" aria-busy={loading}>
      <label htmlFor={id}>{label}</label>
      <div className="combo">
        <input
          id={id}
          className="input mono"
          role="combobox"
          aria-expanded={expanded}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={expanded && active >= 0 ? optionId(active) : undefined}
          aria-invalid={invalid || undefined}
          aria-describedby={described}
          placeholder={t("collation.picker.placeholder")}
          value={value}
          disabled={disabled}
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => {
            onChange(e.target.value);
            setFiltering(true);
            setOpen(true);
            setActive(-1);
          }}
          onFocus={() => setOpen(true)}
          onKeyDown={onKeyDown}
          onBlur={() => setOpen(false)}
        />
        {loading ? (
          <LoaderCircle size={16} className="spin combo-icon" aria-hidden="true" />
        ) : (
          list && (
            <button
              type="button"
              className="btn btn-ghost btn-sm combo-toggle"
              tabIndex={-1}
              disabled={disabled}
              aria-label={t("collation.picker.toggle")}
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
        aria-label={label}
        className="combo-list collation-list"
        hidden={!expanded}
      >
        {options.map(({ info, kind }, index) => (
          // biome-ignore lint/a11y/useKeyWithClickEvents: the keyboard goes through the input (aria-activedescendant).
          <div
            key={info.name}
            id={optionId(index)}
            role="option"
            tabIndex={-1}
            aria-selected={info.name.toLowerCase() === needle}
            className={index === active ? "combo-option is-active" : "combo-option"}
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => choose(info.name)}
          >
            <span>{info.name}</span>
            <span className="collation-traits">
              {kind === "server" && (
                <span className="badge badge-skipped">{t("collation.picker.suggestServer")}</span>
              )}
              {kind === "database" && (
                <span className="badge badge-skipped">{t("collation.picker.suggestDatabase")}</span>
              )}
              <span>{traitsLabel(info)}</span>
            </span>
          </div>
        ))}
      </div>
      {hint && (
        <p id={`${id}-hint`} className="hint">
          {hint}
        </p>
      )}
      <p id={`${id}-status`} role="status" className={invalid ? "field-error" : "hint"}>
        {status}
      </p>
      {loadError !== null && <ErrorBanner error={loadError} />}
    </div>
  );
}
