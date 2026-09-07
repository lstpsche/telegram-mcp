# Telegram folders as local scopes

Import a Telegram folder's current supported chats into a named scope. This is a
one-time selection, not a subscription: later folder edits, new chats, unread
changes and mute changes do not modify the scope. Importing does not grant access,
change Full read settings, or write anything to Telegram.

Stop the daemon for human discovery, list folder IDs, and inspect a selection:

```sh
telegram-mcp service stop
telegram-mcp folders
telegram-mcp folders --id 2
telegram-mcp scope --name work --folder 2
telegram-mcp service start
```

Replace `2` with a returned folder ID and `work` with your chosen local name.
Use `--id SCOPE_ID` to replace a specific existing scope. Saving an existing name
retains its scope ID. `--folder` cannot be combined with `--peer`; use ordinary
scope editing for a manually selected subset. Imports replace membership atomically.

Folder titles are display-only. Only the human-chosen local name and exact peer
IDs are saved. The same scope can then narrow `list_chats`, `list_unread`,
`search_messages` and `catch_up`. Current grants still apply in restricted mode;
a forum parent selection does not select or authorize every topic.

The importer evaluates explicit included/pinned and excluded peers, contacts,
noncontacts, bots, groups and broadcasts. Explicit inclusions take precedence.
Dynamic rules honor archive exclusion, read status and inherited/per-chat mute
settings; unread mentions bypass read/mute exclusions as in Telegram. Shared
folders use their explicit member lists. Main and Archive are dialog lists rather
than custom folders and are not imported by IDs 0 or 1.

Resolution traverses the existing main/archive discovery pages, at most 100 pages
of 100 candidates under the human operation's 30-second deadline. It returns at
most 100 supported peers for preview. Unsupported dynamic candidates are omitted;
unavailable or unsupported explicit members cause an error. A scan failure,
repeated boundary or repeated selected peer releases no snapshot. Results describe
a live traversal, not a transactional Telegram snapshot.

A scope holds at most 20 unique peers. Larger imports fail without changing an
existing scope; choose a smaller folder or use explicit `--peer` selections.
Empty completed membership is valid. Folder names, rules, access hashes and
message content are never saved in scope metadata. No MCP tool can import folders.

See [text access](text-access.md) for scope and permission controls and
[Telegram's folder API](https://core.telegram.org/api/folders) for provider rules.
