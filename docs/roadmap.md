# Rixa Outcome Roadmap

This is the stable outcome order, not live status. [GitHub Issues](https://github.com/AChWorks/rixa/issues) own current task state/contracts, dependencies, blockers and review; [First usable Rixa](https://github.com/AChWorks/rixa/milestone/1) groups product acceptance.

| Order | Outcome / owner | Dependency |
| --- | --- | --- |
| 1 | [#2: bounded consumer contracts](https://github.com/AChWorks/rixa/issues/2): trusted two-site context, initial fixed permissions, real composition and public Media/publication seam | Bootstrap #1; inspect current released public APIs. Produces the real consumer input for AChrix #4. |
| 2 | [#3: executable composition/isolation](https://github.com/AChWorks/rixa/issues/3): pinned dependency, real PostgreSQL/private storage, single/two-site administration and relevant CI | #2 plus the required integrated/released AChrix #4 slice; establish safe implementation/proof order before READY. |
| 3 | [#4: editorial/appearance management](https://github.com/AChWorks/rixa/issues/4): posts/pages, visual editor, private draft/preview and site-owned theme configuration | #2/#3; real fields inform Settings only where reusable. |
| 4 | [#5: coherent public publication](https://github.com/AChWorks/rixa/issues/5): static HTML, authorized images, basic SEO and truthful AI-readable meaning | Real content/composition and resolved public Media seam. |
| 5 | [#6: measured operation/independence](https://github.com/AChWorks/rixa/issues/6): aggregate resources and coherent isolated site transfer/restore | Real composition/publication; narrow Foundation tuning gaps are coordinated, not duplicated. |

[#1](https://github.com/AChWorks/rixa/issues/1) owns the documentation/bootstrap evidence. [#7](https://github.com/AChWorks/rixa/issues/7) owns native main-protection verification; it is separate governance work, not an excuse to invent CI checks or change visibility.

## Foundation coordination

| AChrix owner | Relationship |
| --- | --- |
| [#4 Multi-Site](https://github.com/AChWorks/achrix/issues/4) | Accepted optional reusable ownership in existing Foundation repository/Go module/shared release. Rixa #2 supplies its bounded real consumer contract, #3 supplies product proof; content/themes/publication stay here. |
| [#76 resource configuration](https://github.com/AChWorks/achrix/issues/76) | Operator caps/defaults/admission require measured product bounds and explicit public configurability where needed. The linked AChrix Issue owns reusable implementation; Rixa #6 owns aggregate behavior. |
| [#77 public image preparation/export](https://github.com/AChWorks/achrix/issues/77) | Rixa #2 established that private Read cannot supply public image publication. Resolve the explicit processing/privacy decision, then implement only the narrow Media seam; Rixa #5 owns artifact activation/withdrawal. |
| [#50 Settings](https://github.com/AChWorks/achrix/issues/50) | Waits for concrete reusable application-level fields from the real editorial/configuration workflow; domain fields remain here. |
| [#48 Search](https://github.com/AChWorks/achrix/issues/48) | No actual Rixa search flow has been selected; do not implement Search from the CMS label alone. |
| [#5 Gateway Bridge](https://github.com/AChWorks/achrix/issues/5) | Future optional manual/AI publishing/management is desired. Ownership/packaging/security decision remains owner-gated; Gateway repository is outside scope. |
| [#19 lifecycle proof](https://github.com/AChWorks/achrix/issues/19), [#49 recovery](https://github.com/AChWorks/achrix/issues/49) | #19 remains paused pending separate owner instruction; #49 remains downstream. Site-transfer development proof does not complete/activate either. |

Do not generalize custom roles, registration, commerce, conversion, advanced search, Notifications, page builders, update UI or adaptive resource controllers before an actual accepted flow justifies them. Later product scope follows the same ownership and compatibility rules; acceptance of a reusable boundary is not permission to create empty Modules.
