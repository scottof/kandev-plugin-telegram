# Changelog

## [0.1.3] - 2026-10-24

### Added

- `notify_user` tool for agents in kanban and office tasks: one plain-text message to the allowed users, or one of
  them, with a link to the task. Repeated idempotency keys never send twice.
- The bot reacts with 👀 to a `/new` command while the triage agent works.
- Optional column alerts ("Moved: <task>" with the old and new column), off by default.
- A Troubleshooting section in the README.

## [0.1.2] - 2026-10-24

### Changed

- `/new` falls back to the workspace's default agent when neither the Triage agent profile nor a utility agent
  default is set and kandev has a single workspace.
- A failed triage says which setting to fix (Settings > Plugins > Telegram or Settings > Utility Agents) instead of
  showing the raw kandev error.

## [0.1.1] - 2026-10-24

### Fixed

- `/status` and `/new` replies no longer vanish when Telegram refuses their link buttons, for example when the kandev
  URL is `localhost`. The reply goes out again as plain text, as alerts already did.

## [0.1.0] - 2026-10-18

First release, started from `kdlbs/kandev-plugin-template`.

### Added

- Telegram bot over long polling, configured from Settings > Plugins: bot token, allowed user ids, kandev URL,
  triage agent profile, start agent, and alert toggles.
- Alerts with a task link when an agent waits for a permission or an answer, and when a task is completed, fails to
  start or (opt-in) moves to review. Bursts are batched into one message. Alerts survive a restart
  or a Telegram outage, and a restart only repeats one if it lands between sending it and recording it. Delivery is
  tracked per allowed user, retries during an outage wait for the 15 second cycle, and Telegram's `retry_after` is
  honoured for up to 5 minutes.
- `/new <text>` files a task through the triage agent, with the replied-to message as context.
- `/status` lists running tasks and agents waiting on you.
- Settings page status card with a test message button, settings warnings and the last alert Telegram refused. The
  test button fails at once while Telegram asks the bot to wait, and a bad allowed user id is named by its position.
