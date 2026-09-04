# Provider validation inventory

Declared interfaces audited in September 2026. Tests validate Atenea routing and adapters with fixtures; external tools remain unverified operationally.

| Capability | Implementations | Validation |
|---|---|---|
| code.context | kivgraph.context, tokensave.context | Declaration and local contracts |
| code.search | ripgrep, claude.search, codex.search | Declaration and local contracts |
| symbol.search | kivgraph.search | Declaration and local contracts |
| symbol.intent_search | kivgraph.intent_search | Declaration and local contracts |
| symbol.dependencies | kivgraph.dependencies | Declaration and local contracts |
| symbol.definition | kivgraph.definition | Declaration and local contracts |
| symbol.references | kivgraph.references | Declaration and local contracts |
| symbol.implementations | No implementation | Declaration and local contracts |
| symbol.overview | kivgraph.overview, tokensave.overview | Declaration and local contracts |
| symbol.calls | tokensave.calls | Declaration and local contracts |
| code.impact | kivgraph.impact | Declaration and local contracts |
| repository.index | kivgraph.index | Declaration and local contracts |
| symbol.consumers | kivgraph.cross_repo_consumers | Declaration and local contracts |
| symbol.get | kivgraph.get | Declaration and local contracts |
| symbol.unresolved | No implementation | Declaration and local contracts |
| graph.status | kivgraph.status | Declaration and local contracts |
| desktop.apps | macos.apps | Declaration and local contracts |
| desktop.inspect | macos.inspect | Declaration and local contracts |
| desktop.screenshot | macos.screenshot | Declaration and local contracts |
| desktop.click | macos.click | Declaration and local contracts |
| desktop.move | macos.move | Declaration and local contracts |
| desktop.drag | macos.drag | Declaration and local contracts |
| desktop.scroll | macos.scroll | Declaration and local contracts |
| desktop.type | macos.type | Declaration and local contracts |
| desktop.key | macos.key | Declaration and local contracts |
| symbol.source | kivgraph.source | Declaration and local contracts |
| symbol.impact | kivgraph.symbol_impact | Declaration and local contracts |
| graph.repositories | kivgraph.repositories | Declaration and local contracts |
| graph.ensure_fresh | kivgraph.ensure_fresh | Declaration and local contracts |
| web.fetch | scrapling.request, scrapling.fetch, scrapling.stealth | Declaration and local contracts |
| web.extract | scrapling.extract_request, scrapling.extract_fetch, scrapling.extract_stealth | Declaration and local contracts |
| web.crawl | scrapling.crawl, scrapling.crawl_stealth | Declaration and local contracts |

## Raw tool declarations

