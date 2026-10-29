# Durable initial administrator bootstrap

The product-level `ApplyBootstrapOperation` and `InspectBootstrapOperation` APIs prepare one initial administrator for a quiesced control or enabled-site scope. Native installation calls these APIs under its runtime user; they are not an arbitrary account-adoption or password-reset interface. Ordinary `BootstrapAdmin` retains its existing deterministic conflict semantics.

Supply an operation ID, exact immutable private draft bytes, scope, exact login and a journal path in an existing canonical `0700` directory owned by the executing user. Initial creation also needs the intended password through protected transient input. Migrations, separate reachable database targets and private storage must already exist. The operation does not migrate schema, install prerequisites, freeze final configuration, grant host privileges or start a service.

The private `0600` journal binds the executing image, exact draft hash, private parent identity/path, operation, login, scope and PostgreSQL system/database identity. It contains no password, DSN, configuration bytes or session token. All setup/bootstrap kinds and IDs within that parent serialize through the retained management lock. The native manager owns the installation-wide executor fence across its separate root/runtime state directories.

## Creation and re-entry

Before creation, exact-login lookup must establish absence. An already-existing account is a conflict even when the supplied password would authenticate it. The absence baseline and attempted effect are synchronized before `CreateAccount`; returned account identity is synchronized before a verified result is reported.

A later process never blindly repeats `CreateAccount`. An attempted but absent account, unavailable database or unknown outcome requires inspection. A found account is insufficient proof by login alone: reconciliation requires the intended password to authenticate that exact enabled account, matching the returned session principal, followed by successful logout of the temporary session. The public released Identity service owns these checks; Rixa does not read or rewrite its private credential tables.

The bootstrap-only authentication runtime uses a one-minute session lifetime. Authentication is recorded as attempted before issuing a session. Wrong credentials may be corrected while retaining the same operation. An uncertain Login/Logout acknowledgement or process loss during that proof is retained and never automatically repeated; an unreachable temporary token is not exposed and expires after its bounded lifetime. This slice supplies no automatic reset for that uncertainty: retain the original operation and inspect its executor/account before an explicitly supported recovery is selected. Do not edit/delete journals or invent a new operation to bypass the boundary.

After verification, Apply or Inspect may omit the password. They still recheck the exact physical target and retained account identity/status; changed or missing verified material is not permission to create another account. Inspection issues no session and creates no account or journal. A successful state read with an error or unavailable observation does not establish current readiness.

## Results and limits

The shared result contains operation, scope, state, safe code/guidance and the verified principal where established. It excludes the draft, login, private paths and credentials. Preserve nonzero errors: for example, a verified receipt followed by unavailable shutdown still needs inspection; do not infer success solely from a populated principal or filename.

Keep initial creation quiesced and exclusively managed. The intended-password proof prevents adopting an independently created account whose credentials differ; it is not a defense against a malicious database owner or another party already holding the intended credential. Existing service/creator activity must be fenced by the host manager. Binary rollback does not undo accounts, audit events or sessions.

Real PostgreSQL tests cover cross-process loss after account commit, intended-password proof and logout, fresh deterministic conflicts, physical target drift, cross-process lock exclusion and uncertainty during session issuance. Policy/unit tests cover malformed/private state, changed bindings, missing accounts, failed/mismatched credentials and unknown acknowledgements. These are bootstrap proofs, not a claim of complete installation, production support or privileged service delivery.
