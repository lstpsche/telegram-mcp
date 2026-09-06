# Read-only polls

History and message context expose an optional `poll` containing the question,
ordered text options, closed/public-voter/multiple-choice/quiz flags, and available
aggregate voter counts. Each option remains distinct even if display texts match.
Missing `voters` or `total_voters` means unknown; explicit zero means zero.
`minimal` reports Telegram's reduced result form, not freshness or completeness.
Counts are snapshots supplied with the message and can change. Multiple-choice
option counts must not be summed to infer the number of people who voted.

Search and catch-up expose `has_poll: true` and a bounded question snippet.
Use `get_message_context` for options and counts. Telegram controls search
matching; this feature does not index questions or options locally. Existing
pagination, grants, Full read access and history/context read acknowledgments
apply. Search and catch-up do not acknowledge reads. Poll grouping and raw poll
IDs are not navigation or authority; use the containing message ID.

The tool does not vote, enumerate voters, expose personal choices, reveal quiz
solutions, or request extra poll results. Recent-voter identities, opaque voting
options, answer authors, entities and hashes remain absent from public output.
Polls with attached media or media answer variants remain unsupported, as do
protected, ephemeral and other excluded messages. There are no new permissions,
MCP tools, dependencies or persisted poll data.

Text fields are valid UTF-8 and at most 4096 bytes each; polls have 2–100 options.
Malformed option mappings and invalid supplied counts fail without content in
errors. The existing complete-response budget applies before any receipt; an
oversized poll response is an error, never silent truncation.

The adapter follows the pinned generated Telegram layer and optional-field
presence, rather than assuming absent fields contain zero. See Telegram's
[poll](https://core.telegram.org/constructor/poll),
[poll results](https://core.telegram.org/constructor/pollResults) and
[answer results](https://core.telegram.org/constructor/pollAnswerVoters) contracts.
