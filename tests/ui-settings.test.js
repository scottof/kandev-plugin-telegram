import assert from "node:assert/strict";
import test from "node:test";

let importCount = 0;

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
  await import(new URL(`../ui/bundle.js?settings-test=${importCount++}`, import.meta.url));
  return { pluginId, definition };
}

test("registers under the manifest id", async () => {
  const { pluginId, definition } = await loadPlugin();
  assert.equal(pluginId, "kandev-plugin-telegram");
  assert.equal(typeof definition.initialize, "function");
});
