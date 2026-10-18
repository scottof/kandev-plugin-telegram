import assert from "node:assert/strict";
import test from "node:test";

let importCount = 0;

function element(type, props, ...children) {
  return { type, props: props || {}, children: children.flat() };
}

const ui = new Proxy({}, { get: (_, name) => String(name) });

function textOf(node) {
  if (node === null || node === undefined || node === false) return "";
  if (typeof node === "string" || typeof node === "number") return String(node);
  return node.children.map(textOf).join(" ");
}

function findAll(node, predicate) {
  if (!node || typeof node !== "object") return [];
  const here = predicate(node) ? [node] : [];
  return here.concat(node.children.flatMap((child) => findAll(child, predicate)));
}

function buttonByText(tree, text) {
  const matches = findAll(tree, (n) => n.type === "Button" && textOf(n).includes(text));
  assert.equal(matches.length, 1, `one "${text}" button`);
  return matches[0];
}

function jsonResponse(status, body) {
  return { ok: status >= 200 && status < 300, status, json: async () => body };
}

function makeHost(responses = {}) {
  const calls = [];
  const toasts = [];
  const host = {
    jsx: element,
    ui,
    React: {
      useState: (initial) => [initial, () => {}],
      useEffect: (fn) => fn(),
      useCallback: (fn) => fn,
      useRef: (initial) => ({ current: initial }),
    },
    api: {
      async fetch(path, init) {
        calls.push({ path, method: init?.method });
        const handler = responses[path];
        if (!handler) throw new Error("network down");
        return handler();
      },
    },
    toast: {
      success: (msg) => toasts.push({ kind: "success", msg }),
      error: (msg) => toasts.push({ kind: "error", msg }),
    },
  };
  return { host, calls, toasts };
}

async function loadPlugin() {
  let pluginId;
  let definition;
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      registerKandevPlugin(id, pluginDefinition) {
        pluginId = id;
        definition = pluginDefinition;
      },
    },
  });
  const mod = await import(new URL(`../ui/bundle.js?settings-test=${importCount++}`, import.meta.url));
  return { pluginId, definition, mod };
}

const noop = { onRefresh() {}, onTest() {}, testing: false };

test("registers one plugin-settings component under the manifest id", async () => {
  const { pluginId, definition } = await loadPlugin();
  assert.equal(pluginId, "kandev-plugin-telegram");
  const registrations = [];
  const registry = new Proxy(
    {},
    {
      get: (_, method) => (...args) => registrations.push({ method, slot: args[0] }),
    },
  );
  definition.initialize(registry, makeHost().host);
  assert.deepEqual(registrations, [{ method: "registerComponent", slot: "plugin-settings" }]);
});

test("loading state shows a spinner", async () => {
  const { mod } = await loadPlugin();
  const tree = mod.renderStatusCard(makeHost().host, { phase: "loading" }, noop);
  assert.equal(findAll(tree, (n) => n.type === "Spinner").length, 1);
  assert.match(textOf(tree), /Loading status/);
});

test("error state explains the failure and keeps Refresh", async () => {
  const { mod } = await loadPlugin();
  const tree = mod.renderStatusCard(makeHost().host, { phase: "error", message: "HTTP 500" }, noop);
  assert.equal(findAll(tree, (n) => n.type === "Alert").length, 1);
  assert.match(textOf(tree), /Could not load the bot status/);
  assert.match(textOf(tree), /HTTP 500/);
  buttonByText(tree, "Refresh");
});

test("not configured state lists missing settings and disables Test", async () => {
  const { mod } = await loadPlugin();
  const data = { configured: false, missing: ["bot_token", "kandev_url"], running: false, connected: false };
  const tree = mod.renderStatusCard(makeHost().host, { phase: "ready", data }, noop);
  const badge = findAll(tree, (n) => n.type === "Badge")[0];
  assert.equal(textOf(badge), "Not configured");
  assert.match(textOf(tree), /Missing settings: Bot token, kandev URL/);
  assert.equal(buttonByText(tree, "Send test message").props.disabled, true);
});

