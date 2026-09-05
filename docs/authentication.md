# Telegram account authentication

The runtime supports one local Telegram account in an explicitly selected
environment: production, or Test DC 1, 2, or 3. No environment is selected by
default. Production uses the pinned SDK production DC list, starts at DC 2,
and follows Telegram-directed account DC migration.

## Preconditions

- Use a Telegram application API ID and API hash obtained through Telegram's
  development tools.
- For production, independently establish eligibility for the intended API and
  AI use, including the rights and consent of affected people. The configuration
  attestation expresses the operator's assertion; it grants no content authority
  and does not establish a contractual exemption.
- For tests, use a disposable or synthetic Test-DC account. Telegram documents
  that test accounts are shared-risk fixtures and may be wiped.
- A real pre-registered Test-DC account may be required. gotd v0.161.0 records
  that automatic provisioning of random `99966XYYYY` users no longer works
  reliably as of 2026.
- Stop `telegram-mcpd`. Configuration, authentication, and logout deliberately
  require its exclusive account lock.

## Configure

```sh
telegram-mcpctl configure --production --attest-eligible
```

For a disposable Test-DC account, use `telegram-mcpctl configure --test-dc 2`.
The selectors cannot be combined. Production requires the attestation flag
before the command can open a credential prompt. Qualify signed control and
daemon artifacts using the [Keychain procedure](keychain.md) before real login.

After acquiring the account lock and confirming no authorization evidence
exists, the command opens `/dev/tty` itself and prompts, without echo, for the
API ID and API hash. It does not accept credentials through argv, stdin, or
environment variables. SQLite receives the API ID, environment, Test-DC number
(zero for production), and timestamp only. The API ID, API hash, environment,
and Test DC are stored together as one
versioned non-synchronizing native login-Keychain item. Before constructing a
Telegram client, the application checks that the bundle agrees with SQLite.
An interrupted update cannot mix fields from different configurations; if the
stores disagree, stop the daemon and rerun configuration.

Changing configuration is refused while either an authorization epoch or a
Keychain session in either environment exists. Test and production use distinct
session items; the runtime never loads the other environment's session. Log out first so a session cannot be silently deleted
or rebound to different application credentials or a different DC.

## Phone and 2FA

```sh
telegram-mcpctl auth phone
```

The phone number, login code, and optional 2FA password are read without echo
from `/dev/tty`. The 2FA password is moved into locked memory and wiped after
gotd computes its SRP answer. New-account sign-up and Terms-of-Service
acceptance are intentionally unsupported.

The password prompt appears only when Telegram requires Two-Step Verification.
It asks for the existing account password, not an app passcode or a password to
create. An empty value cannot skip this requirement and produces a specific
diagnostic. Authentication rate limits have a separate fixed diagnostic; other
unclassified failures disclose no raw Telegram error or credential details.

## QR

```sh
telegram-mcpctl auth qr
```

The QR token is rendered as terminal blocks directly on `/dev/tty`; its raw URI
is not printed. Expired tokens are refreshed by gotd. If Telegram requests 2FA,
the same protected password path is used, with at most three attempts.

An already-authorized session does not count as proof that a newly requested
method works. To record both method checks, authenticate with one method, log
out, and authenticate with the other. The checks remain metadata after logout;
the active authorization epoch does not. Entering application credentials again
clears both checks so evidence from an older API/environment/DC configuration cannot carry
forward.

## Run and inspect

```sh
telegram-mcpctl status
telegram-mcpd
```

Status reports only socket liveness, selected environment and configuration metadata, whether an
authorization epoch is recorded, and which environment-bound manual method checks
passed. `production_login=supported` reports capability, not account readiness. It does
not claim the daemon is Telegram-ready merely because a socket exists.

The daemon acquires the account lock before opening SQLite, reading Keychain,
or constructing a gotd client. It requires a recorded authorization epoch before
opening the text runtime. A surviving Keychain session with missing metadata
does not manufacture new authority: stop the daemon and run `auth phone` to
reconcile the session, then restart. An already-authorized session can be reused
without recording a new method check. An unauthorized session, including authorization
revoked after startup, invalidates the stale epoch and leaves the daemon in
`reauth_required`. SIGINT or SIGTERM cancels gotd and the socket loop, removes
the exact socket node, closes SQLite, and releases the lock.

## Logout

```sh
telegram-mcpctl logout
```

When Telegram considers the session authorized, remote `auth.logOut` must
succeed before local deletion. The returned future-auth token is wiped and
discarded. Only the selected environment's local gotd session is then deleted from Keychain and the current
authorization epoch is removed atomically from metadata. The credential bundle remains
for an explicit future reauthentication.

Primary protocol references:

- <https://core.telegram.org/api/auth>
- <https://core.telegram.org/api/qr-login>
