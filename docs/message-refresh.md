# Message refresh and edit awareness

Full message reads now include an `observation` token. To check previously read
messages, pass 1–20 tokens to `get_message_context`:

```json
{"refresh":["<observation from an earlier full message>"]}
```

`refresh` cannot be combined with other arguments. Each token identifies one
exact message; refresh fetches only those targets, with no neighbors. Results
include current full bodies, fresh tokens, and `refresh_state` (`changed` or
`unchanged`). The existing `contexts` array associates requested targets with
returned IDs. All targets must succeed. Unavailable or denied messages fail the
batch; the tool does not label them deleted.

Comparison covers delivered text, formatting, edit time, media identity and
other message metadata. It ignores temporary media handles and context-only
navigation such as neighbor/reply-chain coverage. A replaced attachment can be
reported changed even when its filename, MIME type and size are identical.
`changed` describes the observed representation; it is not a historical edit
diff. Changes and reversions between reads can go undetected.

Full messages and search hits may include `edited_at` when Telegram supplies a
valid edit timestamp. Absence means no edit timestamp was supplied, not proof
that a message was never changed. Dates remain UTC; the original sending `date`
is unchanged. Sender/date search still filters by sending time.

Tokens expire within 24 hours, capped by the current grant, and bind the account
epoch and policy revision. Grants, scopes and access-mode changes invalidate
them. Tokens contain a keyed content digest and exact reference, never message
text, filenames or historical bodies. The server stores no observation history.
An expired token returns `resource_expired`; invalid or changed-authority tokens
return `invalid_reference`. Read the message normally to obtain a fresh token.

Refresh uses the existing shared policy lease, 20-second deadline, complete
response budget and history-acknowledgment flow, including for unchanged
messages. It reauthorizes current content and the affected read prefix; a token
is never permission. Failure before acknowledgment releases no body. A failure
after a possible receipt releases no body and reports `read_effect_uncertain`.
Telegram reads are live observations, not an atomic snapshot or deletion feed.
