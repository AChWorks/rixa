# Installation foundation

Rixa installation is capability-driven. A panel name, root shell, writable web root, package manager, `systemd` binary or PostgreSQL client is evidence about the environment; none of those facts alone proves that the corresponding Rixa installation requirement is satisfied.

The reusable installer core is deliberately split from the distribution-specific mutating adapters. This keeps one source of truth for prerequisites and planning while allowing the VPS/server and control-panel paths to use only mechanisms they can actually execute and verify.

## Preflight

A Rixa binary that contains the installation foundation can inspect the host without changing it:

```bash
rixa install-preflight
rixa install-preflight --json
```

Each capability is either a required installation outcome or non-blocking context and has one of four states:

- `available` — the required outcome has actually been verified, or the contextual capability exists;
- `provisionable` — the selected installer adapter has both the necessary authority and a supported implementation that can make and verify the change;
- `operator-required` — the host or provider must supply/enable the capability or a different supported installer adapter must be used;
- `unsupported` — the selected environment cannot satisfy a mandatory requirement through the current supported product boundary.

The generated plan contains only required capabilities. Context such as whether the process has root privilege or which package manager exists never blocks an otherwise fully provider-managed installation by itself. Likewise, the plan never turns the mere presence of `psql` into a verified database or the presence of `systemd` into a verified persistent Rixa service.

The base Rixa binary performs discovery and planning only. It does not claim package/database/service/certificate provisioning that is not actually implemented. The server installer and panel adapters opt into individual provisioning actions only when they own a tested implementation for that action.

## Direct TLS hosting profile

The first production-ingress building block is `hosting_profile: "direct_tls"`. Rixa itself terminates TLS. This preserves the existing AChrix browser security boundary: the application sees a real TLS request, the exact browser `Host`/`Origin`, and the socket peer address used by authentication admission controls.

The direct-TLS profile requires:

- an explicit non-loopback IP listener;
- an HTTPS origin for the control surface and every enabled site;
- the configured origins to use the effective listener port (`443` origins are canonical and omit `:443`);
- a readable certificate/key pair with a private key that is not group/world accessible;
- a currently valid certificate whose SANs cover the control hostname and every enabled site hostname;
- the normal distinct-hostname, database-secret, private-root and administrator requirements already enforced by Rixa.

A typical configuration starts from [`config/rixa.direct-tls.example.json`](../config/rixa.direct-tls.example.json). The existing development profile remains loopback-only; omitting `hosting_profile` continues to select `development` for backward compatibility.

## Front proxies and control panels

Do not place a generic HTTP reverse proxy in front of Rixa and trust `Forwarded` or `X-Forwarded-*` headers by convention. The consumed AChrix Identity browser boundary intentionally does not trust those headers. A panel-managed deployment that already owns TCP/443 therefore needs a separately reviewed trusted-proxy integration that preserves exact HTTPS/origin semantics and a trustworthy client-address boundary.

The panel installer outcome owns that integration and must preserve an existing panel-managed listener rather than overwrite it. Until a particular adapter is proven, preflight reports the exact missing ingress/service/database/storage/TLS requirement as `operator-required`.

## Distribution ownership

- [#28](https://github.com/AChWorks/rixa/issues/28) owns this shared preflight/planning and hosting-profile contract.
- [#29](https://github.com/AChWorks/rixa/issues/29) owns the one-line privileged VPS/server installer and its real provisioning adapters.
- [#30](https://github.com/AChWorks/rixa/issues/30) owns aaPanel-first control-panel/constrained-host installation and later evidence-based Plesk, DirectAdmin and cPanel paths.

Installation and update remain separate. This foundation does not imply an updater, destructive recovery mechanism or unrestricted host-control API.
