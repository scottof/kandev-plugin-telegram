// UI bundle for kandev-plugin-telegram: a hand-written, no-build ES module.
// It uses the host's React (host.React / host.jsx) and host.ui components;
// never bundle React, recharts or Radix here.
//
// It adds one status card to this plugin's settings page. The card talks to
// the backend through the plugin's authenticated `status` and `test` webhooks.

const SETTING_LABELS = {
  bot_token: "Bot token",
  kandev_url: "kandev URL",
};

async function readJSON(response) {
  try {
    return await response.json();
  } catch {
    return {};
  }
}

// loadStatus returns the status webhook body, or throws with its error.
export async function loadStatus(host) {
  const response = await host.api.fetch("/webhooks/status", { method: "GET" });
  const body = await readJSON(response);
  if (!response.ok) {
    throw new Error(body.error || `HTTP ${response.status}`);
  }
  return body;
}

// runTest asks the backend to send a test message and toasts the outcome.
export async function runTest(host) {
  try {
    const response = await host.api.fetch("/webhooks/test", { method: "POST" });
    const body = await readJSON(response);
    if (response.ok && body.ok) {
      const users = body.sent === 1 ? "1 user" : `${body.sent} users`;
      host.toast.success(`Test message sent to ${users}.`);
    } else {
      host.toast.error(body.error || `HTTP ${response.status}`);
    }
  } catch (err) {
    host.toast.error(err instanceof Error ? err.message : String(err));
  }
}

function formatTime(iso) {
  if (!iso) return "Never";
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : date.toLocaleString();
}

function statusBadge(h, ui, data) {
  if (!data.configured) return h(ui.Badge, { variant: "secondary" }, "Not configured");
  if (data.running && data.connected) return h(ui.Badge, null, "Connected");
  return h(ui.Badge, { variant: "destructive" }, "Not connected");
}

function detailRows(h, data) {
  const rows = [];
  const line = (text) => h("p", { className: "text-sm text-muted-foreground" }, text);
  if (data.username) rows.push(line(`Bot: @${data.username}`));
  if (typeof data.allowed_users === "number") rows.push(line(`Allowed users: ${data.allowed_users}`));
  if (data.configured) rows.push(line(`Last update: ${formatTime(data.last_update_at)}`));
  if (data.missing && data.missing.length > 0) {
    const names = data.missing.map((key) => SETTING_LABELS[key] || key).join(", ");
    rows.push(line(`Missing settings: ${names}`));
  }
  if (data.config_error) rows.push(line(`Settings error: ${data.config_error}`));
  if (data.last_error) rows.push(line(`Last error: ${data.last_error}`));
  return rows;
}

function cardBody(h, ui, state) {
  if (state.phase === "loading") {
    return h("div", { className: "flex items-center gap-2 text-sm text-muted-foreground" }, h(ui.Spinner, null), "Loading status");
  }
  if (state.phase === "error") {
    return h(
      ui.Alert,
      { variant: "destructive" },
      h(ui.AlertTitle, null, "Could not load the bot status"),
      h(ui.AlertDescription, null, state.message),
    );
  }
  return h("div", { className: "space-y-2" }, statusBadge(h, ui, state.data), ...detailRows(h, state.data));
}

// renderStatusCard is the pure view: state is {phase: "loading" | "error" |
// "ready", message?, data?}.
export function renderStatusCard(host, state, { onRefresh, onTest, testing }) {
  const { jsx: h, ui } = host;
  const canTest = state.phase === "ready" && state.data.running === true && !testing;
  return h(
    ui.Card,
    null,
    h(
      ui.CardHeader,
      null,
      h(ui.CardTitle, null, "Telegram bot"),
      h(ui.CardDescription, null, "Connection status, and a test message to the allowed users."),
    ),
    h(ui.CardContent, null, cardBody(h, ui, state)),
    h(
      ui.CardFooter,
      { className: "flex gap-2" },
      h(ui.Button, { type: "button", onClick: onTest, disabled: !canTest }, testing ? "Sending..." : "Send test message"),
      h(ui.Button, { type: "button", variant: "outline", onClick: onRefresh, disabled: state.phase === "loading" }, "Refresh"),
    ),
  );
}

export function makeStatusCard(host) {
  const { React } = host;
  return function TelegramStatusCard() {
    const [state, setState] = React.useState({ phase: "loading" });
    const [testing, setTesting] = React.useState(false);
    const mounted = React.useRef(true);

    const refresh = React.useCallback(() => {
      setState({ phase: "loading" });
      loadStatus(host).then(
        (data) => mounted.current && setState({ phase: "ready", data }),
        (err) => mounted.current && setState({ phase: "error", message: err instanceof Error ? err.message : String(err) }),
      );
    }, []);

    React.useEffect(() => {
      mounted.current = true;
      refresh();
      return () => {
        mounted.current = false;
      };
    }, [refresh]);

    const onTest = async () => {
      setTesting(true);
      await runTest(host);
      if (mounted.current) {
        setTesting(false);
        refresh();
      }
    };

    return renderStatusCard(host, state, { onRefresh: refresh, onTest, testing });
  };
}

window.registerKandevPlugin("kandev-plugin-telegram", {
  initialize(registry, host) {
    registry.registerComponent("plugin-settings", makeStatusCard(host));
  },
  destroy() {},
});
