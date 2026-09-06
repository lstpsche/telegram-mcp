# Metadata backup and audit maintenance

These are human control commands, never MCP tools. Stop the daemon first with
`telegram-mcp service stop`. They take the account and policy locks and do
not connect to Telegram, read local credentials or sessions, or mark messages read. Restart
with `service start` after maintenance. Backup inspection needs only the file.

Create a private backup directory, then export to a new absolute path. On Unix:

```sh
mkdir -m 700 "$HOME/telegram-mcp-backups"
telegram-mcp backup --file "$HOME/telegram-mcp-backups/metadata.json"
telegram-mcp backup-inspect --file "$HOME/telegram-mcp-backups/metadata.json"
```

The versioned JSON contains the environment/Test DC selector, named scope names
and typed member IDs, and audit retention settings. It excludes API credentials
and API IDs, sessions, authorization epochs, grants, Full read access, scope IDs,
policy revisions, access hashes, update checkpoints, audit history and Telegram
content. It is a settings backup, not a database or account-session backup.
Keep it private: membership IDs can still reveal relationships. It is not encrypted.

On Unix, files must be owned regular `0600` files in owned `0700` directories.
On Windows, the directory and file must have owner-restricted ACLs, without
access for other ordinary users. Symlinks and Windows reparse points,
hard-linked input files, overwrites, invalid/ambiguous JSON and inputs over 64 KiB
are refused. Creation publishes a complete file atomically without overwriting an
existing path. Do not copy a live SQLite database as an alternative: restoring
old authority or checkpoints is unsafe.

Inspect the backup, preview it against the current installation, then explicitly
replace scopes and reset access:

```sh
telegram-mcp restore --dry-run --file "$HOME/telegram-mcp-backups/metadata.json"
telegram-mcp restore --file "$HOME/telegram-mcp-backups/metadata.json" --replace-scopes --reset-access
telegram-mcp scopes
```

The dry run takes the same maintenance locks and checks the same configured,
authenticated account and production/Test DC match as restore. It prints current
and proposed scope selections and retention settings as JSON, without current
scope IDs. Its consequences summarize the eventual access reset, fresh scope IDs,
reference invalidation and audit-history preservation. It does not change scopes,
retention, grants, Full read access, policy revisions or audit history. The daemon
must still be stopped. Metadata is opened read-only: a missing database or an
outdated or incompatible migration history is refused without initialization or
migration. Maintenance lock files and SQLite coordination files may still be used.

Restore requires an already configured, authenticated account in the same
production/Test DC environment. It is an explicit import into that current
account, not proof that the backup belongs to the same Telegram user. Review the
member IDs before importing. Every scope receives a new ID; all existing scopes,
restricted grants and Full read access are replaced/reset atomically. Existing
cursors and image handles are invalidated. Re-select scopes and explicitly grant
access or enable Full read access when ready.

The current session, account configuration, synchronization state and audit
history are preserved. Backup retention settings are restored, but no historical
audit records are deleted during restore; the next audited request or confirmed
prune applies them. A transaction failure leaves the prior state intact. File or
close errors are reported even when an operation may already have committed;
inspect the current state before repeating a failed command.

After database loss, configure/authenticate through the normal human workflow
before restoring settings. A surviving local session may need reconciliation;
absence of an epoch is not proof that the session is gone. The backup cannot
repair corrupt storage, a Telegram synchronization gap, lost credentials. It never manufactures account authority.

Inspect the content-free audit inventory and prospective prune count:

```sh
telegram-mcp audit
telegram-mcp audit retention --days 30 --max-records 10000 --apply
telegram-mcp audit prune --confirm
telegram-mcp audit purge --all --confirm
```

The default retention is 30 days and 10,000 records. Supported limits are 1–3650
days and 1–1,000,000 records; there is no unlimited mode. Among records within
the age window, retain the newest insertions up to the count limit. Time is UTC,
and the age cutoff is rounded down to a whole second. A record at the cutoff is
retained. Counts include successful and failed operations.

Retention runs atomically with every required audit insertion. If pruning fails,
that audit transaction fails and the reader's existing failure boundary withholds
content. There is no background timer: on an idle installation, age-expired rows
remain until a subsequent audited operation or explicit prune. Migration creates
the settings and index without deleting existing rows. `--apply` changes settings
and immediately prunes in one transaction. `purge --all --confirm` removes all audit
records, preserves retention settings, and does not disable future auditing.
Audit maintenance works even without an authorization epoch.

Pruning is logical deletion. SQLite reuses freed pages; the database need not
shrink, and WAL files, filesystem snapshots and external copies may retain old
bytes. These commands do not claim forensic erasure and do not export audit rows
or automatically vacuum the live database.
