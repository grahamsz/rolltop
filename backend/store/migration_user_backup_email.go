// File overview: Mirrored backup-email column for per-user databases.

package store

func userBackupEmailMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion011,
		Label:   "user schema 011 backup email mirror",
		After: []migrationStep{
			{Label: "add user backup email mirror", Run: ensureUserBackupEmailColumn},
		},
	}
}