| Server | Tool | Validation |
|---|---|---|
| context7 | resolve-library-id | Declared; external operation not executed |
| context7 | query-docs | Declared; external operation not executed |
| semgrep | semgrep_rule_schema | Declared; external operation not executed |
| semgrep | get_supported_languages | Declared; external operation not executed |
| semgrep | semgrep_scan | Declared; external operation not executed |
| semgrep | semgrep_scan_with_custom_rule | Declared; external operation not executed |
| claude-mem | important_workflow | Declared; external operation not executed |
| claude-mem | search | Declared; external operation not executed |
| claude-mem | timeline | Declared; external operation not executed |
| claude-mem | get_observations | Declared; external operation not executed |
| claude-mem | session_start_context | Declared; external operation not executed |
| claude-mem | smart_search | Declared; external operation not executed |
| claude-mem | smart_unfold | Declared; external operation not executed |
| claude-mem | smart_outline | Declared; external operation not executed |
| claude-mem | build_corpus | Declared; external operation not executed |
| claude-mem | list_corpora | Declared; external operation not executed |
| claude-mem | prime_corpus | Declared; external operation not executed |
| claude-mem | query_corpus | Declared; external operation not executed |
| claude-mem | rebuild_corpus | Declared; external operation not executed |
| claude-mem | reprime_corpus | Declared; external operation not executed |
| agent-device | alert | Declared; external operation not executed |
| agent-device | app-switcher | Declared; external operation not executed |
| agent-device | apps | Declared; external operation not executed |
| agent-device | appstate | Declared; external operation not executed |
| agent-device | artifacts | Declared; external operation not executed |
| agent-device | audio | Declared; external operation not executed |
| agent-device | back | Declared; external operation not executed |
| agent-device | batch | Declared; external operation not executed |
| agent-device | boot | Declared; external operation not executed |
| agent-device | capabilities | Declared; external operation not executed |
| agent-device | click | Declared; external operation not executed |
| agent-device | clipboard | Declared; external operation not executed |
| agent-device | close | Declared; external operation not executed |
| agent-device | debug | Declared; external operation not executed |
| agent-device | devices | Declared; external operation not executed |
| agent-device | diff | Declared; external operation not executed |
| agent-device | doctor | Declared; external operation not executed |
| agent-device | events | Declared; external operation not executed |
| agent-device | fill | Declared; external operation not executed |
| agent-device | find | Declared; external operation not executed |
| agent-device | focus | Declared; external operation not executed |
| agent-device | get | Declared; external operation not executed |
| agent-device | gesture | Declared; external operation not executed |
| agent-device | home | Declared; external operation not executed |
| agent-device | hover | Declared; external operation not executed |
| agent-device | install | Declared; external operation not executed |
| agent-device | install-from-source | Declared; external operation not executed |
| agent-device | is | Declared; external operation not executed |
| agent-device | keyboard | Declared; external operation not executed |
| agent-device | logs | Declared; external operation not executed |
| agent-device | longpress | Declared; external operation not executed |
| agent-device | metro | Declared; external operation not executed |
| agent-device | network | Declared; external operation not executed |
| agent-device | open | Declared; external operation not executed |
| agent-device | orientation | Declared; external operation not executed |
| agent-device | perf | Declared; external operation not executed |
| agent-device | press | Declared; external operation not executed |
| agent-device | push | Declared; external operation not executed |
| agent-device | react-native | Declared; external operation not executed |
| agent-device | record | Declared; external operation not executed |
| agent-device | reinstall | Declared; external operation not executed |
| agent-device | replay | Declared; external operation not executed |
| agent-device | screenshot | Declared; external operation not executed |
| agent-device | scroll | Declared; external operation not executed |
| agent-device | session | Declared; external operation not executed |
| agent-device | settings | Declared; external operation not executed |
| agent-device | shutdown | Declared; external operation not executed |
| agent-device | snapshot | Declared; external operation not executed |
| agent-device | swipe | Declared; external operation not executed |
| agent-device | test | Declared; external operation not executed |
| agent-device | trace | Declared; external operation not executed |
| agent-device | trigger-app-event | Declared; external operation not executed |
| agent-device | tv-remote | Declared; external operation not executed |
| agent-device | type | Declared; external operation not executed |
| agent-device | viewport | Declared; external operation not executed |
| agent-device | wait | Declared; external operation not executed |
| agent-device | help | Declared; external operation not executed |
| diagram | doctor | Declared; external operation not executed |
| diagram | types | Declared; external operation not executed |
| diagram | spec | Declared; external operation not executed |
| diagram | templates | Declared; external operation not executed |
| diagram | template | Declared; external operation not executed |
| diagram | profiles | Declared; external operation not executed |
| diagram | import_drawio | Declared; external operation not executed |
| diagram | import_mermaid | Declared; external operation not executed |
| diagram | validate | Declared; external operation not executed |
| diagram | export_svg | Declared; external operation not executed |
| diagram | export_png | Declared; external operation not executed |

## Shipped agents

| Agent | Validation |
|---|---|
| filereader | Local contract tests; paid model behavior not certified |
| reviewer | Local contract tests; paid model behavior not certified |
| plan-check | Local contract tests; paid model behavior not certified |
| semantic-reviewer | Local contract tests; paid model behavior not certified |
| explore | Local contract tests; paid model behavior not certified |
| reader | Local contract tests; paid model behavior not certified |
| plan | Local contract tests; paid model behavior not certified |
