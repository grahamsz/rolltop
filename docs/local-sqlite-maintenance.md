# Local SQLite Maintenance

This MailMirror deployment is currently the only running instance of this codebase.
One-time local SQLite changes are acceptable when they simplify plugin-table
migrations or data cleanup.

Before making a one-time local database change:

1. Stop the app.
2. Copy the SQLite database to a timestamped backup.
3. Prefer a checked-in migration when the change should be repeatable.
4. Document any manual-only cleanup in this file or the related change notes.

Disabling a plugin should not drop its data. Data removal should remain an
explicit admin maintenance action.
