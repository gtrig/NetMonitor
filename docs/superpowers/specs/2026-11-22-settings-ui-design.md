# Settings UI — design

## Intent
Manage networks, routers, SNMP targets, notification channels and rules from the browser instead of hand-editing `config/config.yaml` and restarting. (User request, 2026-11-22.)

## Decisions (user)
- **`config.yaml` stays the single source of truth.** The UI writes changes back to it and the app hot-reloads them.
- **Edits require `auth:` to be configured.** Without auth, the settings page is read-only and shows a banner explaining how to enable auth.

## Approaches considered
- Database as source of truth, with config.yaml only seeding it: live changes and no file permission issues, but two places to look. **Rejected by the user.**
- **Write back to config.yaml + hot reload (chosen):** one place to look, and hand edits keep working. Costs: the config mount becomes writable, YAML comments are lost on the first UI save, and secrets typed into the UI land in the file.

## Design
- **Raw vs effective config.** The app keeps the *raw* config (with `${VAR}` references unexpanded) for writing back, and the *effective* (expanded, defaulted, validated) config for running. Saving validates the effective version first, so an invalid edit is rejected with every error listed and nothing is written.
- **Write:** marshal the raw config to YAML with a short header comment, write it to a temp file in the same directory, then rename it (atomic). Defaults are not written out, so the file stays minimal.
- **Editable in the UI:** `networks`, `routers`, `snmp_targets`, `channels`, `rules`.
  - Not editable: `listen`, `data_dir`, `oui_path`, `auth`. These still need a file edit and restart, so you can't lock yourself out from the UI.
- **Secrets** (router password, SNMP community/passphrases, bot token, webhook URL, SMTP password):
  - The API never returns their values. Each one shows as unset, `${NAME}` (a reference to `.env`), or "set".
  - Leaving a secret field empty keeps the current raw value. Typing a value stores it as written. The UI suggests `${NAME}` to keep the secret in `.env`.
  - Items are matched by their original name, so renaming an item keeps its secrets.
- **Hot reload.** After a successful save, or on **Reload from file** / SIGHUP:
  1. Stop the scheduler and wait for in-flight collections.
  2. Build new sources, scheduler and dispatcher.
  3. Swap them in.
  4. The web handlers always read the current runtime. Devices, events and history are untouched.
  - If a hand-edited file is invalid on reload, the old runtime keeps running and the error is shown.
- **API:** `GET /api/settings` returns the raw editable sections with secrets masked. `PUT /api/settings` replaces all editable sections; it returns 403 without auth and 400 with error messages if invalid. `POST /api/settings/reload` re-reads the file. Cross-origin protection applies as for the other mutating endpoints.
- **UI:** a `/settings` page in the nav with one section per kind.
  - Each item is a card you can edit inline, plus add and delete buttons.
  - Source rows within a network use type/ref pickers.
  - Rules use checkboxes for events, networks and channels.
  - One **Save** applies everything; errors appear at the top.
- **Deployment.** The config mount becomes read-write, and the container runs as `${NETMON_UID:-1000}:${NETMON_GID:-1000}` (from `.env`) so it can write the host's `config/`. The image's `/data` becomes writable by any UID. The existing test volume gets a one-time ownership fix.

## Non-goals
- Keeping comments in config.yaml.
- Editing listen/auth from the UI.
- Multiple users.
- Undo or version history (git or a backup of the file is the user's tool for that).
