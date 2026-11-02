// IMEs capitalise the first letter and autocorrect text in editable fields by default. The HTML attributes are not
// inherited from <html> or <body>, so one capturing focusin listener sets them on whichever field is being entered,
// before the keyboard reads them. A field that declares its own value keeps it.

const TEXT_INPUT_TYPES = new Set(["", "text", "search", "email", "url", "tel", "password"]);

function isTextLike(el: Element): boolean {
  const tag = el.tagName;
  if (tag === "TEXTAREA") return true;
  if (tag === "INPUT") return TEXT_INPUT_TYPES.has((el.getAttribute("type") ?? "").trim().toLowerCase());
  const editable = el.getAttribute("contenteditable");
  return editable !== null && editable.toLowerCase() !== "false";
}

/** Adds autocapitalize="off" and autocorrect="off" to a text-like field that has not declared them. */
export function applyInputAttributes(el: Element | null | undefined): void {
  if (!el || !isTextLike(el)) return;
  if (!el.hasAttribute("autocapitalize")) el.setAttribute("autocapitalize", "off");
  if (!el.hasAttribute("autocorrect")) el.setAttribute("autocorrect", "off");
}

let installed: (() => void) | null = null;

/** Installs the document-level listener once; returns a function that removes it. */
export function installInputCapitalization(doc: Document = document): () => void {
  if (installed) return installed;
  // At the document a shadow-DOM field is retargeted to its host; the real field is first in the composed path.
  const onEvent = (event: Event) => {
    const real = event.composedPath()[0];
    applyInputAttributes(real instanceof Element ? real : (event.target as Element | null));
  };
  // pointerdown precedes focus (also for touch), for keyboards that read the attributes before focusin arrives.
  doc.addEventListener("focusin", onEvent, true);
  doc.addEventListener("pointerdown", onEvent, true);
  applyInputAttributes(doc.activeElement);
  installed = () => {
    doc.removeEventListener("focusin", onEvent, true);
    doc.removeEventListener("pointerdown", onEvent, true);
    installed = null;
  };
  return installed;
}
