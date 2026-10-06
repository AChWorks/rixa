# Static publication

Rixa publishes public pages as product-owned immutable artifact generations. This boundary is separate from editorial publication intent: intent selects an immutable content revision, while an explicit publication apply operation materializes and activates a coherent public generation.

## Storage and trust boundary

Each enabled site has two disjoint private roots:

- `media_root` is AChrix Media private storage and is never a webroot.
- `public_root` is Rixa-owned publication control storage. It contains generated generations, the active-generation pointer, and private manifests.

Both roots must be canonical, non-symlink directories with mode `0700`. They may not overlap with each other or another site's roots. Public HTTP requests never receive filesystem paths or Media source IDs.

A public generation contains server-rendered HTML, `sitemap.xml`, `robots.txt`, and clean PNG/JPEG representations prepared through AChrix's separately authorized public-image seam. The original Media object is never copied or served directly.

## Publication transaction

Publication apply is serialized per site and uses an optimistic expected-generation precondition. This first executable boundary supports one active publication writer process per configured `public_root`; shared-root multi-writer replicas require a separate coordination contract and are not supported by this slice.

1. Read all selected publication intents and Appearance in a read-only PostgreSQL `REPEATABLE READ` snapshot.
2. Build route ownership from the previous active manifest. Corrections retain the public route, explicit reroutes preserve old routes as permanent redirects, and withdrawals preserve owned routes as HTTP 410 tombstones.
3. Prepare each exact Media revision into private staging. Only the supported clean PNG/JPEG profile is accepted, and every published figure must have a non-empty description.
4. Render semantic HTML, truthful JSON-LD, sitemap, robots policy, and content-addressed images into a new immutable generation.
5. Re-read the editorial snapshot and Media source metadata. Any revision/hash drift aborts without changing the active generation.
6. Persist and fsync the generation, then atomically replace the small `current` pointer. A failure before pointer replacement leaves the previous generation active. If durability after pointer replacement is ambiguous, the mutation returns an unknown-outcome result and the operation ID can be reconciled from the active generation plus its bounded retained parent chain. Callers must reconcile an unknown outcome before advancing through later publication generations rather than blindly replaying the mutation.

At startup Rixa validates the current pointer, private manifest, routes, and generated file inventory before public serving becomes ready. Orphaned staging/uncommitted generations are not served.

During shutdown, ingress closes first; Rixa then cancels new/in-flight publication work and drains the publication apply gate before shutting down the site's AChrix Applications. If publication cannot drain within the configured shutdown budget, shutdown returns an explicit error instead of racing dependency teardown against activation.

## Public read path

Normal public reads use only the in-memory active manifest plus immutable files. They do not call content SQL, Identity, authentication, or Media.

The native Go HTTPS ingress is the initial serving choice because Rixa already owns exact-host TLS routing and can serve the immutable generation without adding another trust/proxy layer. Public transfer admission is bounded, GET/HEAD only, paths are exact and fail closed, and image URLs are content-addressed. The first withdrawal-sensitive profile intentionally uses a short revalidating cache for pages and images rather than a year-long immutable cache. Responses set explicit content types, cache policy, CSP, `nosniff`, referrer policy, and crawler directives.

A maintained lightweight front server can remain an optional deployment optimization if later operational measurements show a material benefit. It is not required for correctness.

## Meaning, SEO, and crawler policy

Primary content is present in HTML without client JavaScript. Page title, canonical URL, language, publication/modification times, visible content, sitemap entries, and JSON-LD are generated from the same selected content/Appearance generation.

Rixa emits only facts it owns. It does not invent an author, publisher, review, rating, or commerce claim. Posts use `BlogPosting`; pages use `WebPage`; applicable pages also emit `BreadcrumbList`. Content `dateModified` advances only when the published content/media identity or canonical route changes; a theme-only Appearance publication does not falsely claim that the article itself was modified.

`public_policy` requires an explicit wildcard crawler rule and supports additional provider-specific user agents. Each rule records an operator purpose such as `search`, `ai-search`, or `ai-training`, plus allow/disallow intent. Page-level indexing/snippet policy is emitted separately.

Robots, noindex, snippet directives, and provider-specific user-agent rules are discovery preferences, not privacy or authentication controls. Rixa makes no guarantee of indexing, citation, training exclusion, recommendation, or ranking.

## Operational configuration

Example:

```json
{
  "public_root": "/var/lib/rixa/site-a/public",
  "public_policy": {
    "indexing": "allow",
    "snippet": "allow",
    "crawlers": [
      {"user_agent": "*", "access": "allow", "purpose": "search"},
      {"user_agent": "ProviderTrainingBot", "access": "disallow", "purpose": "ai-training"}
    ]
  }
}
```

Create `public_root` ahead of startup as the Rixa service account with mode `0700`. Do not place it inside `media_root` or configure `media_root` beneath it.

## Validation evidence

Issue #5 and its implementation PR are the durable evidence ledger for the exact candidate. Required evidence includes the real two-site fixture, JavaScript-independent page reads, clean public image proof, correction/reroute/withdrawal/restart reconciliation, negative cross-site/private/spoofed-route checks, static-read-without-dynamic-service-dependencies proof, current CI, and the exact-candidate independent HIGH_ASSURANCE review.

Serving-path measurement is recorded on the Issue/PR against the candidate used for the measurement rather than encoded as a performance promise or flaky CI threshold.
