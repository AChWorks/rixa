# Authenticated server entry

Use a published immutable bootstrap generation and its independently trusted literal script digest. This source defines `bootstrap-v1`; publication and actual supported-server acceptance are recorded in [installation delivery](https://github.com/AChWorks/rixa/issues/29). A source checkout, branch or tag name alone is not that proof.

The entry requires an authorized root infrastructure operator on **Debian 13 / Linux amd64 / systemd**, trusted base APT/dpkg tools and Debian archive keys, `/usr/bin/curl`, `/usr/bin/python3`, coreutils, current system CA certificates and direct outbound HTTPS to the official downloads. It installs none of those base tools. `/root` must be a root-owned `0700` directory for the documented command. Supply distinct control/site HTTPS hostnames and suitable TLS certificate/key files. The entry does not issue certificates or configure DNS, firewalls, proxies or unrelated listeners.

## Start guided development setup

After verifying that the generation and its host profile are published, run this single command in a trusted administrator terminal:

```sh
sudo /usr/bin/env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin LANG=C.UTF-8 TERM="${TERM:-dumb}" /bin/sh -c 'set -eu; umask 077; test "$(stat -c "%u:%a:%F" /root)" = "0:700:directory"; d=$(mktemp -d /root/rixa-entry.XXXXXXXX); cleanup() { rm -f "$d/install.sh"; rmdir "$d"; }; trap cleanup EXIT; curl --disable --proto "=https" --proto-redir "=https" --tlsv1.2 --fail --silent --show-error --location --max-redirs 5 --connect-timeout 15 --max-time 120 --max-filesize 262144 https://raw.githubusercontent.com/AChWorks/rixa/bootstrap-v1/install.sh -o "$d/install.sh"; printf "%s  %s\n" "7c4b154deef812781a9ffd66ab1ba198eb4288c610eec481a89c441d66ba24cd" "$d/install.sh" | sha256sum -c - >&2; /bin/sh "$d/install.sh" --channel development'
```

The literal SHA256 authenticates `install.sh` before the shell reads it. It belongs to the command, outside the script; no script hashes or trusts itself. `curl --disable` ignores ambient curl configuration, HTTPS is required for redirects, and bytes/time/redirects are bounded. The outer command removes only its newly created temporary script and empty private directory. It never deletes installation state.

This example intentionally selects the **development** channel. Omit `--channel development` to request stable, or replace it with one exact `--release <tag>`. Stable fails when no suitable stable release is available; it never falls back to a prerelease. Other [starter arguments](server-management.md#discover-and-select-the-right-manager), including `inspect --non-interactive`, pass literally to the existing bootstrap command. The entry supplies no separate channel resolver or lifecycle interface. CLI and terminal messages remain English.

The starter opens its normal guided interface, using the actual controlling terminal. On a positively clean dedicated host you can explicitly review PostgreSQL prerequisites, then privately reuse that completed operation for initial native installation. Supplied compatible databases remain an alternative. Configuration, Review and Apply are distinct choices. The [native guide](server-management.md#bootstrap-starter-and-postgresql-prerequisites) owns the exact supported host, input, ownership and recovery rules; this entry does not automatically Apply, Resume, reinstall or reset anything.

## Initial trust and retained tools

Generation `bootstrap-v1` pins the already published starter:

| Pin | Value |
| --- | --- |
| Rixa starter release | `v0.6.0` |
| Starter source | `50c0c3b4d0ac1bd73562d36a5b96b9a1f7a0d58d` |
| Starter archive SHA256 | `544555483b4a7d7c9dea09d25c1042e8bd4670d1a757c524b829ce894519828f` |
| Official GitHub CLI helper | `v2.103.3` |
| Helper archive SHA256 | `4c806978ffce1cec70853302a56d70ac6da4f19bae7adca4e1a8ab00633dd91d` |

These are generation-specific machine trust anchors, also enforced by [install.sh](../install.sh). The starter was independently authenticated before these pins were adopted. The entry checks each entire archive before parsing an exact expected member, checks the starter's source/release/target metadata and actual pinned executable hashes, then executes that trusted starter. It does not implement Sigstore policy or parse selected product candidates; S reuses the normal [publisher verification and authenticated package path](release-verification.md#bootstrap-starter-and-generation-trust).

Pinned public archives and executable files remain under root-private `/opt/rixa-entry/bootstrap-v1`. A preparation-only lock under `/opt/rixa-entry` prevents concurrent initialization; native lifecycle locking stays in the existing manager. Every reuse checks directory/file ownership, modes, regular-file/no-symlink identity, bounds and exact hashes. Existing changed, unknown or incomplete generation state is refused rather than replaced.

Retaining these tools is intentional: a saved native Review can still name the initial helper before Apply freezes its own copy. Menu exit or an initializer failure must not break that consumer. Do not remove this cache while a review/process needs it. There is no cache garbage collector, automatic partial-cache repair or cleanup of older generations. After an interrupted initial download, inspect the private cache and consumers before an explicitly owned recovery decision; do not delete native journals or frozen managers to force re-entry.

After initial trust, the shared starter authenticates a separately selected product artifact and stages protected copies. Existing operations route to their recorded exact frozen managers; a newer entry does not replace them. Ordinary product releases retain their archive/checksum/attestation assets; this tagged source entry adds no fourth asset. Production hosting, aaPanel/web profiles, unattended updates and destructive purge keep their own delivery and authority gates.