test("connected state shows bot, users and last update", async () => {
  const { mod } = await loadPlugin();
  const data = {
    configured: true,
    missing: [],
    running: true,
    connected: true,
    username: "kandev_bot",
    allowed_users: 2,
    last_update_at: "",
    last_error: "",
  };
  const tree = mod.renderStatusCard(makeHost().host, { phase: "ready", data }, noop);
  assert.equal(textOf(findAll(tree, (n) => n.type === "Badge")[0]), "Connected");
  const text = textOf(tree);
  assert.match(text, /@kandev_bot/);
  assert.match(text, /Allowed users: 2/);
  assert.match(text, /Last update: Never/);
  assert.equal(buttonByText(tree, "Send test message").props.disabled, false);
  assert.doesNotMatch(text, /—/);
});

test("a stale connected flag is not shown as connected when the bot is not running", async () => {
  const { mod } = await loadPlugin();
  const data = { configured: true, missing: [], running: false, connected: true, last_error: "telegram getMe: 401 Unauthorized" };
  const tree = mod.renderStatusCard(makeHost().host, { phase: "ready", data }, noop);
  assert.equal(textOf(findAll(tree, (n) => n.type === "Badge")[0]), "Not connected");
  assert.match(textOf(tree), /Last error: telegram getMe: 401 Unauthorized/);
});

test("settings warnings are shown without blocking Test", async () => {
  const { mod } = await loadPlugin();
  const data = { configured: true, missing: [], running: true, connected: true, warnings: ["kandev_url points at this computer"] };
  const tree = mod.renderStatusCard(makeHost().host, { phase: "ready", data }, noop);
  assert.equal(textOf(findAll(tree, (n) => n.type === "Badge")[0]), "Connected");
  assert.match(textOf(tree), /Warning: kandev_url points at this computer/);
  assert.equal(buttonByText(tree, "Send test message").props.disabled, false);
});

test("loadStatus reads the status webhook and rejects on HTTP errors", async () => {
  const { mod } = await loadPlugin();
  const ok = makeHost({ "/webhooks/status": () => jsonResponse(200, { configured: true }) });
  assert.deepEqual(await mod.loadStatus(ok.host), { configured: true });
  assert.deepEqual(ok.calls, [{ path: "/webhooks/status", method: "GET" }]);

  const failing = makeHost({ "/webhooks/status": () => jsonResponse(503, { error: "the plugin is not ready yet" }) });
  await assert.rejects(mod.loadStatus(failing.host), /the plugin is not ready yet/);
});

test("runTest posts to the test webhook and toasts the outcome", async () => {
  const { mod } = await loadPlugin();
  const ok = makeHost({ "/webhooks/test": () => jsonResponse(200, { ok: true, sent: 2 }) });
  await mod.runTest(ok.host);
  assert.deepEqual(ok.calls, [{ path: "/webhooks/test", method: "POST" }]);
  assert.deepEqual(ok.toasts, [{ kind: "success", msg: "Test message sent to 2 users." }]);

  const refused = makeHost({ "/webhooks/test": () => jsonResponse(200, { ok: false, error: "No allowed user ids are set." }) });
  await mod.runTest(refused.host);
  assert.deepEqual(refused.toasts, [{ kind: "error", msg: "No allowed user ids are set." }]);

  const down = makeHost();
  await mod.runTest(down.host);
  assert.deepEqual(down.toasts, [{ kind: "error", msg: "network down" }]);
});

test("the mounted card fetches the status once", async () => {
  const { mod } = await loadPlugin();
  const { host, calls } = makeHost({ "/webhooks/status": () => jsonResponse(200, { configured: false, missing: [] }) });
  const Card = mod.makeStatusCard(host);
  const tree = Card();
  assert.match(textOf(tree), /Loading status/);
  assert.deepEqual(calls, [{ path: "/webhooks/status", method: "GET" }]);
});
