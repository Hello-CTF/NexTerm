import { readFileSync } from "node:fs";
import { compile } from "tailwindcss";

export async function installCompiledCSS(source: string): Promise<HTMLStyleElement> {
  const compiler = await compile(readFileSync(source, "utf8"), {
    base: ".",
    loadStylesheet: (id, base) => Promise.resolve({ path: id, base, content: "" }),
  });
  const element = document.createElement("style");
  element.textContent = compiler.build([]);
  document.head.append(element);
  return element;
}

export function flattenCSSRules(rules: CSSRuleList): CSSRule[] {
  return Array.from(rules).flatMap((rule) => [
    rule,
    ...("cssRules" in rule ? flattenCSSRules((rule as CSSGroupingRule).cssRules) : []),
  ]);
}

export function styleRules(element: HTMLStyleElement): CSSStyleRule[] {
  if (!element.sheet) throw new Error("stylesheet was not installed");
  return flattenCSSRules(element.sheet.cssRules).filter(
    (rule): rule is CSSStyleRule => rule instanceof CSSStyleRule,
  );
}

export function requireStyleRule(
  rules: CSSStyleRule[],
  selector: string,
  property?: string,
): CSSStyleRule {
  const rule = rules.find(
    (candidate) =>
      candidate.selectorText.split(",").some((part) => part.trim() === selector) &&
      (!property || candidate.style.getPropertyValue(property).trim().length > 0),
  );
  if (!rule) {
    throw new Error(`missing style rule: ${selector}${property ? ` (${property})` : ""}`);
  }
  return rule;
}

export function customProperties(rule: CSSStyleRule): Map<string, string> {
  const values = new Map<string, string>();
  for (let index = 0; index < rule.style.length; index += 1) {
    const property = rule.style.item(index);
    if (!property.startsWith("--")) continue;
    values.set(property.slice(2), rule.style.getPropertyValue(property).trim());
  }
  return values;
}
