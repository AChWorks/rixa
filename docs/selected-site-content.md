# Saved content from the control interface

Sign in at the configured control administration origin, open **Sites**, and choose **Manage content** for a declared site you may manage. The workspace shows that site's stable ID and public origin. Switching sites is explicit navigation; a control login remains separate from every site's own login.

Create a post or page, open its latest saved version, edit and save. Saving creates a source revision; it does not publish, correct or withdraw public output. The selected-site workspace provides both saved content and a separately confirmed public publication workflow. The site's own administration retains its existing publication, Media and appearance flows.

The content list and revision history have bounded pages. Follow **More content** or **Older revisions** to continue; the shown count describes the current page. Concurrent edits may move content between pages, so refresh the first page when you need a current starting view. Revision previews identify an exact saved version.

Private preview requires administration authority. Root-relative content links point to the selected site's declared public origin. Private originals remain private attachments/placeholders; preview does not authorize their publication.

## Publishing or withdrawing from one selected site

After saving, inspect the exact content head, intended public revision, the active public generation and the proposed public route. **Set publication intent** selects a saved revision without publishing it. **Clear publication intent** prepares withdrawal without immediately changing the public site. Preview the intended saved revision before applying. A cleared intent has revision zero; save and intent editing do not publish automatically.

**Apply coherent publication** operates on the current selected site's *entire public generation*, not just the open article. Other marked content, withdrawals, appearance and site navigation can also change. The page explicitly identifies the SiteID and public origin and asks you to review the proposed route and acknowledge that site-wide consequence before submitting. Changing the proposed route, saving or changing publication intent resets the confirmation. A selected-site publish requires a current saved-head revision, intent-version, intended revision and expected active generation; stale values are rejected rather than silently overwriting a newer change. Content remains in that site's own database and public root; nothing fans out to another site.

A successful response shows the new public generation and an **Open public site** link. On timeout or a lost response, **do not submit a different request under a new ID**. Retain the exact operation ID and payload, use **Inspect mutation outcome** on that same selected site, and retry only the original exact request if inspection does not establish a result. A change to the route or reviewed versions under an already-used ID returns a conflict. Reload the editor before planning another operation; this replaces stale UI values with current authoritative state. Withdrawal may return **410 Gone** for previously published routes, not a silent deletion.

## Saving and recovery

Keep the editor open when a save result is uncertain. The recovery section retains the exact submitted change and its operation ID. **Inspect** checks that same operation. An unavailable or missing result does not prove that nothing was saved. **Retry submitted save** deliberately resends the retained request with the same ID; it does not substitute later edits or create a new operation. Further writes remain blocked while that request is unresolved.

If someone saved a newer revision, your text stays in the editor. Open the latest saved version in another tab and reconcile deliberately; the interface does not overwrite it automatically. If your session expires, sign in through the offered separate tab, return and retry deliberately. Your unsaved text remains in the current tab, and retry obtains current authentication/CSRF state.

Navigation, site/language changes, logout and closing/reloading warn about unsaved or unresolved work. Browser input is not a durable backup: abandoning the tab may lose unsaved text. Drafts and credentials are not stored in WebStorage or placed in URLs.

## Language and scope

English/LTR is the default. Persian/RTL and valid installed interface languages use the same [localization](localization.md) mechanism. Interface language does not change the site's saved content language, text or publication. Mixed-script titles, IDs and origins retain their own direction.

Every selected-site operation uses normal fresh control and site-domain authorization. A displayed site ID, URL or cursor is a requested target, never a grant. Unknown, disabled or unauthorized sites and foreign content/revision/operation IDs fail through the same boundary. This workspace does not grant host installation/update authority or introduce cross-site bulk actions.
