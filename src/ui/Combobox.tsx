import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { isTouchShell } from "../app/shell";
import { isImeKeyEvent } from "./DialogHost";
import { IconCheck, IconChevronDown } from "./icons";

export interface ComboboxOption {
  value: string;
  label?: string;
  description?: string;
}

interface ComboboxProps {
  id?: string;
  ariaLabel: string;
  value: string;
  options: readonly ComboboxOption[];
  onChange: (value: string) => void;
  disabled?: boolean;
  className?: string;
  placeholder?: string;
  inputMode?: "numeric";
}

export function Combobox({ id, ariaLabel, value, options, onChange, disabled, className = "", placeholder, inputMode }: ComboboxProps) {
  const listId = useId();
  const rootRef = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const normalized = query.trim().toLocaleLowerCase();
  const filtered = options.filter((option) =>
    !normalized || option.value.toLocaleLowerCase().includes(normalized) || option.label?.toLocaleLowerCase().includes(normalized));

  useEffect(() => setActive(0), [query, open]);
  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent) => {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", onPointerDown);
    return () => document.removeEventListener("pointerdown", onPointerDown);
  }, [open]);
  useEffect(() => {
    if (open) document.getElementById(`${listId}-${active}`)?.scrollIntoView?.({ block: "nearest" });
  }, [active, listId, open]);

  const choose = (option: ComboboxOption) => {
    onChange(option.value);
    setQuery("");
    setOpen(false);
  };
  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (isImeKeyEvent(event)) return;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      setOpen(true);
      if (filtered.length) setActive((index) => (index + (event.key === "ArrowDown" ? 1 : -1) + filtered.length) % filtered.length);
    } else if (event.key === "Enter" && open && filtered[active]) {
      event.preventDefault();
      choose(filtered[active]);
    } else if (event.key === "Escape" && open) {
      event.preventDefault();
      event.stopPropagation();
      setOpen(false);
    } else if (event.key === "Tab") {
      setOpen(false);
    }
  };

  if (isTouchShell()) {
    return (
      <>
        <input id={id} className={`nx-input ${className}`} aria-label={ariaLabel} list={`${listId}-native`}
          placeholder={placeholder} inputMode={inputMode} value={value} disabled={disabled}
          onChange={(event) => onChange(event.target.value)} />
        <datalist id={`${listId}-native`}>
          {options.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
        </datalist>
      </>
    );
  }

  return (
    <div ref={rootRef} className={`nx-select-root ${className}`}>
      <input id={id} className="nx-input pr-8" role="combobox" aria-label={ariaLabel}
        aria-expanded={open} aria-autocomplete="list" aria-controls={open ? listId : undefined}
        aria-activedescendant={open && filtered[active] ? `${listId}-${active}` : undefined}
        autoComplete="off" placeholder={placeholder} inputMode={inputMode} value={value} disabled={disabled}
        onFocus={() => { setQuery(""); setOpen(true); }}
        onChange={(event) => { onChange(event.target.value); setQuery(event.target.value); setOpen(true); }}
        onKeyDown={onKeyDown} />
      <button type="button" className="nx-combobox-toggle" aria-label={`${ariaLabel}预设`} tabIndex={-1}
        disabled={disabled} onClick={() => setOpen((current) => !current)}>
        <IconChevronDown size={12} className={open ? "rotate-180" : ""} />
      </button>
      {open && filtered.length > 0 && (
        <ul id={listId} role="listbox" aria-label={`${ariaLabel}预设`} className="nx-select-popup">
          {filtered.map((option, index) => (
            <li key={option.value} id={`${listId}-${index}`} role="option" aria-selected={option.value === value}
              className={`nx-select-option ${index === active ? "is-active" : ""}`}
              onMouseEnter={() => setActive(index)} onMouseDown={(event) => event.preventDefault()} onClick={() => choose(option)}>
              <span className="nx-select-check">{option.value === value && <IconCheck size={12} />}</span>
              <span className="min-w-0 flex-1">
                <span className="block truncate">{option.label ?? option.value}</span>
                {option.description && <span className="nx-select-description">{option.description}</span>}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
