import { readFileSync } from "node:fs";
import path from "node:path";
import { compile } from "tailwindcss";

function dirname(file: string): string {
  return path.resolve(file, "..");
}

export async function installCompiledCSS(source: string): Promise<HTMLStyleElement> {
  const compiler = await compile(readFileSync(source, "utf8"), {
    base: path.resolve(dirname(source)),
    loadStylesheet: (id, base) => {
      // Local partials are inlined; package imports such as tailwindcss stay empty.
      if (!id.startsWith(".")) return Promise.resolve({ path: id, base, content: "" });
      const file = path.resolve(base, id);
      return Promise.resolve({
        path: file,
        base: dirname(file),
        content: readFileSync(file, "utf8"),
      });
    },
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

const CONDITIONAL_RULES = new Set(["CSSMediaRule", "CSSContainerRule"]);

/** Conditions (media or container queries) of the at-rules enclosing a rule, innermost first. */
export function ruleConditions(rule: CSSRule): string[] {
  const conditions: string[] = [];
  for (let parent = rule.parentRule; parent; parent = parent.parentRule) {
    if (!CONDITIONAL_RULES.has(parent.constructor.name)) continue;
    const grouping = parent as CSSGroupingRule & { conditionText?: string };
    conditions.push((grouping.conditionText ?? "").trim());
  }
  return conditions;
}

/** Names of the cascade layers enclosing a rule, innermost first. */
export function ruleLayers(rule: CSSRule): string[] {
  const layers: string[] = [];
  for (let parent = rule.parentRule; parent; parent = parent.parentRule) {
    if (parent.constructor.name === "CSSLayerBlockRule") {
      layers.push((parent as CSSRule & { name: string }).name);
    }
  }
  return layers;
}

/**
 * Style rules whose selector list contains `selector`, in stylesheet order. `condition` null selects
 * rules outside any media or container query; a string selects rules inside a query with that text.
 */
export function rulesFor(
  rules: CSSStyleRule[],
  selector: string,
  condition: string | null = null,
): CSSStyleRule[] {
  return rules.filter((rule) => {
    if (!rule.selectorText.split(",").some((part) => part.trim() === selector)) return false;
    const conditions = ruleConditions(rule);
    return condition === null ? conditions.length === 0 : conditions.includes(condition);
  });
}

/** Declared value of `property` for `selector` in the given condition; the last declaration wins. */
export function declared(
  rules: CSSStyleRule[],
  selector: string,
  property: string,
  condition: string | null = null,
): string | undefined {
  const values = rulesFor(rules, selector, condition)
    .map((rule) => rule.style.getPropertyValue(property).trim())
    .filter((value) => value.length > 0);
  return values.at(-1);
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
