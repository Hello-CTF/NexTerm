// Store collections that change on every session status event or workspace edit. Subscribing a component to the
// whole collection re-renders it on any change; src/app/store/selectors.ts holds the narrower hooks.
export const WHOLE_COLLECTION_HOOKS = {
  sessions: "useSessionById, useSessionStatus, useSessionSummary or useSessionList",
  workspaces: "useWorkspaceById, useActiveWorkspace or useWorkspaceList",
};

const isCollection = (name) => Object.hasOwn(WHOLE_COLLECTION_HOOKS, name);

function returnedExpression(fn) {
  const { body } = fn;
  if (body.type !== "BlockStatement") return body;
  if (body.body.length !== 1 || body.body[0].type !== "ReturnStatement") return null;
  return body.body[0].argument;
}

// Local names bound to a whole collection by a destructured selector parameter: `({ sessions }) => sessions`
// binds `sessions`, and `({ workspaces: all }) => all` binds `all`.
function destructuredCollectionBindings(pattern) {
  const bindings = new Map();
  for (const property of pattern.properties) {
    if (property.type !== "Property" || property.computed || property.key.type !== "Identifier") continue;
    if (!isCollection(property.key.name)) continue;
    const value = property.value.type === "AssignmentPattern" ? property.value.left : property.value;
    if (value.type === "Identifier") bindings.set(value.name, property.key.name);
  }
  return bindings;
}

export function selectedCollection(fn) {
  if (fn.params.length !== 1) return null;
  const [param] = fn.params;
  const body = returnedExpression(fn);
  if (!body) return null;
  if (param.type === "Identifier") {
    if (
      body.type === "MemberExpression" &&
      !body.computed &&
      body.object.type === "Identifier" &&
      body.object.name === param.name &&
      body.property.type === "Identifier" &&
      isCollection(body.property.name)
    ) {
      return body.property.name;
    }
    return null;
  }
  if (param.type === "ObjectPattern" && body.type === "Identifier") {
    return destructuredCollectionBindings(param).get(body.name) ?? null;
  }
  return null;
}

const isFunction = (node) => node?.type === "ArrowFunctionExpression" || node?.type === "FunctionExpression";

// useShallow(selector) only changes how results are compared, so the wrapped selector is judged the same way.
function unwrapSelector(node) {
  if (node?.type === "CallExpression" && node.callee.type === "Identifier" && node.callee.name === "useShallow") {
    return node.arguments[0];
  }
  return node;
}

export const storeSubscriptionPlugin = {
  rules: {
    "no-whole-collection-subscription": {
      meta: {
        type: "problem",
        docs: { description: "Disallow subscribing to a whole UI store collection with useUi((s) => s.collection)." },
        schema: [],
      },
      create(context) {
        return {
          CallExpression(node) {
            if (node.callee.type !== "Identifier" || node.callee.name !== "useUi") return;
            const selector = unwrapSelector(node.arguments[0]);
            if (!isFunction(selector)) return;
            const collection = selectedCollection(selector);
            if (!collection) return;
            context.report({
              node,
              message: `Subscribing to the whole ${collection} collection re-renders on every change; use ${WHOLE_COLLECTION_HOOKS[collection]} from src/app/store.ts.`,
            });
          },
        };
      },
    },
  },
};
