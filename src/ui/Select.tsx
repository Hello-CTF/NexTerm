import { useEffect, useId, useLayoutEffect, useRef, useState, type CSSProperties, type KeyboardEvent } from "react";
import { isTouchShell } from "../app/shell";
import { IconCheck, IconChevronDown } from "./icons";

export interface SelectOption {
  value: string;
  label: string;
  description?: string;
  disabled?: boolean;
}

interface SelectProps {
  id?: string;
  ariaLabel: string;
  value: string;
  options: readonly SelectOption[];
  onChange: (value: string) => void;
  disabled?: boolean;
  className?: string;
  placeholder?: string;
  // title is the tooltip of the control, for a toolbar select whose label is only its current value.
  title?: string;
  // triggerClassName replaces the look of the trigger (default: an input), for a select that sits in a toolbar as a
  // small button. The open list is unaffected.
  triggerClassName?: string;
}

// popupPlacement puts the list under the trigger, or above it when there is little room below. The list is
// position: fixed, so a toolbar, table or dialog with overflow set does not clip or scroll it.
function popupPlacement(trigger: HTMLElement): CSSProperties {
  const rect = trigger.getBoundingClientRect();
  const margin = 8;
  const width = Math.max(rect.width, 180);
  const below = window.innerHeight - rect.bottom - margin;
  const above = rect.top - margin;
  const flip = below < 160 && above > below;
  return {
    left: Math.max(margin, Math.min(rect.left, window.innerWidth - width - margin)),
    minWidth: width,
    maxHeight: Math.max(120, Math.min(280, flip ? above : below)),
    ...(flip ? { bottom: window.innerHeight - rect.top + 4 } : { top: rect.bottom + 4 }),
  };
}

export function Select({ id, ariaLabel, value, options, onChange, disabled, className = "", placeholder = "请选择", title, triggerClassName }: SelectProps) {
  const listId = useId();
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const [open, setOpen] = useState(false);
  const selectedIndex = options.findIndex((option) => option.value === value);
  const [active, setActive] = useState(Math.max(0, selectedIndex));
  const [placement, setPlacement] = useState<CSSProperties>({});
  const selected = selectedIndex >= 0 ? options[selectedIndex] : undefined;

  useLayoutEffect(() => {
    if (open && triggerRef.current) setPlacement(popupPlacement(triggerRef.current));
  }, [open]);

  useEffect(() => {
    if (!open) return;
    // The fixed list does not follow its trigger, so a scroll outside the list or a resize closes it.
    const close = (event: Event) => {
      if (event.target instanceof Node && document.getElementById(listId)?.contains(event.target)) return;
      setOpen(false);
    };
    window.addEventListener("scroll", close, true);
    window.addEventListener("resize", close);
    return () => {
      window.removeEventListener("scroll", close, true);
      window.removeEventListener("resize", close);
    };
  }, [open, listId]);

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent) => {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", onPointerDown);
    return () => document.removeEventListener("pointerdown", onPointerDown);
  }, [open]);

  useEffect(() => {
    if (!open) return;
    document.getElementById(`${listId}-${active}`)?.scrollIntoView?.({ block: "nearest" });
  }, [active, listId, open]);

  const choose = (index: number) => {
    const option = options[index];
    if (!option || option.disabled) return;
    onChange(option.value);
    setOpen(false);
    triggerRef.current?.focus();
  };
  const move = (offset: number) => {
    if (!options.length) return;
    let next = active;
    for (let count = 0; count < options.length; count += 1) {
      next = (next + offset + options.length) % options.length;
      if (!options[next]?.disabled) break;
    }
    setActive(next);
  };
  const onKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (disabled) return;
    if (event.key === "Escape" && open) {
      event.preventDefault();
      setOpen(false);
    } else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      if (!open) {
        setActive(Math.max(0, selectedIndex));
        setOpen(true);
      } else {
        move(event.key === "ArrowDown" ? 1 : -1);
      }
    } else if ((event.key === "Enter" || event.key === " ") && open) {
      event.preventDefault();
      choose(active);
    } else if (event.key === "Home" && open) {
      event.preventDefault();
      setActive(0);
    } else if (event.key === "End" && open) {
      event.preventDefault();
      setActive(options.length - 1);
    } else if (event.key === "Tab") {
      setOpen(false);
    }
  };

  if (isTouchShell()) {
    return (
      <select id={id} className={`nx-select ${className}`} aria-label={ariaLabel} title={title} value={value} disabled={disabled}
        onChange={(event) => onChange(event.target.value)}>
        {!selected && <option value="" disabled>{placeholder}</option>}
        {options.map((option) => (
          <option key={option.value} value={option.value} disabled={option.disabled}>
            {option.label}{option.description ? ` · ${option.description}` : ""}
          </option>
        ))}
      </select>
    );
  }

  return (
    <div ref={rootRef} className={`nx-select-root ${className}`}>
      <button ref={triggerRef} id={id} type="button" className={triggerClassName ?? "nx-input nx-select-trigger"} title={title}
        aria-label={ariaLabel} aria-haspopup="listbox" aria-expanded={open} aria-controls={open ? listId : undefined}
        aria-activedescendant={open && options[active] ? `${listId}-${active}` : undefined}
        disabled={disabled} onClick={() => {
          if (!open) setActive(Math.max(0, selectedIndex));
          setOpen((current) => !current);
        }} onKeyDown={onKeyDown}>
        <span className={`truncate ${selected ? "" : "text-neutral-500"}`}>{selected?.label ?? placeholder}</span>
        <IconChevronDown size={12} className={`shrink-0 transition-transform ${open ? "rotate-180" : ""}`} />
      </button>
      {open && (
        <ul id={listId} role="listbox" aria-label={ariaLabel} className="nx-select-popup" style={placement}>
          {options.map((option, index) => (
            <li key={option.value} id={`${listId}-${index}`} role="option" aria-selected={option.value === value}
              aria-disabled={option.disabled || undefined}
              className={`nx-select-option ${index === active ? "is-active" : ""} ${option.disabled ? "is-disabled" : ""}`}
              onMouseEnter={() => setActive(index)} onMouseDown={(event) => event.preventDefault()} onClick={() => choose(index)}>
              <span className="nx-select-check">{option.value === value && <IconCheck size={12} />}</span>
              <span className="min-w-0 flex-1">
                <span className="block truncate">{option.label}</span>
                {option.description && <span className="nx-select-description">{option.description}</span>}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
