# Changelog

## [0.1.6] - 2026-10-24

### Changed

- Built against the kandev v0.98.0 SDK. The plugin packages it uses are unchanged from v0.97.0, so behavior is the
  same, and kandev 0.95.0 or newer is still supported.

## [0.1.5] - 2026-10-24

### Added

- Alerts about one task have Mute and Done buttons. Mute stops alerts for that task until `/unmute`; Done moves it to
  the workflow's finishing column, or its last column when none is named like one.
- `/task <words>` shows a task's workflow, column and state with the same buttons.
- `/mute <words>` and `/unmute` (a list, a task, or `all`).
- Optional quiet hours, off by default: alerts are held and sent as one batch when the window ends. The time zone
  defaults to Europe/Amsterdam.
- Optional workflow filter: alert only for the workflows you name.
- The bot registers its commands with Telegram, so they show in the chat's command menu.
- `/status` notes muted tasks and active quiet hours.

### Changed

- When Telegram refuses a link button, an alert keeps its Mute and Done buttons before falling back to plain text.

## [0.1.4] - 2026-10-24

### Fixed

- Column alerts now cover the moves kandev's workflows make, such as to Review after an agent turn. kandev reports
  those only with `task.updated`, so the plugin tracks each task's last seen column.
- A repeated identical column move alerts again; kandev can send board moves without a transition id.
- `/new` no longer files a task into a column for finished work, such as Done, where kandev would complete it at once.
- The `/new` reply names the column the task went to instead of "default column".
- A command cut short by a plugin restart now gets a reply after the restart instead of silence.

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
