# Experimental user-authorized content MCP — Issue #49

> **Development and staging preview only.** The opt-in implementation consumes
> AChrix OAuth `v0.1.0` (an upstream **Pre-release**) and Bridge `v0.1.0`.
> It does **not** claim production OAuth/OIDC support, dynamic client
> registration, ChatGPT/Claude onboarding, a shipped Gateway connector,
> a public general-purpose CMS HTTP API or completed Issue #49.

## Product ownership and enablement

Rixa uses the site's existing **Identity** login/session and **SiteAccess**
content grants on every remote request. AChrix OAuth issues resource-scoped,
opaque and individually revocable access tokens to explicit pre-enrolled
clients; AChrix Bridge adapts five typed MCP tools to the existing
`EditorialService`. No caller-provided user ID, site, client, gateway
identity or tool argument grants authority. An enabled client must have current
`rixa.content.read` at the site and also `rixa.content.edit` for a write.

Configure only deliberately approved clients on the intended **isolated**
management origin, with real HTTPS and independent public and preview origins.
This minimal example is a fragment of one site's entry:

```json
{
  "management_origin": "https://admin.site.example",
  "preview_origin": "https://preview.site.example",
  "remote": {
    "enabled": true,
    "clients": [
      {
        "id": "approved-test-client",
        "name": "Approved test client",
        "redirect_uris": ["https://client.example/callback"],
        "enabled": true,
        "allow_draft_writes": true
      }
    ]
  }
}
```

This is a **pre-enrolled public-client** code+PKCE profile, not a dynamic
registration workflow. Use actual operator-validated and approved client IDs
and callbacks; never accept client-supplied URLs as authority, or invent hosted
client support. Configuration changes require restarting the Rixa process;
there is no account-linking/connection management UI in this slice. Client
disable and per-user Disconnect UX remain Issue #49 delivery work. An explicit
OAuth `POST /oauth/revoke` terminates a supported client token/grant. Browser
logout deliberately does **not** revoke an existing OAuth grant.

With `remote.enabled=false` (default), no OAuth issuer, MCP endpoint or
OAuth schema is provisioned. Previously enrolled client metadata can remain
in the disabled configuration for later deliberate re-enablement without
creating a listener, worker or authorization grant. No MCP endpoint runs on public/preview origins.
The AChrix Bridge profile uses the official MCP SDK's DNS-rebinding guard.
When the TCP listener itself is loopback (such as `127.0.0.1`) but the
canonical external management Host is not, the SDK rejects MCP POST with
HTTP 403 **before a tool runs**. For this reviewed staging profile, use a
real non-loopback, TLS-protected listener for that canonical management
hostname. Do not disable the SDK guard, inject forged LocalAddr context, or
rewrite the Host header as a workaround. Support for a loopback-bound reverse
proxy topology requires a separate validated upstream Bridge contract and
appropriate admission controls before production deployment.

Only the exact management hostname owns:

- `/oauth/authorize`, `/oauth/token`, `/oauth/revoke` and RFC 8414 metadata at `/.well-known/oauth-authorization-server/oauth`
- MCP Streamable HTTP `POST /mcp` and RFC 9728 metadata at `/.well-known/oauth-protected-resource/mcp`

## Supported MCP operations

| Tool | Effect |
| --- | --- |
| `rixa.content.list` | Bounded private saved-head listing |
| `rixa.content.head` | Read one authorized private draft |
| `rixa.content.create_draft` | Create a page/post draft with supplied stable `operation_id` |
| `rixa.content.save_draft` | CAS-save using a **decimal-string** `expected_revision` |
| `rixa.content.own_operation` | Inspect only the calling user's committed create/save operation receipt |

Write tools require both `allow_draft_writes` on the trusted enrolled client
and the current user's product `rixa.content.edit` permission. A token is not
an editor role or publisher grant. Publishing and Theme modification are NOT
exposed by this slice. A write acknowledgement loss requires checking the
original `operation_id`; an absent receipt while the request may still be
running does not prove rollback, and never authorizes a new-ID retry. Revision
strings preserve 64-bit precision across JavaScript-based clients.

## Installation and retained-data boundary

Before opt-in **development/staging** activation:

1. Verify exact pinned Go modules and that AChrix OAuth is only a pre-release.
   Review callback ownership, TLS certs, current Identity accounts/grants,
   site isolation, PostgreSQL privileges and the intended client.
2. Plan and rehearse a consistent retained-database backup/restore. The new
   dependency graph brings **Audit v0.3.0** (append-only attribution **schema
   v2**) and **Identity v0.4.0**. An older Rixa binary requiring Audit v1
   may reject the forward-only Audit v2 migration. Do not presume automatic
   managed in-place upgrade or rollback. Product issue #29 owns that gate.
3. Run the normal explicit `rixa migrate --config <path>` with the fully
   approved stage configuration and its bound database secrets. All target
   databases receive the current required Audit/Identity schema; only
   remote-enabled site DBs also receive OAuth's own schema. No request or
   service startup migrates the database. Follow with `rixa check` and
   real HTTPS/PostgreSQL acceptance before enabling externally reachable use.
4. Grant actual site users `rixa.content.read` and, when needed,
   `rixa.content.edit` via the existing administrator-authorized SiteAccess
   flow. Never impersonate users with a shared Control administrator identity.

The selected first-profile client uses OAuth authorization code + PKCE S256,
exact pre-registered redirect and intended `resource`/audience. The OAuth
consent is a same-origin form using the logged-in site's current Identity
cookie and server-issued CSRF transaction. It cannot be approved by a headless
caller-selected actor.

## Validation boundaries

The repository's real PostgreSQL / HTTPS tests exercise two independent
sites, current account grants, the OAuth consent and code exchange, authenticated
MCP calls, draft/revision/own-operation behavior, CSRF/Origin controls, client
write narrowing, revocation and non-revoking interactive logout. The exact
commit's GitHub CI is the evidence for any successful execution; a skipped
local PostgreSQL test is not passing evidence.

Actual hosted ChatGPT/Claude and Gateway client onboarding, explicit
per-account connection configuration UX, complete remote Theme operations,
a supported production migration/release and the final cross-client matrix
remain open in [Rixa #49](https://github.com/AChWorks/rixa/issues/49),
[Rixa #74](https://github.com/AChWorks/rixa/issues/74),
[AChrix #125](https://github.com/AChWorks/achrix/issues/125) and
[Gateway #86](https://github.com/AChWorks/mcp-gateway/issues/86).
