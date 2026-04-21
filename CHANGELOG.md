# Changelog

> **Note:** Versions 0.1.0–0.1.3 were originally published under [`theburrowhub/heimdallr-docker`](https://github.com/theburrowhub/heimdallr-docker) (now archived). The project was unified into this repository and renamed to Heimdallm in v0.1.1.

## [0.4.10](https://github.com/theburrowhub/heimdallm/compare/v0.4.9...v0.4.10) (2026-04-21)


### Features

* implement [#159](https://github.com/theburrowhub/heimdallm/issues/159) — feat: default PR metadata + per-org group overrides ([#161](https://github.com/theburrowhub/heimdallm/issues/161)) ([b46607d](https://github.com/theburrowhub/heimdallm/commit/b46607d3462c5fafcbbb13383efadc3ec32953b4))

## [0.4.9](https://github.com/theburrowhub/heimdallm/compare/v0.4.8...v0.4.9) (2026-04-21)


### Features

* implement [#137](https://github.com/theburrowhub/heimdallm/issues/137) — feat: heimdallm-cli — CLI client to monitor and interact with the daemon ([#157](https://github.com/theburrowhub/heimdallm/issues/157)) ([8c5a2cf](https://github.com/theburrowhub/heimdallm/commit/8c5a2cf137e8226503eb182e2fe7f23bf27ca4a6))

## [0.4.8](https://github.com/theburrowhub/heimdallm/compare/v0.4.7...v0.4.8) (2026-04-21)


### Features

* **config:** make AI CLI execution timeout configurable ([#156](https://github.com/theburrowhub/heimdallm/issues/156)) ([ae1ed9f](https://github.com/theburrowhub/heimdallm/commit/ae1ed9fcbfa14dd8bdefd52290cfd4d60b7360e6)), closes [#155](https://github.com/theburrowhub/heimdallm/issues/155)
* repos tab — filters, grid view, bulk bar, auto-discovery ([#112](https://github.com/theburrowhub/heimdallm/issues/112)) ([#139](https://github.com/theburrowhub/heimdallm/issues/139)) ([e17a6bf](https://github.com/theburrowhub/heimdallm/commit/e17a6bf32136e551e71d08425b9db6b3b079f432))


### Bug Fixes

* dedup PR reviews by HEAD commit SHA ([#151](https://github.com/theburrowhub/heimdallm/issues/151)) ([d16e51e](https://github.com/theburrowhub/heimdallm/commit/d16e51e5992c707124c0aa911414882a855dde03))

## [0.4.7](https://github.com/theburrowhub/heimdallm/compare/v0.4.6...v0.4.7) (2026-04-21)


### Bug Fixes

* use HTTPS with token for git fetch in CheckoutNewBranch ([#152](https://github.com/theburrowhub/heimdallm/issues/152)) ([ac9a39a](https://github.com/theburrowhub/heimdallm/commit/ac9a39ad8979ef1cfe84bd14bc443633f07a2e23)), closes [#148](https://github.com/theburrowhub/heimdallm/issues/148)

## [0.4.6](https://github.com/theburrowhub/heimdallm/compare/v0.4.5...v0.4.6) (2026-04-21)


### Bug Fixes

* add SSH client and agent forwarding for auto_implement ([#149](https://github.com/theburrowhub/heimdallm/issues/149)) ([00b12b1](https://github.com/theburrowhub/heimdallm/commit/00b12b111c9f5f4679e9e387d9e6949f12672113)), closes [#148](https://github.com/theburrowhub/heimdallm/issues/148)

## [0.4.5](https://github.com/theburrowhub/heimdallm/compare/v0.4.4...v0.4.5) (2026-04-21)


### Bug Fixes

* per-repo overrides lost on config save ([#138](https://github.com/theburrowhub/heimdallm/issues/138)) ([#140](https://github.com/theburrowhub/heimdallm/issues/140)) ([33a4668](https://github.com/theburrowhub/heimdallm/commit/33a4668a6c8e98a74423899e8caa290860dade01))

## [0.4.4](https://github.com/theburrowhub/heimdallm/compare/v0.4.3...v0.4.4) (2026-04-21)


### Features

* Repositories view — filters, auto-discover, grid toggle ([#112](https://github.com/theburrowhub/heimdallm/issues/112)) ([#133](https://github.com/theburrowhub/heimdallm/issues/133)) ([5e6790a](https://github.com/theburrowhub/heimdallm/commit/5e6790a85a3707d3adf334218b86d13712ff4465))


### Bug Fixes

* pass GitHubToken to issue pipeline RunOptions ([#142](https://github.com/theburrowhub/heimdallm/issues/142)) ([2a4126b](https://github.com/theburrowhub/heimdallm/commit/2a4126bdc0680b4e6b3a9320abd4c02df98ce8da)), closes [#141](https://github.com/theburrowhub/heimdallm/issues/141)

## [0.4.3](https://github.com/theburrowhub/heimdallm/compare/v0.4.2...v0.4.3) (2026-04-21)


### Features

* **config:** add local_dir_base as list for multi-group workspace support ([#132](https://github.com/theburrowhub/heimdallm/issues/132)) ([aaef09a](https://github.com/theburrowhub/heimdallm/commit/aaef09a36e9ddade82e1aaf43d8c79425cf22d39))

## [0.4.2](https://github.com/theburrowhub/heimdallm/compare/v0.4.1...v0.4.2) (2026-04-21)


### Features

* activity log foundation (store + issue_promoted event) ([#120](https://github.com/theburrowhub/heimdallm/issues/120)) ([1ce71e7](https://github.com/theburrowhub/heimdallm/commit/1ce71e7cced305558ef2fd82ff345ba9f7f901de))
* activity log recorder, config, HTTP endpoint + /stats counter ([#123](https://github.com/theburrowhub/heimdallm/issues/123)) ([bd81df5](https://github.com/theburrowhub/heimdallm/commit/bd81df500da75a36eede9620598f00313d984400))
* **config:** add watch_interval for Tier 3 per-item polling ([b4f9755](https://github.com/theburrowhub/heimdallm/commit/b4f975598a675059ca896df8b9356cfa8075850a))
* **daemon:** replace monolithic poll cycle with multi-tier pipeline ([#107](https://github.com/theburrowhub/heimdallm/issues/107)) ([01f32bf](https://github.com/theburrowhub/heimdallm/commit/01f32bfe8ea8019de76626316bdd18326ac3509f))
* Flutter Web replaces SvelteKit web_ui ([#125](https://github.com/theburrowhub/heimdallm/issues/125)) ([#126](https://github.com/theburrowhub/heimdallm/issues/126)) ([c226e97](https://github.com/theburrowhub/heimdallm/commit/c226e97a980db50a1af1efbc1083ba2a5400a1ec))
* **flutter:** Activity log tab with timeline, filters, and date picker ([#124](https://github.com/theburrowhub/heimdallm/issues/124)) ([7f2f47c](https://github.com/theburrowhub/heimdallm/commit/7f2f47c4f2de3d8a7661bf9605953394d766bbc0))
* **flutter:** categorize prompts into PR Review, Issue Triage, Development ([#122](https://github.com/theburrowhub/heimdallm/issues/122)) ([ea96303](https://github.com/theburrowhub/heimdallm/commit/ea963034c064263667eea332e8b25663040c6b32))
* **flutter:** unified Activity view with advanced filters ([#109](https://github.com/theburrowhub/heimdallm/issues/109)) ([93aa0a4](https://github.com/theburrowhub/heimdallm/commit/93aa0a4a419d5049d7f65429cffec5da78697291))
* **scheduler:** add Pipeline orchestrator wiring 3 tiers ([c9692af](https://github.com/theburrowhub/heimdallm/commit/c9692af309907deea3233b4c2a999e42edabc24c))
* **scheduler:** add priority queue with exponential backoff ([b67a7b5](https://github.com/theburrowhub/heimdallm/commit/b67a7b52a37b2044c03313e23e718c3ad21896fc))
* **scheduler:** add shared rate limiter with tier priorities ([0641b8a](https://github.com/theburrowhub/heimdallm/commit/0641b8a5f3f445cfa71c85c0f7d5d349c3c950db))
* **scheduler:** add Tier 1 discovery goroutine ([5ca7342](https://github.com/theburrowhub/heimdallm/commit/5ca7342681a3d066d847c09a7cda58a346a9e9ac))
* **scheduler:** add Tier 2 per-repo processing goroutine ([6f608c4](https://github.com/theburrowhub/heimdallm/commit/6f608c4f098535e008be1fd0f2c3edaa9f681fbe))
* **scheduler:** add Tier 3 per-item watch with backoff ([6ac4203](https://github.com/theburrowhub/heimdallm/commit/6ac4203f798ae66d643c72134985879d458d79cd))
* Stats filters — org and repo multi-select ([#110](https://github.com/theburrowhub/heimdallm/issues/110)) ([#127](https://github.com/theburrowhub/heimdallm/issues/127)) ([eb0fc78](https://github.com/theburrowhub/heimdallm/commit/eb0fc7849054dde9fd9d958208b01bfdce6f3a2d))
* **web:** native browser notifications for review events ([#129](https://github.com/theburrowhub/heimdallm/issues/129)) ([87e63f9](https://github.com/theburrowhub/heimdallm/commit/87e63f94b3162d420f99a631171ef2ef929ecc38))


### Bug Fixes

* address round-2 reviews — deduplicate SortMode, document TOCTOU + queue concurrency ([f930248](https://github.com/theburrowhub/heimdallm/commit/f93024836c455a184a52da38a2c3b6ea7732d3a4))
* **daemon:** address all review feedback on multi-tier pipeline ([32c58f8](https://github.com/theburrowhub/heimdallm/commit/32c58f88f5faee14790f6fb3420c5d497b19a617))
* **daemon:** serialise reloads, floor watch interval, log issue changes ([7606831](https://github.com/theburrowhub/heimdallm/commit/760683176743bfb2decc6814b8b58dade2367a5b))
* **flutter:** address review — dead params, dedup, cursor, ref.read ([0342ad7](https://github.com/theburrowhub/heimdallm/commit/0342ad720c018a31f0b8473c169e1884d14d7f7a))
* **flutter:** inline sort+filters bar, fix popup width, remove unused _SortButton ([b1142fb](https://github.com/theburrowhub/heimdallm/commit/b1142fb17d9b480bf39fbd50e405585acf57d7cb))
* **flutter:** persist Activity sort preference across sessions ([#108](https://github.com/theburrowhub/heimdallm/issues/108)) ([87162cf](https://github.com/theburrowhub/heimdallm/commit/87162cfecfc5c6fa89e20b2bd4b71453ba6abeb8))
* **flutter:** replace popup menu with styled dialog for org/repo filters ([bd995a6](https://github.com/theburrowhub/heimdallm/commit/bd995a6d1ece93c07ac419df82cee3090da1c08e))
* **flutter:** replace StateProvider with Notifier for sort persistence ([66064b5](https://github.com/theburrowhub/heimdallm/commit/66064b546a1e0295345690da144e25091cd53d24))
* **flutter:** use Notifier for sort persistence (review feedback [#115](https://github.com/theburrowhub/heimdallm/issues/115)) ([c9fe717](https://github.com/theburrowhub/heimdallm/commit/c9fe717e53972a62846cedb980503830d1d3ff36))


### Documentation

* add design spec for Flutter Web replacing SvelteKit web_ui ([af7a6f4](https://github.com/theburrowhub/heimdallm/commit/af7a6f44422fffce26572301ddd4b23ade28d309))
* design spec for multi-tier polling pipeline ([#107](https://github.com/theburrowhub/heimdallm/issues/107)) ([d28550a](https://github.com/theburrowhub/heimdallm/commit/d28550a2e630e9f67f8a4f947499c6179063b9e8))
* implementation plan for multi-tier polling pipeline ([60147a9](https://github.com/theburrowhub/heimdallm/commit/60147a97f825d2a2e4129b0419572a49dcb8e60c))

## [0.4.1](https://github.com/theburrowhub/heimdallm/compare/v0.4.0...v0.4.1) (2026-04-20)


### Features

* RepoDetailScreen + per-repo issue tracking config ([#99](https://github.com/theburrowhub/heimdallm/issues/99)) ([ffe7890](https://github.com/theburrowhub/heimdallm/commit/ffe78905adca581411e3c4135deb2202617ad2eb))


### Bug Fixes

* make run-linux usable end-to-end (HOME, token, gh CLI) ([#118](https://github.com/theburrowhub/heimdallm/issues/118)) ([53f72ce](https://github.com/theburrowhub/heimdallm/commit/53f72cee799fcfe9957c2aee48c6b647fe281614))
* per-repo IT implicit enable + LED consistency + PR metadata persistence ([#106](https://github.com/theburrowhub/heimdallm/issues/106)) ([5cb24ad](https://github.com/theburrowhub/heimdallm/commit/5cb24adf4c832890789bd0f89e2144e8fe936047))

## [0.4.0](https://github.com/theburrowhub/heimdallm/compare/v0.3.0...v0.4.0) (2026-04-20)


### ⚠ BREAKING CHANGES

* the `./config:/config` bind mount is replaced by a `heimdallm-config:/config` named volume. Operators with a customised `docker/config/config.toml` must copy it into the new volume before upgrading or the daemon will regenerate the file from env vars:

### Features

* add make run-linux target for Docker-based GUI testing ([#48](https://github.com/theburrowhub/heimdallm/issues/48)) ([6c3f66d](https://github.com/theburrowhub/heimdallm/commit/6c3f66dcb1ffec758c1033207f6836ff5f1bf3dd)), closes [#16](https://github.com/theburrowhub/heimdallm/issues/16)
* **api:** HTTP client for daemon REST API ([e1b1122](https://github.com/theburrowhub/heimdallm/commit/e1b1122d641a34b6301bf6494984db92f8597579))
* app icons from iconset, all UI strings in English, auto-boot flow ([1324981](https://github.com/theburrowhub/heimdallm/commit/1324981751daa5087ff5dc8a53c52776d9575da2))
* arranque automático sin config screen, repos desde PRs activas ([140c20a](https://github.com/theburrowhub/heimdallm/commit/140c20a0fdc0e02c91235c2e238146c7d58f1148))
* auto-save con debounce en Repos y Agents ([4c4ee0d](https://github.com/theburrowhub/heimdallm/commit/4c4ee0ddc69d910fab91d687c9e6176c82ce1e90))
* **config:** settings screen with poll interval, AI selection, retention ([751f323](https://github.com/theburrowhub/heimdallm/commit/751f3234ed32afdd7c987b9f43d533a06835c8fc))
* **config:** token desde gh CLI, descubrimiento de repos, config por-repo ([b3a1c55](https://github.com/theburrowhub/heimdallm/commit/b3a1c55e9f04aeb40a67048be03598473c5dfbb0))
* **config:** TOML config with defaults and per-repo AI overrides ([b3657fc](https://github.com/theburrowhub/heimdallm/commit/b3657fc4e5f1bcc1677845e6d2bd83e55a82fdeb))
* **daemon:** GET /logs/stream SSE endpoint for log tailing ([ef1a118](https://github.com/theburrowhub/heimdallm/commit/ef1a118b23e37e88e6f2edd555b6abb6656714d5))
* **daemon:** main.go wiring + LaunchAgent install/uninstall ([ab0f735](https://github.com/theburrowhub/heimdallm/commit/ab0f735f4ab14fe5adb7a1a58b022956aec037c8))
* **daemon:** size-based rotation for heimdallm.log ([#78](https://github.com/theburrowhub/heimdallm/issues/78)) ([e24a488](https://github.com/theburrowhub/heimdallm/commit/e24a4880ec2471abe213ade0a2b705d4cad8df0d))
* dashboard con 3 tabs (Reviews/Repos/Stats), /me y /stats en daemon ([1cd22a3](https://github.com/theburrowhub/heimdallm/commit/1cd22a3575c1e693d507795c18441de0aaf6575e))
* **dashboard:** PR list with severity badges and review trigger ([62ec38a](https://github.com/theburrowhub/heimdallm/commit/62ec38a3ec01d69eef61f870ed4423a07a6666ca))
* descartar PRs — dismiss/undo en dashboard y detalle ([b08f5c0](https://github.com/theburrowhub/heimdallm/commit/b08f5c0712153cbc72c271548918babf3600239c))
* **discovery:** auto-discover repos by GitHub topic tag ([#41](https://github.com/theburrowhub/heimdallm/issues/41)) ([17f91fc](https://github.com/theburrowhub/heimdallm/commit/17f91fcf5690c7446b0e07514ca775400e4e8633))
* DMG bonito con create-dmg, background personalizable en assets/dmg-background.png ([7fec3b4](https://github.com/theburrowhub/heimdallm/commit/7fec3b4efa1dd47b37ed33a926bae4bc8470ca6d))
* **docker:** web UI service in compose + make setup ([#68](https://github.com/theburrowhub/heimdallm/issues/68)) ([d81b7e8](https://github.com/theburrowhub/heimdallm/commit/d81b7e80c1ed26498c110ae7a07f7a0895d4c199))
* **executor:** add Comments to PRContext with placeholder and append fallback ([1f99180](https://github.com/theburrowhub/heimdallm/commit/1f9918063b9c25256668e2d8392901a6e67dae81))
* **executor:** AI CLI detection, execution, and JSON parsing ([98164a6](https://github.com/theburrowhub/heimdallm/commit/98164a694ba391d094354bb2d9b9e17a155b4316))
* flags Claude avanzados en pestaña Agents — effort, permission-mode, bare, etc. ([6125d3b](https://github.com/theburrowhub/heimdallm/commit/6125d3b0f0625579fbaa556ab206cf517faebd63))
* flags como chips editables + Save con feedback visual en Agents ([59d8541](https://github.com/theburrowhub/heimdallm/commit/59d8541a3bedc650097e5129ef051853f1994bfd))
* Flutter issue tracking views + daemon endpoints ([#28](https://github.com/theburrowhub/heimdallm/issues/28), [#29](https://github.com/theburrowhub/heimdallm/issues/29)) ([#46](https://github.com/theburrowhub/heimdallm/issues/46)) ([af0dcb3](https://github.com/theburrowhub/heimdallm/commit/af0dcb34e4a8ac29c8efcb5fb96bd874a0afec41))
* **flutter:** LogsScreen con SSE streaming y auto-scroll inteligente ([a56b5a4](https://github.com/theburrowhub/heimdallm/commit/a56b5a4198ce78b460038d1db39f8d09da7710ff))
* generación automática de iconos desde source 1024x1024 ([be00756](https://github.com/theburrowhub/heimdallm/commit/be00756739512d7e4bd6525fbd49d02dc80aa3cd))
* **github:** add Comment type and FetchComments for PR discussion context ([93b0247](https://github.com/theburrowhub/heimdallm/commit/93b02479d06163e5263271210f31c40476cd92c2))
* **github:** add SetPRReviewers, AddLabels, SetAssignees methods ([#56](https://github.com/theburrowhub/heimdallm/issues/56)) ([e8c1937](https://github.com/theburrowhub/heimdallm/commit/e8c1937ce9c8514066d37547395e168bea915711))
* **github:** API client for fetching PRs and diffs ([6aca63d](https://github.com/theburrowhub/heimdallm/commit/6aca63d8a5dd67aac7bbc0641cca7db86b2782c8))
* **heimdallr:** first-run setup UI — token Keychain, config desde la app, nombre Heimdallr ([1404c78](https://github.com/theburrowhub/heimdallm/commit/1404c78bf307d539829565f58d8b4b102c2236d5))
* instancia única, hide-to-tray y directorio visible en repo list ([01d6af0](https://github.com/theburrowhub/heimdallm/commit/01d6af0dee72bab0dc5049524554991c878b3858))
* **issues:** auto_implement pipeline — branch, commit, PR ([#45](https://github.com/theburrowhub/heimdallm/issues/45)) ([1186af4](https://github.com/theburrowhub/heimdallm/commit/1186af4e575bbe76c6b1d76feae5693e97fef4a7))
* **issues:** configurable prompts for auto_implement ([#55](https://github.com/theburrowhub/heimdallm/issues/55)) ([#63](https://github.com/theburrowhub/heimdallm/issues/63)) ([28b6a60](https://github.com/theburrowhub/heimdallm/commit/28b6a6005404e8ab106552443c59f76720b3e90b))
* **issues:** configurable prompts for issue triage ([#53](https://github.com/theburrowhub/heimdallm/issues/53)) ([#57](https://github.com/theburrowhub/heimdallm/issues/57)) ([ba2ea6d](https://github.com/theburrowhub/heimdallm/commit/ba2ea6d7e5bddc493810ccd72c1c8d1202a5f757))
* **issues:** GitHub client — FetchIssues with classification & priority sort ([#43](https://github.com/theburrowhub/heimdallm/issues/43)) ([4f9c574](https://github.com/theburrowhub/heimdallm/commit/4f9c574b76d8b20e910945d932220fe003f127d6))
* **issues:** integrate issue polling into daemon cycle ([#28](https://github.com/theburrowhub/heimdallm/issues/28)) ([#50](https://github.com/theburrowhub/heimdallm/issues/50)) ([4acfc25](https://github.com/theburrowhub/heimdallm/commit/4acfc25ff141896f01fb760e6db30d26edf1bdb0))
* **issues:** issue dependencies + auto-promote when blockers close ([#93](https://github.com/theburrowhub/heimdallm/issues/93)) ([6823967](https://github.com/theburrowhub/heimdallm/commit/68239676ab31fdbbd1bcde53119add5a355b82b2))
* **issues:** review_only triage pipeline + fetcher ([#44](https://github.com/theburrowhub/heimdallm/issues/44)) ([14c276f](https://github.com/theburrowhub/heimdallm/commit/14c276f7ba84e0dee55ba6502930248ebf78f4f2))
* **issues:** SQLite schema + config surface for issue tracking ([#42](https://github.com/theburrowhub/heimdallm/issues/42)) ([cad4445](https://github.com/theburrowhub/heimdallm/commit/cad4445823defae6de89dd99eb37c45fe52d41e1))
* **issues:** sub-issues support + promotion robustness (closes [#94](https://github.com/theburrowhub/heimdallm/issues/94), [#97](https://github.com/theburrowhub/heimdallm/issues/97)) ([#98](https://github.com/theburrowhub/heimdallm/issues/98)) ([6831393](https://github.com/theburrowhub/heimdallm/commit/6831393aa96063ba1757580e48bb3bdc8205c5c4))
* **logs:** UI estilo terminal — monospace, colores por nivel, wrap toggle ([631b717](https://github.com/theburrowhub/heimdallm/commit/631b717c7e5e13850dc11dc1e38686ceb3ebf997))
* **main:** app entry point with daemon lifecycle and router ([6a3f539](https://github.com/theburrowhub/heimdallm/commit/6a3f539266c64a0c6be51e75845fb9ef94754ae9))
* make release-local — firma local con Developer ID, notarización y GitHub release ([551e8ae](https://github.com/theburrowhub/heimdallm/commit/551e8aec9ddb34bf0ed3c88b15892f1e02842d12))
* **make:** add `make up-build` for local-source rebuild-and-start ([#89](https://github.com/theburrowhub/heimdallm/issues/89)) ([4b88603](https://github.com/theburrowhub/heimdallm/commit/4b886037145bc9c6ae8c535a29c929daa1cb6364))
* métricas de tiempo de revisión en Stats ([2c6c431](https://github.com/theburrowhub/heimdallm/commit/2c6c43111bcf64602a2126cda692430ff23788e4))
* **models:** PR, Review, Issue, AppConfig with json_serializable ([5e8a2f7](https://github.com/theburrowhub/heimdallm/commit/5e8a2f7141130cea7ae322466b44dc8988be3888))
* modos de revisión configurable — single (un comentario) y multi (un comentario por issue) ([a2564bb](https://github.com/theburrowhub/heimdallm/commit/a2564bb2aee4b472a3866a1a1368c6546f80680c))
* pantalla de error con mensaje claro, hint y botón Retry cuando el daemon no arranca ([84edfe3](https://github.com/theburrowhub/heimdallm/commit/84edfe3677632782f0c2e7c4a8fed2655ef3a330))
* **pipeline:** inject PR comments into AI prompt for full discussion context ([8a39fb7](https://github.com/theburrowhub/heimdallm/commit/8a39fb70b1eee35be536f655210d58c9cd769999))
* **pipeline:** review orchestration with interfaces for testability ([42fba3e](https://github.com/theburrowhub/heimdallm/commit/42fba3e8199613a186fd6ca7213a0e40bc9f023e))
* **pr-detail:** split-panel PR review screen ([32ce5cb](https://github.com/theburrowhub/heimdallm/commit/32ce5cb39d19c866c2f45cdf783d7dae37b9badf))
* prompt por repo en Repositories, PRs ordenadas pendiente→issues→resueltas, secciones colapsables ([6d5e511](https://github.com/theburrowhub/heimdallm/commit/6d5e5110e1639bd0fdf0b48eb05d4b9b873d2bfd))
* **providers:** Riverpod providers for PRs, detail, and config ([c2244b4](https://github.com/theburrowhub/heimdallm/commit/c2244b4274c378e9b6fc3b18e86edf685deacf2a))
* rediseño menú systray — limpio, orientado a acción ([938af0f](https://github.com/theburrowhub/heimdallm/commit/938af0f1dbd6949b5374223b6e5405ae580452b8))
* rename project from Heimdallr to Heimdallm (Heimdall + LLM) ([#35](https://github.com/theburrowhub/heimdallm/issues/35)) ([c3a6de5](https://github.com/theburrowhub/heimdallm/commit/c3a6de53c01ce5bf88b92e5ee9f19187ea9e825a)), closes [#22](https://github.com/theburrowhub/heimdallm/issues/22)
* repos agrupados por org + persistencia de repos no monitored ([1e411ff](https://github.com/theburrowhub/heimdallm/commit/1e411ff7e5d88e23d00ad0ac338a2cf55e5599b1))
* reviews publicadas en GitHub, no re-review si PR no cambió, retry si falla publicación ([6f874ce](https://github.com/theburrowhub/heimdallm/commit/6f874cef0e396c57792fa363bc8e6d7bb7cce379))
* scheduler, macOS notifications, and Keychain token storage ([6e5d50c](https://github.com/theburrowhub/heimdallm/commit/6e5d50cb24d8d9c73e358d6a3d5690adf358ba80))
* selector de ordenación en Reviews — Priority / Newest ([e45a2db](https://github.com/theburrowhub/heimdallm/commit/e45a2dbc91dfdb189fcf9a94cdb31914517472b4))
* **server:** REST handlers and SSE endpoint ([d20bbf2](https://github.com/theburrowhub/heimdallm/commit/d20bbf2434c86716dfb7b30b2c13f8afdf34af38))
* show GitHub review decision badge on PR list + detail ([#101](https://github.com/theburrowhub/heimdallm/issues/101)) ([0ad6eec](https://github.com/theburrowhub/heimdallm/commit/0ad6eecb84b44c77c1b79500c33d0a6e6f9a26bf))
* soporte Linux completo — credenciales, plataforma Flutter, .deb/.rpm/.AppImage ([11e63e9](https://github.com/theburrowhub/heimdallm/commit/11e63e9b477ac2b585839493c5f73b4f72e759f2))
* **sse+daemon:** SSE stream client and daemon lifecycle manager ([9227f8b](https://github.com/theburrowhub/heimdallm/commit/9227f8b45ca00c8f4c923fba81d1cf6303f13cd2))
* **sse:** fan-out SSE broker for real-time events ([ceb5996](https://github.com/theburrowhub/heimdallm/commit/ceb59969eaea9e846d744451e4e5b0facff9276a))
* **store:** SQLite store with PR, review, and config CRUD ([56cedd4](https://github.com/theburrowhub/heimdallm/commit/56cedd415d955866eea4ce321cfee2fd68a94738))
* systray menu rico con My PRs, My Reviews, Repositories, View on GH, Review Now ([1a6f1cc](https://github.com/theburrowhub/heimdallm/commit/1a6f1cc2659f4883b5e4cd7b8e2f470590f5afbf))
* tab Agents con prompts personalizados, fix Repositories (GET /config real), placeholders {title}{diff}etc ([e5a2530](https://github.com/theburrowhub/heimdallm/commit/e5a253015174edf5fc73208431c577700ec513f8))
* tab Agents, directorio local por repo y config por agente CLI ([1fe0c67](https://github.com/theburrowhub/heimdallm/commit/1fe0c67cd20de22a21e2a47e64e77a32cb579f14))
* tab Prompts con presets (Security/Performance/Architecture/etc), instrucciones vs template completo ([e13733d](https://github.com/theburrowhub/heimdallm/commit/e13733d766feda03e0c285783af83a02b2c11bb3))
* unify heimdallr + heimdallr-docker, rename to heimdallm ([#21](https://github.com/theburrowhub/heimdallm/issues/21)) ([e4d3f9d](https://github.com/theburrowhub/heimdallm/commit/e4d3f9d356748373b35838d677459db55db83cd2))
* **web_ui:** dark mode with system / light / dark toggle ([#74](https://github.com/theburrowhub/heimdallm/issues/74)) ([45cc987](https://github.com/theburrowhub/heimdallm/commit/45cc987caedbdbd08ccd94010ad4542d0d4562da))
* **web_ui:** reuse Heimdallm app icon in header + favicon ([#85](https://github.com/theburrowhub/heimdallm/issues/85)) ([53b439e](https://github.com/theburrowhub/heimdallm/commit/53b439e43335199c81d8b9eb81bdfc226c6bd652))
* **web_ui:** scaffold SvelteKit + API/SSE clients ([#30](https://github.com/theburrowhub/heimdallm/issues/30)) ([#60](https://github.com/theburrowhub/heimdallm/issues/60)) ([2ae3049](https://github.com/theburrowhub/heimdallm/commit/2ae30499b67d36cd782419ed1e33e3a37453038e))
* **web-ui:** Config, Agents and Logs routes ([#66](https://github.com/theburrowhub/heimdallm/issues/66)) ([142f87f](https://github.com/theburrowhub/heimdallm/commit/142f87fd1a85154b7ebdd2af0646d99df249fe7d))
* **widgets:** SeverityBadge, Toast, GoRouter, and screen stubs ([de95d43](https://github.com/theburrowhub/heimdallm/commit/de95d43cfd4d7cead531aa44c9a8bd5a382af767))


### Bug Fixes

* abrir en /config cuando daemon no corre, navegar a dashboard tras setup ([bd741f0](https://github.com/theburrowhub/heimdallm/commit/bd741f065d723628efe9e67fc68de2d6bcab56be))
* añadir ValidateExtraFlags a executor (reemplaza PR [#15](https://github.com/theburrowhub/heimdallm/issues/15) con conflicto) ([6d216f0](https://github.com/theburrowhub/heimdallm/commit/6d216f09ec381afbae4968bf1c65af7a373e64e0))
* applicationShouldTerminateAfterLastWindowClosed = false — tray app no sale al cerrar ventana ([7cf48cd](https://github.com/theburrowhub/heimdallm/commit/7cf48cd8cd48e43fd408b9b1af0bdd521275de4e))
* auto-review no disparaba con muchos repos — query GitHub demasiado larga ([5ef747b](https://github.com/theburrowhub/heimdallm/commit/5ef747bfdf8da874273bfdab19806fda9a2e5fd8))
* botón atrás en todas las vistas, notificaciones navegan a PR detail con pr_id ([2fa9e7f](https://github.com/theburrowhub/heimdallm/commit/2fa9e7f687e15035ef334aa6bc15a0b84370b0cd))
* botón atrás siempre visible, fallback a dashboard si no hay stack ([6054783](https://github.com/theburrowhub/heimdallm/commit/6054783eab1b2696d891bece240fa28b0341ecf5))
* botón Review/Re-review, feedback de errores y retención persistente ([8791918](https://github.com/theburrowhub/heimdallm/commit/8791918b3e15bbcab6d938e18e7a2298623e8bd8))
* capturar stdout en errores del executor — claude --bare escribe a stdout ([db8de35](https://github.com/theburrowhub/heimdallm/commit/db8de351efc1ec045f5026dea85ff0616b9c036e))
* **ci:** consolidate build jobs into release.yml ([#65](https://github.com/theburrowhub/heimdallm/issues/65)) ([3c7d1ea](https://github.com/theburrowhub/heimdallm/commit/3c7d1ea653d56d254317021698828fd4afff4cc1))
* claude falla con exit 1 — ejecutar via login shell para heredar ANTHROPIC_API_KEY ([6c8d34e](https://github.com/theburrowhub/heimdallm/commit/6c8d34ed596efab910d192b2521b7f47cb493a93))
* codesign CI preserva entitlements, main() captura errores de init, daemon path con debugPrint ([26595ea](https://github.com/theburrowhub/heimdallm/commit/26595eac37e55dfa8c2db4fa673a8bd200f3a2fa))
* config perdida entre compilaciones — dev-stop mata UI + skip single-instance en debug ([5ce0dee](https://github.com/theburrowhub/heimdallm/commit/5ce0dee25a3119c10f59feaa8b3dd094edec2c4c))
* config save + reload resilience (three linked bugs) ([#100](https://github.com/theburrowhub/heimdallm/issues/100)) ([d018754](https://github.com/theburrowhub/heimdallm/commit/d018754fe15cccdbefde5aca45bcf1d43cae1cd1))
* curly braces en _setError (flutter analyze) ([455b984](https://github.com/theburrowhub/heimdallm/commit/455b9848ebefad244b17c3547e771c64545bec28))
* daemon arranca sin repos, recarga config via POST /reload, dashboard sin panic ([b591612](https://github.com/theburrowhub/heimdallm/commit/b59161209b5f5a5d77d4916386eed99bf0aa9f0a))
* daemon en ProcessStartMode.detached — sobrevive al cierre de Flutter ([e9e3d82](https://github.com/theburrowhub/heimdallm/commit/e9e3d825e807e778fdf657c5f0d2fb340f8481ed))
* **daemon:** accept read-only fields in PUT /config so web UI saves don't 400 ([#87](https://github.com/theburrowhub/heimdallm/issues/87)) ([4a49c96](https://github.com/theburrowhub/heimdallm/commit/4a49c9647f4298c4badcdb66e424473d28329f45)), closes [#86](https://github.com/theburrowhub/heimdallm/issues/86)
* **daemon:** harden store-layer merge — atomic, strict reload, drop server_port ([#82](https://github.com/theburrowhub/heimdallm/issues/82)) ([4cec262](https://github.com/theburrowhub/heimdallm/commit/4cec2629f1c9a54b03d542752b5fce2b16ae8dc8))
* **daemon:** make /logs stream work under Docker ([#76](https://github.com/theburrowhub/heimdallm/issues/76)) ([6fbd26c](https://github.com/theburrowhub/heimdallm/commit/6fbd26c397f80adcfb297f20e7ae76edd8dc2af3))
* **daemon:** make PUT /config persist — third precedence layer (store &gt; env &gt; TOML) ([#80](https://github.com/theburrowhub/heimdallm/issues/80)) ([61f8fa4](https://github.com/theburrowhub/heimdallm/commit/61f8fa4c3851fa9d7b5ac43f8119fb3f13361c18)), closes [#79](https://github.com/theburrowhub/heimdallm/issues/79)
* **daemon:** write api_token with world-readable perms (0644) ([#72](https://github.com/theburrowhub/heimdallm/issues/72)) ([6f8efbe](https://github.com/theburrowhub/heimdallm/commit/6f8efbea00d3c0e9ff5526c269b5c45dfaa027e7)), closes [#71](https://github.com/theburrowhub/heimdallm/issues/71)
* desactivar app sandbox (bloquea Process.start), simplificar make dev ([408596f](https://github.com/theburrowhub/heimdallm/commit/408596fd11931283c85198320c5f906daa59c912))
* dialog prompts usa SizedBox en lugar de ConstrainedBox para evitar unbounded height ([862dde4](https://github.com/theburrowhub/heimdallm/commit/862dde411ce129f3d7d8e67c24cf5b1767a5905a))
* **discovery:** add non_monitored blacklist, org inference, and count drop warning ([#47](https://github.com/theburrowhub/heimdallm/issues/47)) ([320634d](https://github.com/theburrowhub/heimdallm/commit/320634dfc5a23f403b7dff671c02ffd531c2f35c)), closes [#39](https://github.com/theburrowhub/heimdallm/issues/39)
* **docker:** correct web service build context ([5623c52](https://github.com/theburrowhub/heimdallm/commit/5623c52c99301ae6107ae1b6630e849e5d1d56f0))
* **docker:** forward HEIMDALLM_ISSUE_* + HEIMDALLM_DISCOVERY_* env vars ([#96](https://github.com/theburrowhub/heimdallm/issues/96)) ([310c048](https://github.com/theburrowhub/heimdallm/commit/310c048deea31d4505c97545fafbc87637dece00))
* excluir PRs sin repo del dashboard ([dfcc280](https://github.com/theburrowhub/heimdallm/commit/dfcc280ad775f3059bef41a94bc5ce1df9f775a7))
* executor busca CLI en login shell — /opt/homebrew/bin invisible desde GUI ([a811fe3](https://github.com/theburrowhub/heimdallm/commit/a811fe31da00fcdbe078bc811224a14dfc0a3ff7))
* **executor,github:** restore PATH correctly, per-CLI args, proper URL encoding ([72bedc7](https://github.com/theburrowhub/heimdallm/commit/72bedc7cc77ff225b865e0220f6dae5e6695c972))
* **executor:** add opencode CLI support in buildArgs ([#62](https://github.com/theburrowhub/heimdallm/issues/62)) ([8ef0a7b](https://github.com/theburrowhub/heimdallm/commit/8ef0a7b90c72db15ffe544debea1bbd9b3081102))
* FetchPRs sin repos busca todas las PRs del usuario, sin guard vacío ([37a0d9d](https://github.com/theburrowhub/heimdallm/commit/37a0d9d2d359d9c139113661c60ec14d36be0222))
* **flutter:** SSE disconnect closes subscription, triggerReview as Notifier ([0389569](https://github.com/theburrowhub/heimdallm/commit/0389569b5a2eb50a481a72fc040a370a23032ab6))
* gh CLI lookup via login shell y paths hardcodeados, sin validación de repos ([7e4f228](https://github.com/theburrowhub/heimdallm/commit/7e4f22889e768c684b7dff5a8e5eb152ddfefb9c))
* import DaemonLifecycle en main.dart, _daemonBinaryPath delega sin duplicar lógica ([2d8791c](https://github.com/theburrowhub/heimdallm/commit/2d8791ce0e2501681ba0315edeed59059e3c54c9))
* layout tile PR, orden severidades y spinner de revisión en curso ([fc43e9e](https://github.com/theburrowhub/heimdallm/commit/fc43e9ec8b48ba7e49be909eab1ef44461bae7b8))
* Linux taskbar icon not showing on GNOME ([#10](https://github.com/theburrowhub/heimdallm/issues/10)) ([#11](https://github.com/theburrowhub/heimdallm/issues/11)) ([d2a1865](https://github.com/theburrowhub/heimdallm/commit/d2a1865324e5d8f6f8e8ee3f1498647a13a1fbf3))
* **logs:** leer stderr log + header auth correcto en SseClient ([8958f33](https://github.com/theburrowhub/heimdallm/commit/8958f339348659459a315b6f014b542f635723f9))
* **main:** remove dead broker field, stop broker on shutdown, modern launchctl, ParseDuration ([1c8acfe](https://github.com/theburrowhub/heimdallm/commit/1c8acfe96029ea497fabc8aeb5571d52e0102cb6))
* no pasar stdin por el login shell — consumía el prompt enviado a claude ([d0347c7](https://github.com/theburrowhub/heimdallm/commit/d0347c73d54e9c1419d68d61060106e0133b368c))
* nombre Heimdallr en ventana, icono en menubar macOS, splash sin timeout ([d0e3caf](https://github.com/theburrowhub/heimdallm/commit/d0e3caf83a53302f3eb6e562d9a797b21a948d1e))
* normalizar strings vacíos a null en repo_overrides del config ([4cd53de](https://github.com/theburrowhub/heimdallm/commit/4cd53de2aeb02c3a0b3c618060dfb93561ab109d))
* notificaciones solo desde Flutter, no parpadeo, layout responsive, Review funciona, Open on GitHub ([8b939a3](https://github.com/theburrowhub/heimdallm/commit/8b939a3d4827a4f9cf093340593efc7072dad08b))
* overflow en preset cards y stats bar chart ([0a75128](https://github.com/theburrowhub/heimdallm/commit/0a7512876692a69f08072bbb70a14e1b57cb219e))
* platform-agnostic daemon log path in /logs/stream ([f58578b](https://github.com/theburrowhub/heimdallm/commit/f58578b98b47d7b21228dacd786fba88484c4dc3))
* PRs con repo vacío — botón deshabilitado, aviso y SSE error propagado ([3987f2e](https://github.com/theburrowhub/heimdallm/commit/3987f2ee0feed5972014bd5521c45564161d14b5))
* PRs ordenadas por fecha desc dentro de cada nivel de severidad ([9738112](https://github.com/theburrowhub/heimdallm/commit/9738112a266397c91361e120d027e43dcac24ec2))
* queries GitHub separadas por qualifier (no OR), resolve repo desde repository_url ([204eb5b](https://github.com/theburrowhub/heimdallm/commit/204eb5baa66155b81dc9547b1ba46d5ccbb9fbbd))
* re-review loop — gracia de 30s para el updated_at que GitHub cambia al postear review ([9e8006c](https://github.com/theburrowhub/heimdallm/commit/9e8006ce2883ae192457a742ef8f53424a394742))
* re-reviews no longer repeat false positives — structured review cycle context ([#92](https://github.com/theburrowhub/heimdallm/issues/92)) ([1cc96b5](https://github.com/theburrowhub/heimdallm/commit/1cc96b5ac480fe6e0132db074a9b8534387dec78))
* renombrar rutas auto-pr → heimdallr en logs y LaunchAgent ([4c13332](https://github.com/theburrowhub/heimdallm/commit/4c13332377552bfbe8e48e6c84fb833306db7ef2))
* renombrar todo a Heimdallr, make dev completo para pruebas locales ([925d267](https://github.com/theburrowhub/heimdallm/commit/925d2679fb915785084b763f75e8b6c57e2ff143))
* retention no se guardaba + SnackBar permanente en dismiss ([2f009ad](https://github.com/theburrowhub/heimdallm/commit/2f009ad4d36eeb568a52b9a50dd5eeead50540bb))
* reviews en goroutine — poll loop no bloquea durante análisis largos ([81b937d](https://github.com/theburrowhub/heimdallm/commit/81b937d2cbefcb1b53a0555d1e3a002733eebd55))
* scheduler safe double-stop, SSE unsubscribe non-blocking after shutdown ([8e7c02f](https://github.com/theburrowhub/heimdallm/commit/8e7c02f9d00e23ca6d353d816cd880d1a24327a0))
* **security:** abort startup if API token cannot be created ([f1cd314](https://github.com/theburrowhub/heimdallm/commit/f1cd314e63ac597b50fb409c0c9605c7162a8054))
* **security:** allowlist CLI names + positional arg in resolveCLIPath (issue [#2](https://github.com/theburrowhub/heimdallm/issues/2)) ([#8](https://github.com/theburrowhub/heimdallm/issues/8)) ([ac1d5d4](https://github.com/theburrowhub/heimdallm/commit/ac1d5d40871b665835a4dcc21c71505730b07235))
* **security:** authenticate mutating API endpoints with per-daemon token (issue [#3](https://github.com/theburrowhub/heimdallm/issues/3)) ([#9](https://github.com/theburrowhub/heimdallm/issues/9)) ([7a73e03](https://github.com/theburrowhub/heimdallm/commit/7a73e031eca1ce6b6b4894cf17aaa23fb20f1a0e))
* **security:** block additional credential directories from AI workdir ([c9b54a5](https://github.com/theburrowhub/heimdallm/commit/c9b54a5ef17827b0feed8e52936bf11225578293))
* **security:** catch --flag=value bypass in ValidateExtraFlags, block --permission-mode ([#18](https://github.com/theburrowhub/heimdallm/issues/18)) ([fbc59f5](https://github.com/theburrowhub/heimdallm/commit/fbc59f5cf5bcbabfd12e9b9b86a218eb722c46ea))
* **security:** corregir 3 vulnerabilidades detectadas en auditoría ([#20](https://github.com/theburrowhub/heimdallm/issues/20)) ([94ce891](https://github.com/theburrowhub/heimdallm/commit/94ce891d936a4ca9d5e7ad749a15cdf5f97a75ca))
* **security:** corregir vulnerabilidades backend daemon (C-1, C-2, A-1, A-2, A-3, M-1, M-2, M-4, M-5, B-1) ([c8a15ab](https://github.com/theburrowhub/heimdallm/commit/c8a15ab8478e45e61f66283fa66bcc1935c5ccb5))
* **security:** corregir vulnerabilidades Flutter frontend (C-3, C-4, A-7, M-9, B-3) ([6d6d619](https://github.com/theburrowhub/heimdallm/commit/6d6d6191f8b6b90be46139bfec835c4748b4f63f))
* **security:** corregir vulnerabilidades infra (A-4, A-5, A-6, M-3, M-6, M-7, M-8) ([e079e53](https://github.com/theburrowhub/heimdallm/commit/e079e534a8ad57ceadb5103ec409c899ce1d8e0f))
* **security:** escape user strings in TOML config generation (issue [#6](https://github.com/theburrowhub/heimdallm/issues/6)) ([#14](https://github.com/theburrowhub/heimdallm/issues/14)) ([7acb643](https://github.com/theburrowhub/heimdallm/commit/7acb64377522ad81d48ba3079a87424437074d5b))
* **security:** limit concurrent review triggers with counting semaphore ([3befdfe](https://github.com/theburrowhub/heimdallm/commit/3befdfe9d5f755640ccbbc5d12ea34c36daedefb))
* **security:** protect cachedLogin with mutex to eliminate data race ([d47adbc](https://github.com/theburrowhub/heimdallm/commit/d47adbc6154b69f2e72d100db01f489d3e2f353f))
* **security:** remove CORS wildcard from SSE endpoint (issue [#4](https://github.com/theburrowhub/heimdallm/issues/4)) ([#12](https://github.com/theburrowhub/heimdallm/issues/12)) ([65ce4b1](https://github.com/theburrowhub/heimdallm/commit/65ce4b1b4fb05815dfa352481733ae9e5ecc8040))
* **security:** require auth on GET /config and GET /agents; allowlist PUT /config keys ([#19](https://github.com/theburrowhub/heimdallm/issues/19)) ([7d72a86](https://github.com/theburrowhub/heimdallm/commit/7d72a8613b1a61acdcca5b43f3352527992bb738))
* **security:** require auth token on /me, /prs, /stats endpoints ([2eb611a](https://github.com/theburrowhub/heimdallm/commit/2eb611a40aed083337f58d0f60a394debb63c253))
* **security:** resolve symlinks in ValidateWorkDir before applying denylist ([#17](https://github.com/theburrowhub/heimdallm/issues/17)) ([a33e400](https://github.com/theburrowhub/heimdallm/commit/a33e4006deea72aa0a3afca4803ba02e82c4fb04))
* **security:** use json.Marshal instead of fmt.Sprintf for SSE event JSON ([3657923](https://github.com/theburrowhub/heimdallm/commit/3657923109c6b2c0146649eeb41fb7043ec54d36))
* **security:** validate config values in PUT /config ([406a4b0](https://github.com/theburrowhub/heimdallm/commit/406a4b04e7203441ce6c71676df4f7ca16e89775))
* **security:** validate WorkDir before setting as AI CLI working directory ([#13](https://github.com/theburrowhub/heimdallm/issues/13)) ([750abd8](https://github.com/theburrowhub/heimdallm/commit/750abd8bd9a266db7a122cea2c562e31175e9e97))
* setPreventClose desde postFrameCallback — garantiza que el window está listo ([b383daf](https://github.com/theburrowhub/heimdallm/commit/b383dafacf03011cf076057026346efdfccafb9d))
* show() directo en setupWindow — waitUntilReadyToShow no dispara en bundles de producción ([51b0142](https://github.com/theburrowhub/heimdallm/commit/51b01423812181de3128fa1909a543b9098327e3))
* simplificar detección de token gh CLI, eliminar doble llamada ([cef3738](https://github.com/theburrowhub/heimdallm/commit/cef3738d869e30d66621d7df3a653ea87d674ce8))
* single instance en todos los modos — check activo en debug + dev-stop más agresivo ([676ab7a](https://github.com/theburrowhub/heimdallm/commit/676ab7a0223889c54089ed0720d1b4865b4380dc))
* single instance via PID file — funciona en debug y producción ([c22eb21](https://github.com/theburrowhub/heimdallm/commit/c22eb21d7213cb4ec64799b5b573813efea7b708))
* single instance vía SIGUSR1 — la app lo gestiona sin depender del OS ni del Makefile ([4f72082](https://github.com/theburrowhub/heimdallm/commit/4f720822420c22c0268bf91886809e9a78c8ef3d))
* SnackBar dismiss — showCloseIcon + duration explícito en todos los snackbars ([29d9501](https://github.com/theburrowhub/heimdallm/commit/29d9501bd7be01b69d36627bd5d0060636d3b408))
* solo high severity bloquea (REQUEST_CHANGES), todo lo demás aprueba (APPROVE) ([32db204](https://github.com/theburrowhub/heimdallm/commit/32db204ba97b8a5ce8fba92eef8d7a3bf01c1a72))
* splash infinita hasta que daemon responde, sin fallback a Settings si hay token ([24e8ced](https://github.com/theburrowhub/heimdallm/commit/24e8ced64c6656643090d74a23f41cf372c8b2ea))
* SSE reconnect + sort persistente en Reviews ([40e13a5](https://github.com/theburrowhub/heimdallm/commit/40e13a599ac45d90749db449b1a6810b5b2860b7))
* **sse:** close subscriber channels on broker Stop() ([199c478](https://github.com/theburrowhub/heimdallm/commit/199c4780fa8c816b62b964af7744ceeb2883b63c))
* **sse:** enviar token de auth en SseClient — corrige 401 en /events y /logs/stream ([d6227be](https://github.com/theburrowhub/heimdallm/commit/d6227be73b61fbc885fd49b33719d2fc311ee11e))
* **store:** add json tags to PR and Review structs for snake_case wire format ([76a60ed](https://github.com/theburrowhub/heimdallm/commit/76a60eda55a2bed1380987a7ae09309a7b211f58))
* **store:** use SQLite datetime format, propagate time parse errors ([d121755](https://github.com/theburrowhub/heimdallm/commit/d1217553793de000625f24fefc0c295cdc7fd879))
* sudo gem install fpm — permisos en Ubuntu CI ([8c2e757](https://github.com/theburrowhub/heimdallm/commit/8c2e757c3059f3cc5471decd0d36166822cdcd3a))
* tamaño ventana, notificaciones Flutter (icono correcto + click abre PR), botón atrás ([b032ac5](https://github.com/theburrowhub/heimdallm/commit/b032ac51a671cfaf060880af2c311d2969df1d10))
* test config_test actualizado (claude movido a Agents tab) + rpm en lugar de rpm-build en CI ([9e616f6](https://github.com/theburrowhub/heimdallm/commit/9e616f6b7187c409c6d2b7ed080b2d195f218ce5))
* tray mostraba mis PRs cuando me aún no cargó — prsProvider depende de meProvider ([496d3e3](https://github.com/theburrowhub/heimdallm/commit/496d3e3fdef9f710c31f19a31dcd9b611e11a04d))
* validar cli_flags de perfiles de prompt contra denylist antes de ejecutar ([faa8862](https://github.com/theburrowhub/heimdallm/commit/faa8862fa6774e3b8ff5804bb433fd11094a05a1))


### Documentation

* actualizar README, LLM guide y GitHub Pages para Linux y modos de revisión ([feab4b5](https://github.com/theburrowhub/heimdallm/commit/feab4b587cfa6bcd476c8b2247b2a225373bb1f2))
* add auto-pr design spec ([42cc246](https://github.com/theburrowhub/heimdallm/commit/42cc2466765a4e785838803ab1c13c64029c5820))
* add daemon and Flutter implementation plans ([75ba4bd](https://github.com/theburrowhub/heimdallm/commit/75ba4bd61a598a6693cb6c3b155fc4e3dbc4657d))
* entitlements embebidos en LLM guide (sin depender del repo), hdiutil detach robusto ([508b8df](https://github.com/theburrowhub/heimdallm/commit/508b8df14839069328cdac69d062ca0a575558e0))
* Heimdallm v2 design spec — issue tracking, rename, web UI ([43b4cb6](https://github.com/theburrowhub/heimdallm/commit/43b4cb6905b01f3cb778812e0f065c30a520714f))
* plan PR comments context injection ([ef214d1](https://github.com/theburrowhub/heimdallm/commit/ef214d1d0ac57449103f84308887273ae73b77c4))
* README, LLM-HOW-TO-INSTALL y GitHub Pages ([3e15bdc](https://github.com/theburrowhub/heimdallm/commit/3e15bdcc83955b6ddcbc311d1c85a7ad923389af))
* **readme:** cover three install gotchas that tripped a real user ([d8b16e9](https://github.com/theburrowhub/heimdallm/commit/d8b16e9eeb72d5d20348abf915f96f215118db75))
* **readme:** document issue pipeline, web UI, and topic discovery ([e03f3cb](https://github.com/theburrowhub/heimdallm/commit/e03f3cb05c7747acb3a1f0fffba1d1d148d26fd4))
* **readme:** document reusing host AI auth inside Docker ([f0bfe49](https://github.com/theburrowhub/heimdallm/commit/f0bfe49580990b2dfa4cf14cb0cbbc9230f151b6))
* **readme:** promote Claude OAuth path to first-class instructions ([35ec2e5](https://github.com/theburrowhub/heimdallm/commit/35ec2e522dd722daa8e4635312edf174c23b9eff))
* simplificar LLM guide — solo descargar, instalar, permitir uso ([9f5cea0](https://github.com/theburrowhub/heimdallm/commit/9f5cea00903378a0f68afb14316bb75f8d578ad5))
* spec PR comments context injection ([86a61b0](https://github.com/theburrowhub/heimdallm/commit/86a61b06c39eb6bf95a63fecc566dbf054604f4e))
* update documentation post-consolidation and rename to Heimdallm ([#37](https://github.com/theburrowhub/heimdallm/issues/37)) ([032398d](https://github.com/theburrowhub/heimdallm/commit/032398dfdd38583ef776eed2fbdee7a558fcfe0b))

## [0.3.0](https://github.com/theburrowhub/heimdallm/compare/v0.2.0...v0.3.0) (2026-04-20)


### ⚠ BREAKING CHANGES

* the `./config:/config` bind mount is replaced by a `heimdallm-config:/config` named volume. Operators with a customised `docker/config/config.toml` must copy it into the new volume before upgrading or the daemon will regenerate the file from env vars:

### Features

* add make run-linux target for Docker-based GUI testing ([#48](https://github.com/theburrowhub/heimdallm/issues/48)) ([6c3f66d](https://github.com/theburrowhub/heimdallm/commit/6c3f66dcb1ffec758c1033207f6836ff5f1bf3dd)), closes [#16](https://github.com/theburrowhub/heimdallm/issues/16)
* **api:** HTTP client for daemon REST API ([e1b1122](https://github.com/theburrowhub/heimdallm/commit/e1b1122d641a34b6301bf6494984db92f8597579))
* app icons from iconset, all UI strings in English, auto-boot flow ([1324981](https://github.com/theburrowhub/heimdallm/commit/1324981751daa5087ff5dc8a53c52776d9575da2))
* arranque automático sin config screen, repos desde PRs activas ([140c20a](https://github.com/theburrowhub/heimdallm/commit/140c20a0fdc0e02c91235c2e238146c7d58f1148))
* auto-save con debounce en Repos y Agents ([4c4ee0d](https://github.com/theburrowhub/heimdallm/commit/4c4ee0ddc69d910fab91d687c9e6176c82ce1e90))
* **config:** settings screen with poll interval, AI selection, retention ([751f323](https://github.com/theburrowhub/heimdallm/commit/751f3234ed32afdd7c987b9f43d533a06835c8fc))
* **config:** token desde gh CLI, descubrimiento de repos, config por-repo ([b3a1c55](https://github.com/theburrowhub/heimdallm/commit/b3a1c55e9f04aeb40a67048be03598473c5dfbb0))
* **config:** TOML config with defaults and per-repo AI overrides ([b3657fc](https://github.com/theburrowhub/heimdallm/commit/b3657fc4e5f1bcc1677845e6d2bd83e55a82fdeb))
* **daemon:** GET /logs/stream SSE endpoint for log tailing ([ef1a118](https://github.com/theburrowhub/heimdallm/commit/ef1a118b23e37e88e6f2edd555b6abb6656714d5))
* **daemon:** main.go wiring + LaunchAgent install/uninstall ([ab0f735](https://github.com/theburrowhub/heimdallm/commit/ab0f735f4ab14fe5adb7a1a58b022956aec037c8))
* **daemon:** size-based rotation for heimdallm.log ([#78](https://github.com/theburrowhub/heimdallm/issues/78)) ([e24a488](https://github.com/theburrowhub/heimdallm/commit/e24a4880ec2471abe213ade0a2b705d4cad8df0d))
* dashboard con 3 tabs (Reviews/Repos/Stats), /me y /stats en daemon ([1cd22a3](https://github.com/theburrowhub/heimdallm/commit/1cd22a3575c1e693d507795c18441de0aaf6575e))
* **dashboard:** PR list with severity badges and review trigger ([62ec38a](https://github.com/theburrowhub/heimdallm/commit/62ec38a3ec01d69eef61f870ed4423a07a6666ca))
* descartar PRs — dismiss/undo en dashboard y detalle ([b08f5c0](https://github.com/theburrowhub/heimdallm/commit/b08f5c0712153cbc72c271548918babf3600239c))
* **discovery:** auto-discover repos by GitHub topic tag ([#41](https://github.com/theburrowhub/heimdallm/issues/41)) ([17f91fc](https://github.com/theburrowhub/heimdallm/commit/17f91fcf5690c7446b0e07514ca775400e4e8633))
* DMG bonito con create-dmg, background personalizable en assets/dmg-background.png ([7fec3b4](https://github.com/theburrowhub/heimdallm/commit/7fec3b4efa1dd47b37ed33a926bae4bc8470ca6d))
* **docker:** web UI service in compose + make setup ([#68](https://github.com/theburrowhub/heimdallm/issues/68)) ([d81b7e8](https://github.com/theburrowhub/heimdallm/commit/d81b7e80c1ed26498c110ae7a07f7a0895d4c199))
* **executor:** add Comments to PRContext with placeholder and append fallback ([1f99180](https://github.com/theburrowhub/heimdallm/commit/1f9918063b9c25256668e2d8392901a6e67dae81))
* **executor:** AI CLI detection, execution, and JSON parsing ([98164a6](https://github.com/theburrowhub/heimdallm/commit/98164a694ba391d094354bb2d9b9e17a155b4316))
* flags Claude avanzados en pestaña Agents — effort, permission-mode, bare, etc. ([6125d3b](https://github.com/theburrowhub/heimdallm/commit/6125d3b0f0625579fbaa556ab206cf517faebd63))
* flags como chips editables + Save con feedback visual en Agents ([59d8541](https://github.com/theburrowhub/heimdallm/commit/59d8541a3bedc650097e5129ef051853f1994bfd))
* Flutter issue tracking views + daemon endpoints ([#28](https://github.com/theburrowhub/heimdallm/issues/28), [#29](https://github.com/theburrowhub/heimdallm/issues/29)) ([#46](https://github.com/theburrowhub/heimdallm/issues/46)) ([af0dcb3](https://github.com/theburrowhub/heimdallm/commit/af0dcb34e4a8ac29c8efcb5fb96bd874a0afec41))
* **flutter:** LogsScreen con SSE streaming y auto-scroll inteligente ([a56b5a4](https://github.com/theburrowhub/heimdallm/commit/a56b5a4198ce78b460038d1db39f8d09da7710ff))
* generación automática de iconos desde source 1024x1024 ([be00756](https://github.com/theburrowhub/heimdallm/commit/be00756739512d7e4bd6525fbd49d02dc80aa3cd))
* **github:** add Comment type and FetchComments for PR discussion context ([93b0247](https://github.com/theburrowhub/heimdallm/commit/93b02479d06163e5263271210f31c40476cd92c2))
* **github:** add SetPRReviewers, AddLabels, SetAssignees methods ([#56](https://github.com/theburrowhub/heimdallm/issues/56)) ([e8c1937](https://github.com/theburrowhub/heimdallm/commit/e8c1937ce9c8514066d37547395e168bea915711))
* **github:** API client for fetching PRs and diffs ([6aca63d](https://github.com/theburrowhub/heimdallm/commit/6aca63d8a5dd67aac7bbc0641cca7db86b2782c8))
* **heimdallr:** first-run setup UI — token Keychain, config desde la app, nombre Heimdallr ([1404c78](https://github.com/theburrowhub/heimdallm/commit/1404c78bf307d539829565f58d8b4b102c2236d5))
* instancia única, hide-to-tray y directorio visible en repo list ([01d6af0](https://github.com/theburrowhub/heimdallm/commit/01d6af0dee72bab0dc5049524554991c878b3858))
* **issues:** auto_implement pipeline — branch, commit, PR ([#45](https://github.com/theburrowhub/heimdallm/issues/45)) ([1186af4](https://github.com/theburrowhub/heimdallm/commit/1186af4e575bbe76c6b1d76feae5693e97fef4a7))
* **issues:** configurable prompts for auto_implement ([#55](https://github.com/theburrowhub/heimdallm/issues/55)) ([#63](https://github.com/theburrowhub/heimdallm/issues/63)) ([28b6a60](https://github.com/theburrowhub/heimdallm/commit/28b6a6005404e8ab106552443c59f76720b3e90b))
* **issues:** configurable prompts for issue triage ([#53](https://github.com/theburrowhub/heimdallm/issues/53)) ([#57](https://github.com/theburrowhub/heimdallm/issues/57)) ([ba2ea6d](https://github.com/theburrowhub/heimdallm/commit/ba2ea6d7e5bddc493810ccd72c1c8d1202a5f757))
* **issues:** GitHub client — FetchIssues with classification & priority sort ([#43](https://github.com/theburrowhub/heimdallm/issues/43)) ([4f9c574](https://github.com/theburrowhub/heimdallm/commit/4f9c574b76d8b20e910945d932220fe003f127d6))
* **issues:** integrate issue polling into daemon cycle ([#28](https://github.com/theburrowhub/heimdallm/issues/28)) ([#50](https://github.com/theburrowhub/heimdallm/issues/50)) ([4acfc25](https://github.com/theburrowhub/heimdallm/commit/4acfc25ff141896f01fb760e6db30d26edf1bdb0))
* **issues:** issue dependencies + auto-promote when blockers close ([#93](https://github.com/theburrowhub/heimdallm/issues/93)) ([6823967](https://github.com/theburrowhub/heimdallm/commit/68239676ab31fdbbd1bcde53119add5a355b82b2))
* **issues:** review_only triage pipeline + fetcher ([#44](https://github.com/theburrowhub/heimdallm/issues/44)) ([14c276f](https://github.com/theburrowhub/heimdallm/commit/14c276f7ba84e0dee55ba6502930248ebf78f4f2))
* **issues:** SQLite schema + config surface for issue tracking ([#42](https://github.com/theburrowhub/heimdallm/issues/42)) ([cad4445](https://github.com/theburrowhub/heimdallm/commit/cad4445823defae6de89dd99eb37c45fe52d41e1))
* **issues:** sub-issues support + promotion robustness (closes [#94](https://github.com/theburrowhub/heimdallm/issues/94), [#97](https://github.com/theburrowhub/heimdallm/issues/97)) ([#98](https://github.com/theburrowhub/heimdallm/issues/98)) ([6831393](https://github.com/theburrowhub/heimdallm/commit/6831393aa96063ba1757580e48bb3bdc8205c5c4))
* **logs:** UI estilo terminal — monospace, colores por nivel, wrap toggle ([631b717](https://github.com/theburrowhub/heimdallm/commit/631b717c7e5e13850dc11dc1e38686ceb3ebf997))
* **main:** app entry point with daemon lifecycle and router ([6a3f539](https://github.com/theburrowhub/heimdallm/commit/6a3f539266c64a0c6be51e75845fb9ef94754ae9))
* make release-local — firma local con Developer ID, notarización y GitHub release ([551e8ae](https://github.com/theburrowhub/heimdallm/commit/551e8aec9ddb34bf0ed3c88b15892f1e02842d12))
* **make:** add `make up-build` for local-source rebuild-and-start ([#89](https://github.com/theburrowhub/heimdallm/issues/89)) ([4b88603](https://github.com/theburrowhub/heimdallm/commit/4b886037145bc9c6ae8c535a29c929daa1cb6364))
* métricas de tiempo de revisión en Stats ([2c6c431](https://github.com/theburrowhub/heimdallm/commit/2c6c43111bcf64602a2126cda692430ff23788e4))
* **models:** PR, Review, Issue, AppConfig with json_serializable ([5e8a2f7](https://github.com/theburrowhub/heimdallm/commit/5e8a2f7141130cea7ae322466b44dc8988be3888))
* modos de revisión configurable — single (un comentario) y multi (un comentario por issue) ([a2564bb](https://github.com/theburrowhub/heimdallm/commit/a2564bb2aee4b472a3866a1a1368c6546f80680c))
* pantalla de error con mensaje claro, hint y botón Retry cuando el daemon no arranca ([84edfe3](https://github.com/theburrowhub/heimdallm/commit/84edfe3677632782f0c2e7c4a8fed2655ef3a330))
* **pipeline:** inject PR comments into AI prompt for full discussion context ([8a39fb7](https://github.com/theburrowhub/heimdallm/commit/8a39fb70b1eee35be536f655210d58c9cd769999))
* **pipeline:** review orchestration with interfaces for testability ([42fba3e](https://github.com/theburrowhub/heimdallm/commit/42fba3e8199613a186fd6ca7213a0e40bc9f023e))
* **pr-detail:** split-panel PR review screen ([32ce5cb](https://github.com/theburrowhub/heimdallm/commit/32ce5cb39d19c866c2f45cdf783d7dae37b9badf))
* prompt por repo en Repositories, PRs ordenadas pendiente→issues→resueltas, secciones colapsables ([6d5e511](https://github.com/theburrowhub/heimdallm/commit/6d5e5110e1639bd0fdf0b48eb05d4b9b873d2bfd))
* **providers:** Riverpod providers for PRs, detail, and config ([c2244b4](https://github.com/theburrowhub/heimdallm/commit/c2244b4274c378e9b6fc3b18e86edf685deacf2a))
* rediseño menú systray — limpio, orientado a acción ([938af0f](https://github.com/theburrowhub/heimdallm/commit/938af0f1dbd6949b5374223b6e5405ae580452b8))
* rename project from Heimdallr to Heimdallm (Heimdall + LLM) ([#35](https://github.com/theburrowhub/heimdallm/issues/35)) ([c3a6de5](https://github.com/theburrowhub/heimdallm/commit/c3a6de53c01ce5bf88b92e5ee9f19187ea9e825a)), closes [#22](https://github.com/theburrowhub/heimdallm/issues/22)
* repos agrupados por org + persistencia de repos no monitored ([1e411ff](https://github.com/theburrowhub/heimdallm/commit/1e411ff7e5d88e23d00ad0ac338a2cf55e5599b1))
* reviews publicadas en GitHub, no re-review si PR no cambió, retry si falla publicación ([6f874ce](https://github.com/theburrowhub/heimdallm/commit/6f874cef0e396c57792fa363bc8e6d7bb7cce379))
* scheduler, macOS notifications, and Keychain token storage ([6e5d50c](https://github.com/theburrowhub/heimdallm/commit/6e5d50cb24d8d9c73e358d6a3d5690adf358ba80))
* selector de ordenación en Reviews — Priority / Newest ([e45a2db](https://github.com/theburrowhub/heimdallm/commit/e45a2dbc91dfdb189fcf9a94cdb31914517472b4))
* **server:** REST handlers and SSE endpoint ([d20bbf2](https://github.com/theburrowhub/heimdallm/commit/d20bbf2434c86716dfb7b30b2c13f8afdf34af38))
* show GitHub review decision badge on PR list + detail ([#101](https://github.com/theburrowhub/heimdallm/issues/101)) ([0ad6eec](https://github.com/theburrowhub/heimdallm/commit/0ad6eecb84b44c77c1b79500c33d0a6e6f9a26bf))
* soporte Linux completo — credenciales, plataforma Flutter, .deb/.rpm/.AppImage ([11e63e9](https://github.com/theburrowhub/heimdallm/commit/11e63e9b477ac2b585839493c5f73b4f72e759f2))
* **sse+daemon:** SSE stream client and daemon lifecycle manager ([9227f8b](https://github.com/theburrowhub/heimdallm/commit/9227f8b45ca00c8f4c923fba81d1cf6303f13cd2))
* **sse:** fan-out SSE broker for real-time events ([ceb5996](https://github.com/theburrowhub/heimdallm/commit/ceb59969eaea9e846d744451e4e5b0facff9276a))
* **store:** SQLite store with PR, review, and config CRUD ([56cedd4](https://github.com/theburrowhub/heimdallm/commit/56cedd415d955866eea4ce321cfee2fd68a94738))
* systray menu rico con My PRs, My Reviews, Repositories, View on GH, Review Now ([1a6f1cc](https://github.com/theburrowhub/heimdallm/commit/1a6f1cc2659f4883b5e4cd7b8e2f470590f5afbf))
* tab Agents con prompts personalizados, fix Repositories (GET /config real), placeholders {title}{diff}etc ([e5a2530](https://github.com/theburrowhub/heimdallm/commit/e5a253015174edf5fc73208431c577700ec513f8))
* tab Agents, directorio local por repo y config por agente CLI ([1fe0c67](https://github.com/theburrowhub/heimdallm/commit/1fe0c67cd20de22a21e2a47e64e77a32cb579f14))
* tab Prompts con presets (Security/Performance/Architecture/etc), instrucciones vs template completo ([e13733d](https://github.com/theburrowhub/heimdallm/commit/e13733d766feda03e0c285783af83a02b2c11bb3))
* unify heimdallr + heimdallr-docker, rename to heimdallm ([#21](https://github.com/theburrowhub/heimdallm/issues/21)) ([e4d3f9d](https://github.com/theburrowhub/heimdallm/commit/e4d3f9d356748373b35838d677459db55db83cd2))
* **web_ui:** dark mode with system / light / dark toggle ([#74](https://github.com/theburrowhub/heimdallm/issues/74)) ([45cc987](https://github.com/theburrowhub/heimdallm/commit/45cc987caedbdbd08ccd94010ad4542d0d4562da))
* **web_ui:** reuse Heimdallm app icon in header + favicon ([#85](https://github.com/theburrowhub/heimdallm/issues/85)) ([53b439e](https://github.com/theburrowhub/heimdallm/commit/53b439e43335199c81d8b9eb81bdfc226c6bd652))
* **web_ui:** scaffold SvelteKit + API/SSE clients ([#30](https://github.com/theburrowhub/heimdallm/issues/30)) ([#60](https://github.com/theburrowhub/heimdallm/issues/60)) ([2ae3049](https://github.com/theburrowhub/heimdallm/commit/2ae30499b67d36cd782419ed1e33e3a37453038e))
* **web-ui:** Config, Agents and Logs routes ([#66](https://github.com/theburrowhub/heimdallm/issues/66)) ([142f87f](https://github.com/theburrowhub/heimdallm/commit/142f87fd1a85154b7ebdd2af0646d99df249fe7d))
* **widgets:** SeverityBadge, Toast, GoRouter, and screen stubs ([de95d43](https://github.com/theburrowhub/heimdallm/commit/de95d43cfd4d7cead531aa44c9a8bd5a382af767))


### Bug Fixes

* abrir en /config cuando daemon no corre, navegar a dashboard tras setup ([bd741f0](https://github.com/theburrowhub/heimdallm/commit/bd741f065d723628efe9e67fc68de2d6bcab56be))
* añadir ValidateExtraFlags a executor (reemplaza PR [#15](https://github.com/theburrowhub/heimdallm/issues/15) con conflicto) ([6d216f0](https://github.com/theburrowhub/heimdallm/commit/6d216f09ec381afbae4968bf1c65af7a373e64e0))
* applicationShouldTerminateAfterLastWindowClosed = false — tray app no sale al cerrar ventana ([7cf48cd](https://github.com/theburrowhub/heimdallm/commit/7cf48cd8cd48e43fd408b9b1af0bdd521275de4e))
* auto-review no disparaba con muchos repos — query GitHub demasiado larga ([5ef747b](https://github.com/theburrowhub/heimdallm/commit/5ef747bfdf8da874273bfdab19806fda9a2e5fd8))
* botón atrás en todas las vistas, notificaciones navegan a PR detail con pr_id ([2fa9e7f](https://github.com/theburrowhub/heimdallm/commit/2fa9e7f687e15035ef334aa6bc15a0b84370b0cd))
* botón atrás siempre visible, fallback a dashboard si no hay stack ([6054783](https://github.com/theburrowhub/heimdallm/commit/6054783eab1b2696d891bece240fa28b0341ecf5))
* botón Review/Re-review, feedback de errores y retención persistente ([8791918](https://github.com/theburrowhub/heimdallm/commit/8791918b3e15bbcab6d938e18e7a2298623e8bd8))
* capturar stdout en errores del executor — claude --bare escribe a stdout ([db8de35](https://github.com/theburrowhub/heimdallm/commit/db8de351efc1ec045f5026dea85ff0616b9c036e))
* **ci:** consolidate build jobs into release.yml ([#65](https://github.com/theburrowhub/heimdallm/issues/65)) ([3c7d1ea](https://github.com/theburrowhub/heimdallm/commit/3c7d1ea653d56d254317021698828fd4afff4cc1))
* claude falla con exit 1 — ejecutar via login shell para heredar ANTHROPIC_API_KEY ([6c8d34e](https://github.com/theburrowhub/heimdallm/commit/6c8d34ed596efab910d192b2521b7f47cb493a93))
* codesign CI preserva entitlements, main() captura errores de init, daemon path con debugPrint ([26595ea](https://github.com/theburrowhub/heimdallm/commit/26595eac37e55dfa8c2db4fa673a8bd200f3a2fa))
* config perdida entre compilaciones — dev-stop mata UI + skip single-instance en debug ([5ce0dee](https://github.com/theburrowhub/heimdallm/commit/5ce0dee25a3119c10f59feaa8b3dd094edec2c4c))
* config save + reload resilience (three linked bugs) ([#100](https://github.com/theburrowhub/heimdallm/issues/100)) ([d018754](https://github.com/theburrowhub/heimdallm/commit/d018754fe15cccdbefde5aca45bcf1d43cae1cd1))
* curly braces en _setError (flutter analyze) ([455b984](https://github.com/theburrowhub/heimdallm/commit/455b9848ebefad244b17c3547e771c64545bec28))
* daemon arranca sin repos, recarga config via POST /reload, dashboard sin panic ([b591612](https://github.com/theburrowhub/heimdallm/commit/b59161209b5f5a5d77d4916386eed99bf0aa9f0a))
* daemon en ProcessStartMode.detached — sobrevive al cierre de Flutter ([e9e3d82](https://github.com/theburrowhub/heimdallm/commit/e9e3d825e807e778fdf657c5f0d2fb340f8481ed))
* **daemon:** accept read-only fields in PUT /config so web UI saves don't 400 ([#87](https://github.com/theburrowhub/heimdallm/issues/87)) ([4a49c96](https://github.com/theburrowhub/heimdallm/commit/4a49c9647f4298c4badcdb66e424473d28329f45)), closes [#86](https://github.com/theburrowhub/heimdallm/issues/86)
* **daemon:** harden store-layer merge — atomic, strict reload, drop server_port ([#82](https://github.com/theburrowhub/heimdallm/issues/82)) ([4cec262](https://github.com/theburrowhub/heimdallm/commit/4cec2629f1c9a54b03d542752b5fce2b16ae8dc8))
* **daemon:** make /logs stream work under Docker ([#76](https://github.com/theburrowhub/heimdallm/issues/76)) ([6fbd26c](https://github.com/theburrowhub/heimdallm/commit/6fbd26c397f80adcfb297f20e7ae76edd8dc2af3))
* **daemon:** make PUT /config persist — third precedence layer (store &gt; env &gt; TOML) ([#80](https://github.com/theburrowhub/heimdallm/issues/80)) ([61f8fa4](https://github.com/theburrowhub/heimdallm/commit/61f8fa4c3851fa9d7b5ac43f8119fb3f13361c18)), closes [#79](https://github.com/theburrowhub/heimdallm/issues/79)
* **daemon:** write api_token with world-readable perms (0644) ([#72](https://github.com/theburrowhub/heimdallm/issues/72)) ([6f8efbe](https://github.com/theburrowhub/heimdallm/commit/6f8efbea00d3c0e9ff5526c269b5c45dfaa027e7)), closes [#71](https://github.com/theburrowhub/heimdallm/issues/71)
* desactivar app sandbox (bloquea Process.start), simplificar make dev ([408596f](https://github.com/theburrowhub/heimdallm/commit/408596fd11931283c85198320c5f906daa59c912))
* dialog prompts usa SizedBox en lugar de ConstrainedBox para evitar unbounded height ([862dde4](https://github.com/theburrowhub/heimdallm/commit/862dde411ce129f3d7d8e67c24cf5b1767a5905a))
* **discovery:** add non_monitored blacklist, org inference, and count drop warning ([#47](https://github.com/theburrowhub/heimdallm/issues/47)) ([320634d](https://github.com/theburrowhub/heimdallm/commit/320634dfc5a23f403b7dff671c02ffd531c2f35c)), closes [#39](https://github.com/theburrowhub/heimdallm/issues/39)
* **docker:** correct web service build context ([5623c52](https://github.com/theburrowhub/heimdallm/commit/5623c52c99301ae6107ae1b6630e849e5d1d56f0))
* **docker:** forward HEIMDALLM_ISSUE_* + HEIMDALLM_DISCOVERY_* env vars ([#96](https://github.com/theburrowhub/heimdallm/issues/96)) ([310c048](https://github.com/theburrowhub/heimdallm/commit/310c048deea31d4505c97545fafbc87637dece00))
* excluir PRs sin repo del dashboard ([dfcc280](https://github.com/theburrowhub/heimdallm/commit/dfcc280ad775f3059bef41a94bc5ce1df9f775a7))
* executor busca CLI en login shell — /opt/homebrew/bin invisible desde GUI ([a811fe3](https://github.com/theburrowhub/heimdallm/commit/a811fe31da00fcdbe078bc811224a14dfc0a3ff7))
* **executor,github:** restore PATH correctly, per-CLI args, proper URL encoding ([72bedc7](https://github.com/theburrowhub/heimdallm/commit/72bedc7cc77ff225b865e0220f6dae5e6695c972))
* **executor:** add opencode CLI support in buildArgs ([#62](https://github.com/theburrowhub/heimdallm/issues/62)) ([8ef0a7b](https://github.com/theburrowhub/heimdallm/commit/8ef0a7b90c72db15ffe544debea1bbd9b3081102))
* FetchPRs sin repos busca todas las PRs del usuario, sin guard vacío ([37a0d9d](https://github.com/theburrowhub/heimdallm/commit/37a0d9d2d359d9c139113661c60ec14d36be0222))
* **flutter:** SSE disconnect closes subscription, triggerReview as Notifier ([0389569](https://github.com/theburrowhub/heimdallm/commit/0389569b5a2eb50a481a72fc040a370a23032ab6))
* gh CLI lookup via login shell y paths hardcodeados, sin validación de repos ([7e4f228](https://github.com/theburrowhub/heimdallm/commit/7e4f22889e768c684b7dff5a8e5eb152ddfefb9c))
* import DaemonLifecycle en main.dart, _daemonBinaryPath delega sin duplicar lógica ([2d8791c](https://github.com/theburrowhub/heimdallm/commit/2d8791ce0e2501681ba0315edeed59059e3c54c9))
* layout tile PR, orden severidades y spinner de revisión en curso ([fc43e9e](https://github.com/theburrowhub/heimdallm/commit/fc43e9ec8b48ba7e49be909eab1ef44461bae7b8))
* Linux taskbar icon not showing on GNOME ([#10](https://github.com/theburrowhub/heimdallm/issues/10)) ([#11](https://github.com/theburrowhub/heimdallm/issues/11)) ([d2a1865](https://github.com/theburrowhub/heimdallm/commit/d2a1865324e5d8f6f8e8ee3f1498647a13a1fbf3))
* **logs:** leer stderr log + header auth correcto en SseClient ([8958f33](https://github.com/theburrowhub/heimdallm/commit/8958f339348659459a315b6f014b542f635723f9))
* **main:** remove dead broker field, stop broker on shutdown, modern launchctl, ParseDuration ([1c8acfe](https://github.com/theburrowhub/heimdallm/commit/1c8acfe96029ea497fabc8aeb5571d52e0102cb6))
* no pasar stdin por el login shell — consumía el prompt enviado a claude ([d0347c7](https://github.com/theburrowhub/heimdallm/commit/d0347c73d54e9c1419d68d61060106e0133b368c))
* nombre Heimdallr en ventana, icono en menubar macOS, splash sin timeout ([d0e3caf](https://github.com/theburrowhub/heimdallm/commit/d0e3caf83a53302f3eb6e562d9a797b21a948d1e))
* normalizar strings vacíos a null en repo_overrides del config ([4cd53de](https://github.com/theburrowhub/heimdallm/commit/4cd53de2aeb02c3a0b3c618060dfb93561ab109d))
* notificaciones solo desde Flutter, no parpadeo, layout responsive, Review funciona, Open on GitHub ([8b939a3](https://github.com/theburrowhub/heimdallm/commit/8b939a3d4827a4f9cf093340593efc7072dad08b))
* overflow en preset cards y stats bar chart ([0a75128](https://github.com/theburrowhub/heimdallm/commit/0a7512876692a69f08072bbb70a14e1b57cb219e))
* platform-agnostic daemon log path in /logs/stream ([f58578b](https://github.com/theburrowhub/heimdallm/commit/f58578b98b47d7b21228dacd786fba88484c4dc3))
* PRs con repo vacío — botón deshabilitado, aviso y SSE error propagado ([3987f2e](https://github.com/theburrowhub/heimdallm/commit/3987f2ee0feed5972014bd5521c45564161d14b5))
* PRs ordenadas por fecha desc dentro de cada nivel de severidad ([9738112](https://github.com/theburrowhub/heimdallm/commit/9738112a266397c91361e120d027e43dcac24ec2))
* queries GitHub separadas por qualifier (no OR), resolve repo desde repository_url ([204eb5b](https://github.com/theburrowhub/heimdallm/commit/204eb5baa66155b81dc9547b1ba46d5ccbb9fbbd))
* re-review loop — gracia de 30s para el updated_at que GitHub cambia al postear review ([9e8006c](https://github.com/theburrowhub/heimdallm/commit/9e8006ce2883ae192457a742ef8f53424a394742))
* re-reviews no longer repeat false positives — structured review cycle context ([#92](https://github.com/theburrowhub/heimdallm/issues/92)) ([1cc96b5](https://github.com/theburrowhub/heimdallm/commit/1cc96b5ac480fe6e0132db074a9b8534387dec78))
* renombrar rutas auto-pr → heimdallr en logs y LaunchAgent ([4c13332](https://github.com/theburrowhub/heimdallm/commit/4c13332377552bfbe8e48e6c84fb833306db7ef2))
* renombrar todo a Heimdallr, make dev completo para pruebas locales ([925d267](https://github.com/theburrowhub/heimdallm/commit/925d2679fb915785084b763f75e8b6c57e2ff143))
* retention no se guardaba + SnackBar permanente en dismiss ([2f009ad](https://github.com/theburrowhub/heimdallm/commit/2f009ad4d36eeb568a52b9a50dd5eeead50540bb))
* reviews en goroutine — poll loop no bloquea durante análisis largos ([81b937d](https://github.com/theburrowhub/heimdallm/commit/81b937d2cbefcb1b53a0555d1e3a002733eebd55))
* scheduler safe double-stop, SSE unsubscribe non-blocking after shutdown ([8e7c02f](https://github.com/theburrowhub/heimdallm/commit/8e7c02f9d00e23ca6d353d816cd880d1a24327a0))
* **security:** abort startup if API token cannot be created ([f1cd314](https://github.com/theburrowhub/heimdallm/commit/f1cd314e63ac597b50fb409c0c9605c7162a8054))
* **security:** allowlist CLI names + positional arg in resolveCLIPath (issue [#2](https://github.com/theburrowhub/heimdallm/issues/2)) ([#8](https://github.com/theburrowhub/heimdallm/issues/8)) ([ac1d5d4](https://github.com/theburrowhub/heimdallm/commit/ac1d5d40871b665835a4dcc21c71505730b07235))
* **security:** authenticate mutating API endpoints with per-daemon token (issue [#3](https://github.com/theburrowhub/heimdallm/issues/3)) ([#9](https://github.com/theburrowhub/heimdallm/issues/9)) ([7a73e03](https://github.com/theburrowhub/heimdallm/commit/7a73e031eca1ce6b6b4894cf17aaa23fb20f1a0e))
* **security:** block additional credential directories from AI workdir ([c9b54a5](https://github.com/theburrowhub/heimdallm/commit/c9b54a5ef17827b0feed8e52936bf11225578293))
* **security:** catch --flag=value bypass in ValidateExtraFlags, block --permission-mode ([#18](https://github.com/theburrowhub/heimdallm/issues/18)) ([fbc59f5](https://github.com/theburrowhub/heimdallm/commit/fbc59f5cf5bcbabfd12e9b9b86a218eb722c46ea))
* **security:** corregir 3 vulnerabilidades detectadas en auditoría ([#20](https://github.com/theburrowhub/heimdallm/issues/20)) ([94ce891](https://github.com/theburrowhub/heimdallm/commit/94ce891d936a4ca9d5e7ad749a15cdf5f97a75ca))
* **security:** corregir vulnerabilidades backend daemon (C-1, C-2, A-1, A-2, A-3, M-1, M-2, M-4, M-5, B-1) ([c8a15ab](https://github.com/theburrowhub/heimdallm/commit/c8a15ab8478e45e61f66283fa66bcc1935c5ccb5))
* **security:** corregir vulnerabilidades Flutter frontend (C-3, C-4, A-7, M-9, B-3) ([6d6d619](https://github.com/theburrowhub/heimdallm/commit/6d6d6191f8b6b90be46139bfec835c4748b4f63f))
* **security:** corregir vulnerabilidades infra (A-4, A-5, A-6, M-3, M-6, M-7, M-8) ([e079e53](https://github.com/theburrowhub/heimdallm/commit/e079e534a8ad57ceadb5103ec409c899ce1d8e0f))
* **security:** escape user strings in TOML config generation (issue [#6](https://github.com/theburrowhub/heimdallm/issues/6)) ([#14](https://github.com/theburrowhub/heimdallm/issues/14)) ([7acb643](https://github.com/theburrowhub/heimdallm/commit/7acb64377522ad81d48ba3079a87424437074d5b))
* **security:** limit concurrent review triggers with counting semaphore ([3befdfe](https://github.com/theburrowhub/heimdallm/commit/3befdfe9d5f755640ccbbc5d12ea34c36daedefb))
* **security:** protect cachedLogin with mutex to eliminate data race ([d47adbc](https://github.com/theburrowhub/heimdallm/commit/d47adbc6154b69f2e72d100db01f489d3e2f353f))
* **security:** remove CORS wildcard from SSE endpoint (issue [#4](https://github.com/theburrowhub/heimdallm/issues/4)) ([#12](https://github.com/theburrowhub/heimdallm/issues/12)) ([65ce4b1](https://github.com/theburrowhub/heimdallm/commit/65ce4b1b4fb05815dfa352481733ae9e5ecc8040))
* **security:** require auth on GET /config and GET /agents; allowlist PUT /config keys ([#19](https://github.com/theburrowhub/heimdallm/issues/19)) ([7d72a86](https://github.com/theburrowhub/heimdallm/commit/7d72a8613b1a61acdcca5b43f3352527992bb738))
* **security:** require auth token on /me, /prs, /stats endpoints ([2eb611a](https://github.com/theburrowhub/heimdallm/commit/2eb611a40aed083337f58d0f60a394debb63c253))
* **security:** resolve symlinks in ValidateWorkDir before applying denylist ([#17](https://github.com/theburrowhub/heimdallm/issues/17)) ([a33e400](https://github.com/theburrowhub/heimdallm/commit/a33e4006deea72aa0a3afca4803ba02e82c4fb04))
* **security:** use json.Marshal instead of fmt.Sprintf for SSE event JSON ([3657923](https://github.com/theburrowhub/heimdallm/commit/3657923109c6b2c0146649eeb41fb7043ec54d36))
* **security:** validate config values in PUT /config ([406a4b0](https://github.com/theburrowhub/heimdallm/commit/406a4b04e7203441ce6c71676df4f7ca16e89775))
* **security:** validate WorkDir before setting as AI CLI working directory ([#13](https://github.com/theburrowhub/heimdallm/issues/13)) ([750abd8](https://github.com/theburrowhub/heimdallm/commit/750abd8bd9a266db7a122cea2c562e31175e9e97))
* setPreventClose desde postFrameCallback — garantiza que el window está listo ([b383daf](https://github.com/theburrowhub/heimdallm/commit/b383dafacf03011cf076057026346efdfccafb9d))
* show() directo en setupWindow — waitUntilReadyToShow no dispara en bundles de producción ([51b0142](https://github.com/theburrowhub/heimdallm/commit/51b01423812181de3128fa1909a543b9098327e3))
* simplificar detección de token gh CLI, eliminar doble llamada ([cef3738](https://github.com/theburrowhub/heimdallm/commit/cef3738d869e30d66621d7df3a653ea87d674ce8))
* single instance en todos los modos — check activo en debug + dev-stop más agresivo ([676ab7a](https://github.com/theburrowhub/heimdallm/commit/676ab7a0223889c54089ed0720d1b4865b4380dc))
* single instance via PID file — funciona en debug y producción ([c22eb21](https://github.com/theburrowhub/heimdallm/commit/c22eb21d7213cb4ec64799b5b573813efea7b708))
* single instance vía SIGUSR1 — la app lo gestiona sin depender del OS ni del Makefile ([4f72082](https://github.com/theburrowhub/heimdallm/commit/4f720822420c22c0268bf91886809e9a78c8ef3d))
* SnackBar dismiss — showCloseIcon + duration explícito en todos los snackbars ([29d9501](https://github.com/theburrowhub/heimdallm/commit/29d9501bd7be01b69d36627bd5d0060636d3b408))
* solo high severity bloquea (REQUEST_CHANGES), todo lo demás aprueba (APPROVE) ([32db204](https://github.com/theburrowhub/heimdallm/commit/32db204ba97b8a5ce8fba92eef8d7a3bf01c1a72))
* splash infinita hasta que daemon responde, sin fallback a Settings si hay token ([24e8ced](https://github.com/theburrowhub/heimdallm/commit/24e8ced64c6656643090d74a23f41cf372c8b2ea))
* SSE reconnect + sort persistente en Reviews ([40e13a5](https://github.com/theburrowhub/heimdallm/commit/40e13a599ac45d90749db449b1a6810b5b2860b7))
* **sse:** close subscriber channels on broker Stop() ([199c478](https://github.com/theburrowhub/heimdallm/commit/199c4780fa8c816b62b964af7744ceeb2883b63c))
* **sse:** enviar token de auth en SseClient — corrige 401 en /events y /logs/stream ([d6227be](https://github.com/theburrowhub/heimdallm/commit/d6227be73b61fbc885fd49b33719d2fc311ee11e))
* **store:** add json tags to PR and Review structs for snake_case wire format ([76a60ed](https://github.com/theburrowhub/heimdallm/commit/76a60eda55a2bed1380987a7ae09309a7b211f58))
* **store:** use SQLite datetime format, propagate time parse errors ([d121755](https://github.com/theburrowhub/heimdallm/commit/d1217553793de000625f24fefc0c295cdc7fd879))
* sudo gem install fpm — permisos en Ubuntu CI ([8c2e757](https://github.com/theburrowhub/heimdallm/commit/8c2e757c3059f3cc5471decd0d36166822cdcd3a))
* tamaño ventana, notificaciones Flutter (icono correcto + click abre PR), botón atrás ([b032ac5](https://github.com/theburrowhub/heimdallm/commit/b032ac51a671cfaf060880af2c311d2969df1d10))
* test config_test actualizado (claude movido a Agents tab) + rpm en lugar de rpm-build en CI ([9e616f6](https://github.com/theburrowhub/heimdallm/commit/9e616f6b7187c409c6d2b7ed080b2d195f218ce5))
* tray mostraba mis PRs cuando me aún no cargó — prsProvider depende de meProvider ([496d3e3](https://github.com/theburrowhub/heimdallm/commit/496d3e3fdef9f710c31f19a31dcd9b611e11a04d))
* validar cli_flags de perfiles de prompt contra denylist antes de ejecutar ([faa8862](https://github.com/theburrowhub/heimdallm/commit/faa8862fa6774e3b8ff5804bb433fd11094a05a1))


### Documentation

* actualizar README, LLM guide y GitHub Pages para Linux y modos de revisión ([feab4b5](https://github.com/theburrowhub/heimdallm/commit/feab4b587cfa6bcd476c8b2247b2a225373bb1f2))
* add auto-pr design spec ([42cc246](https://github.com/theburrowhub/heimdallm/commit/42cc2466765a4e785838803ab1c13c64029c5820))
* add daemon and Flutter implementation plans ([75ba4bd](https://github.com/theburrowhub/heimdallm/commit/75ba4bd61a598a6693cb6c3b155fc4e3dbc4657d))
* entitlements embebidos en LLM guide (sin depender del repo), hdiutil detach robusto ([508b8df](https://github.com/theburrowhub/heimdallm/commit/508b8df14839069328cdac69d062ca0a575558e0))
* Heimdallm v2 design spec — issue tracking, rename, web UI ([43b4cb6](https://github.com/theburrowhub/heimdallm/commit/43b4cb6905b01f3cb778812e0f065c30a520714f))
* plan PR comments context injection ([ef214d1](https://github.com/theburrowhub/heimdallm/commit/ef214d1d0ac57449103f84308887273ae73b77c4))
* README, LLM-HOW-TO-INSTALL y GitHub Pages ([3e15bdc](https://github.com/theburrowhub/heimdallm/commit/3e15bdcc83955b6ddcbc311d1c85a7ad923389af))
* **readme:** cover three install gotchas that tripped a real user ([d8b16e9](https://github.com/theburrowhub/heimdallm/commit/d8b16e9eeb72d5d20348abf915f96f215118db75))
* **readme:** document issue pipeline, web UI, and topic discovery ([e03f3cb](https://github.com/theburrowhub/heimdallm/commit/e03f3cb05c7747acb3a1f0fffba1d1d148d26fd4))
* **readme:** document reusing host AI auth inside Docker ([f0bfe49](https://github.com/theburrowhub/heimdallm/commit/f0bfe49580990b2dfa4cf14cb0cbbc9230f151b6))
* **readme:** promote Claude OAuth path to first-class instructions ([35ec2e5](https://github.com/theburrowhub/heimdallm/commit/35ec2e522dd722daa8e4635312edf174c23b9eff))
* simplificar LLM guide — solo descargar, instalar, permitir uso ([9f5cea0](https://github.com/theburrowhub/heimdallm/commit/9f5cea00903378a0f68afb14316bb75f8d578ad5))
* spec PR comments context injection ([86a61b0](https://github.com/theburrowhub/heimdallm/commit/86a61b06c39eb6bf95a63fecc566dbf054604f4e))
* update documentation post-consolidation and rename to Heimdallm ([#37](https://github.com/theburrowhub/heimdallm/issues/37)) ([032398d](https://github.com/theburrowhub/heimdallm/commit/032398dfdd38583ef776eed2fbdee7a558fcfe0b))

## [0.2.0](https://github.com/theburrowhub/heimdallm/compare/v0.1.8...v0.2.0) (2026-04-20)


### ⚠ BREAKING CHANGES

* the `./config:/config` bind mount is replaced by a `heimdallm-config:/config` named volume. Operators with a customised `docker/config/config.toml` must copy it into the new volume before upgrading or the daemon will regenerate the file from env vars:

### Bug Fixes

* config save + reload resilience (three linked bugs) ([#100](https://github.com/theburrowhub/heimdallm/issues/100)) ([d018754](https://github.com/theburrowhub/heimdallm/commit/d018754fe15cccdbefde5aca45bcf1d43cae1cd1))

## [0.1.8](https://github.com/theburrowhub/heimdallm/compare/v0.1.7...v0.1.8) (2026-04-20)


### Features

* **issues:** issue dependencies + auto-promote when blockers close ([#93](https://github.com/theburrowhub/heimdallm/issues/93)) ([6823967](https://github.com/theburrowhub/heimdallm/commit/68239676ab31fdbbd1bcde53119add5a355b82b2))
* **issues:** sub-issues support + promotion robustness (closes [#94](https://github.com/theburrowhub/heimdallm/issues/94), [#97](https://github.com/theburrowhub/heimdallm/issues/97)) ([#98](https://github.com/theburrowhub/heimdallm/issues/98)) ([6831393](https://github.com/theburrowhub/heimdallm/commit/6831393aa96063ba1757580e48bb3bdc8205c5c4))
* **make:** add `make up-build` for local-source rebuild-and-start ([#89](https://github.com/theburrowhub/heimdallm/issues/89)) ([4b88603](https://github.com/theburrowhub/heimdallm/commit/4b886037145bc9c6ae8c535a29c929daa1cb6364))
* show GitHub review decision badge on PR list + detail ([#101](https://github.com/theburrowhub/heimdallm/issues/101)) ([0ad6eec](https://github.com/theburrowhub/heimdallm/commit/0ad6eecb84b44c77c1b79500c33d0a6e6f9a26bf))
* **web_ui:** reuse Heimdallm app icon in header + favicon ([#85](https://github.com/theburrowhub/heimdallm/issues/85)) ([53b439e](https://github.com/theburrowhub/heimdallm/commit/53b439e43335199c81d8b9eb81bdfc226c6bd652))


### Bug Fixes

* **ci:** consolidate build jobs into release.yml ([#65](https://github.com/theburrowhub/heimdallm/issues/65)) ([3c7d1ea](https://github.com/theburrowhub/heimdallm/commit/3c7d1ea653d56d254317021698828fd4afff4cc1))
* **daemon:** accept read-only fields in PUT /config so web UI saves don't 400 ([#87](https://github.com/theburrowhub/heimdallm/issues/87)) ([4a49c96](https://github.com/theburrowhub/heimdallm/commit/4a49c9647f4298c4badcdb66e424473d28329f45)), closes [#86](https://github.com/theburrowhub/heimdallm/issues/86)
* **daemon:** harden store-layer merge — atomic, strict reload, drop server_port ([#82](https://github.com/theburrowhub/heimdallm/issues/82)) ([4cec262](https://github.com/theburrowhub/heimdallm/commit/4cec2629f1c9a54b03d542752b5fce2b16ae8dc8))
* **docker:** forward HEIMDALLM_ISSUE_* + HEIMDALLM_DISCOVERY_* env vars ([#96](https://github.com/theburrowhub/heimdallm/issues/96)) ([310c048](https://github.com/theburrowhub/heimdallm/commit/310c048deea31d4505c97545fafbc87637dece00))
* re-reviews no longer repeat false positives — structured review cycle context ([#92](https://github.com/theburrowhub/heimdallm/issues/92)) ([1cc96b5](https://github.com/theburrowhub/heimdallm/commit/1cc96b5ac480fe6e0132db074a9b8534387dec78))

## [0.1.7](https://github.com/theburrowhub/heimdallm/compare/v0.1.6...v0.1.7) (2026-04-20)


### Features

* **daemon:** size-based rotation for heimdallm.log ([#78](https://github.com/theburrowhub/heimdallm/issues/78)) ([e24a488](https://github.com/theburrowhub/heimdallm/commit/e24a4880ec2471abe213ade0a2b705d4cad8df0d))
* **docker:** web UI service in compose + make setup ([#68](https://github.com/theburrowhub/heimdallm/issues/68)) ([d81b7e8](https://github.com/theburrowhub/heimdallm/commit/d81b7e80c1ed26498c110ae7a07f7a0895d4c199))
* **web_ui:** dark mode with system / light / dark toggle ([#74](https://github.com/theburrowhub/heimdallm/issues/74)) ([45cc987](https://github.com/theburrowhub/heimdallm/commit/45cc987caedbdbd08ccd94010ad4542d0d4562da))
* **web-ui:** Config, Agents and Logs routes ([#66](https://github.com/theburrowhub/heimdallm/issues/66)) ([142f87f](https://github.com/theburrowhub/heimdallm/commit/142f87fd1a85154b7ebdd2af0646d99df249fe7d))


### Bug Fixes

* **daemon:** make /logs stream work under Docker ([#76](https://github.com/theburrowhub/heimdallm/issues/76)) ([6fbd26c](https://github.com/theburrowhub/heimdallm/commit/6fbd26c397f80adcfb297f20e7ae76edd8dc2af3))
* **daemon:** make PUT /config persist — third precedence layer (store &gt; env &gt; TOML) ([#80](https://github.com/theburrowhub/heimdallm/issues/80)) ([61f8fa4](https://github.com/theburrowhub/heimdallm/commit/61f8fa4c3851fa9d7b5ac43f8119fb3f13361c18)), closes [#79](https://github.com/theburrowhub/heimdallm/issues/79)
* **daemon:** write api_token with world-readable perms (0644) ([#72](https://github.com/theburrowhub/heimdallm/issues/72)) ([6f8efbe](https://github.com/theburrowhub/heimdallm/commit/6f8efbea00d3c0e9ff5526c269b5c45dfaa027e7)), closes [#71](https://github.com/theburrowhub/heimdallm/issues/71)
* **docker:** correct web service build context ([5623c52](https://github.com/theburrowhub/heimdallm/commit/5623c52c99301ae6107ae1b6630e849e5d1d56f0))


### Documentation

* **readme:** cover three install gotchas that tripped a real user ([d8b16e9](https://github.com/theburrowhub/heimdallm/commit/d8b16e9eeb72d5d20348abf915f96f215118db75))
* **readme:** document issue pipeline, web UI, and topic discovery ([e03f3cb](https://github.com/theburrowhub/heimdallm/commit/e03f3cb05c7747acb3a1f0fffba1d1d148d26fd4))
* **readme:** document reusing host AI auth inside Docker ([f0bfe49](https://github.com/theburrowhub/heimdallm/commit/f0bfe49580990b2dfa4cf14cb0cbbc9230f151b6))
* **readme:** promote Claude OAuth path to first-class instructions ([35ec2e5](https://github.com/theburrowhub/heimdallm/commit/35ec2e522dd722daa8e4635312edf174c23b9eff))

## [0.1.6](https://github.com/theburrowhub/heimdallm/compare/v0.1.5...v0.1.6) (2026-04-19)


### Features

* **github:** add SetPRReviewers, AddLabels, SetAssignees methods ([#56](https://github.com/theburrowhub/heimdallm/issues/56)) ([e8c1937](https://github.com/theburrowhub/heimdallm/commit/e8c1937ce9c8514066d37547395e168bea915711))
* **web_ui:** scaffold SvelteKit + API/SSE clients ([#30](https://github.com/theburrowhub/heimdallm/issues/30)) ([#60](https://github.com/theburrowhub/heimdallm/issues/60)) ([2ae3049](https://github.com/theburrowhub/heimdallm/commit/2ae30499b67d36cd782419ed1e33e3a37453038e))

## [0.1.5](https://github.com/theburrowhub/heimdallm/compare/v0.1.4...v0.1.5) (2026-04-18)


### Features

* **issues:** configurable prompts for auto_implement ([#55](https://github.com/theburrowhub/heimdallm/issues/55)) ([#63](https://github.com/theburrowhub/heimdallm/issues/63)) ([28b6a60](https://github.com/theburrowhub/heimdallm/commit/28b6a6005404e8ab106552443c59f76720b3e90b))
* **issues:** configurable prompts for issue triage ([#53](https://github.com/theburrowhub/heimdallm/issues/53)) ([#57](https://github.com/theburrowhub/heimdallm/issues/57)) ([ba2ea6d](https://github.com/theburrowhub/heimdallm/commit/ba2ea6d7e5bddc493810ccd72c1c8d1202a5f757))


### Bug Fixes

* **executor:** add opencode CLI support in buildArgs ([#62](https://github.com/theburrowhub/heimdallm/issues/62)) ([8ef0a7b](https://github.com/theburrowhub/heimdallm/commit/8ef0a7b90c72db15ffe544debea1bbd9b3081102))

## [0.1.4](https://github.com/theburrowhub/heimdallm/compare/v0.1.3...v0.1.4) (2026-04-17)


### Features

* **issues:** integrate issue polling into daemon cycle ([#28](https://github.com/theburrowhub/heimdallm/issues/28)) ([#50](https://github.com/theburrowhub/heimdallm/issues/50)) ([4acfc25](https://github.com/theburrowhub/heimdallm/commit/4acfc25ff141896f01fb760e6db30d26edf1bdb0))

## [0.1.3](https://github.com/theburrowhub/heimdallm/compare/v0.1.2...v0.1.3) (2026-04-17)


### Features

* add make run-linux target for Docker-based GUI testing ([#48](https://github.com/theburrowhub/heimdallm/issues/48)) ([6c3f66d](https://github.com/theburrowhub/heimdallm/commit/6c3f66dcb1ffec758c1033207f6836ff5f1bf3dd)), closes [#16](https://github.com/theburrowhub/heimdallm/issues/16)
* **issues:** auto_implement pipeline — branch, commit, PR ([#45](https://github.com/theburrowhub/heimdallm/issues/45)) ([1186af4](https://github.com/theburrowhub/heimdallm/commit/1186af4e575bbe76c6b1d76feae5693e97fef4a7))


### Bug Fixes

* **discovery:** add non_monitored blacklist, org inference, and count drop warning ([#47](https://github.com/theburrowhub/heimdallm/issues/47)) ([320634d](https://github.com/theburrowhub/heimdallm/commit/320634dfc5a23f403b7dff671c02ffd531c2f35c)), closes [#39](https://github.com/theburrowhub/heimdallm/issues/39)

## [0.1.2](https://github.com/theburrowhub/heimdallm/compare/v0.1.1...v0.1.2) (2026-04-17)


### Features

* **discovery:** auto-discover repos by GitHub topic tag ([#41](https://github.com/theburrowhub/heimdallm/issues/41)) ([17f91fc](https://github.com/theburrowhub/heimdallm/commit/17f91fcf5690c7446b0e07514ca775400e4e8633))
* **issues:** GitHub client — FetchIssues with classification & priority sort ([#43](https://github.com/theburrowhub/heimdallm/issues/43)) ([4f9c574](https://github.com/theburrowhub/heimdallm/commit/4f9c574b76d8b20e910945d932220fe003f127d6))
* **issues:** review_only triage pipeline + fetcher ([#44](https://github.com/theburrowhub/heimdallm/issues/44)) ([14c276f](https://github.com/theburrowhub/heimdallm/commit/14c276f7ba84e0dee55ba6502930248ebf78f4f2))
* **issues:** SQLite schema + config surface for issue tracking ([#42](https://github.com/theburrowhub/heimdallm/issues/42)) ([cad4445](https://github.com/theburrowhub/heimdallm/commit/cad4445823defae6de89dd99eb37c45fe52d41e1))


### Documentation

* update documentation post-consolidation and rename to Heimdallm ([#37](https://github.com/theburrowhub/heimdallm/issues/37)) ([032398d](https://github.com/theburrowhub/heimdallm/commit/032398dfdd38583ef776eed2fbdee7a558fcfe0b))

## [0.1.1](https://github.com/theburrowhub/heimdallm/compare/v0.1.0...v0.1.1) (2026-04-16)


### Features

* auto-save con debounce en Repos y Agents ([4c4ee0d](https://github.com/theburrowhub/heimdallm/commit/4c4ee0ddc69d910fab91d687c9e6176c82ce1e90))
* **daemon:** GET /logs/stream SSE endpoint for log tailing ([ef1a118](https://github.com/theburrowhub/heimdallm/commit/ef1a118b23e37e88e6f2edd555b6abb6656714d5))
* descartar PRs — dismiss/undo en dashboard y detalle ([b08f5c0](https://github.com/theburrowhub/heimdallm/commit/b08f5c0712153cbc72c271548918babf3600239c))
* **executor:** add Comments to PRContext with placeholder and append fallback ([1f99180](https://github.com/theburrowhub/heimdallm/commit/1f9918063b9c25256668e2d8392901a6e67dae81))
* flags Claude avanzados en pestaña Agents — effort, permission-mode, bare, etc. ([6125d3b](https://github.com/theburrowhub/heimdallm/commit/6125d3b0f0625579fbaa556ab206cf517faebd63))
* flags como chips editables + Save con feedback visual en Agents ([59d8541](https://github.com/theburrowhub/heimdallm/commit/59d8541a3bedc650097e5129ef051853f1994bfd))
* **flutter:** LogsScreen con SSE streaming y auto-scroll inteligente ([a56b5a4](https://github.com/theburrowhub/heimdallm/commit/a56b5a4198ce78b460038d1db39f8d09da7710ff))
* generación automática de iconos desde source 1024x1024 ([be00756](https://github.com/theburrowhub/heimdallm/commit/be00756739512d7e4bd6525fbd49d02dc80aa3cd))
* **github:** add Comment type and FetchComments for PR discussion context ([93b0247](https://github.com/theburrowhub/heimdallm/commit/93b02479d06163e5263271210f31c40476cd92c2))
* instancia única, hide-to-tray y directorio visible en repo list ([01d6af0](https://github.com/theburrowhub/heimdallm/commit/01d6af0dee72bab0dc5049524554991c878b3858))
* **logs:** UI estilo terminal — monospace, colores por nivel, wrap toggle ([631b717](https://github.com/theburrowhub/heimdallm/commit/631b717c7e5e13850dc11dc1e38686ceb3ebf997))
* métricas de tiempo de revisión en Stats ([2c6c431](https://github.com/theburrowhub/heimdallm/commit/2c6c43111bcf64602a2126cda692430ff23788e4))
* modos de revisión configurable — single (un comentario) y multi (un comentario por issue) ([a2564bb](https://github.com/theburrowhub/heimdallm/commit/a2564bb2aee4b472a3866a1a1368c6546f80680c))
* **pipeline:** inject PR comments into AI prompt for full discussion context ([8a39fb7](https://github.com/theburrowhub/heimdallm/commit/8a39fb70b1eee35be536f655210d58c9cd769999))
* rediseño menú systray — limpio, orientado a acción ([938af0f](https://github.com/theburrowhub/heimdallm/commit/938af0f1dbd6949b5374223b6e5405ae580452b8))
* rename project from Heimdallr to Heimdallm (Heimdall + LLM) ([#35](https://github.com/theburrowhub/heimdallm/issues/35)) ([c3a6de5](https://github.com/theburrowhub/heimdallm/commit/c3a6de53c01ce5bf88b92e5ee9f19187ea9e825a)), closes [#22](https://github.com/theburrowhub/heimdallm/issues/22)
* repos agrupados por org + persistencia de repos no monitored ([1e411ff](https://github.com/theburrowhub/heimdallm/commit/1e411ff7e5d88e23d00ad0ac338a2cf55e5599b1))
* selector de ordenación en Reviews — Priority / Newest ([e45a2db](https://github.com/theburrowhub/heimdallm/commit/e45a2dbc91dfdb189fcf9a94cdb31914517472b4))
* soporte Linux completo — credenciales, plataforma Flutter, .deb/.rpm/.AppImage ([11e63e9](https://github.com/theburrowhub/heimdallm/commit/11e63e9b477ac2b585839493c5f73b4f72e759f2))
* tab Agents, directorio local por repo y config por agente CLI ([1fe0c67](https://github.com/theburrowhub/heimdallm/commit/1fe0c67cd20de22a21e2a47e64e77a32cb579f14))
* unify heimdallr + heimdallr-docker, rename to heimdallm ([#21](https://github.com/theburrowhub/heimdallm/issues/21)) ([e4d3f9d](https://github.com/theburrowhub/heimdallm/commit/e4d3f9d356748373b35838d677459db55db83cd2))


### Bug Fixes

* añadir ValidateExtraFlags a executor (reemplaza PR [#15](https://github.com/theburrowhub/heimdallm/issues/15) con conflicto) ([6d216f0](https://github.com/theburrowhub/heimdallm/commit/6d216f09ec381afbae4968bf1c65af7a373e64e0))
* applicationShouldTerminateAfterLastWindowClosed = false — tray app no sale al cerrar ventana ([7cf48cd](https://github.com/theburrowhub/heimdallm/commit/7cf48cd8cd48e43fd408b9b1af0bdd521275de4e))
* auto-review no disparaba con muchos repos — query GitHub demasiado larga ([5ef747b](https://github.com/theburrowhub/heimdallm/commit/5ef747bfdf8da874273bfdab19806fda9a2e5fd8))
* botón Review/Re-review, feedback de errores y retención persistente ([8791918](https://github.com/theburrowhub/heimdallm/commit/8791918b3e15bbcab6d938e18e7a2298623e8bd8))
* capturar stdout en errores del executor — claude --bare escribe a stdout ([db8de35](https://github.com/theburrowhub/heimdallm/commit/db8de351efc1ec045f5026dea85ff0616b9c036e))
* claude falla con exit 1 — ejecutar via login shell para heredar ANTHROPIC_API_KEY ([6c8d34e](https://github.com/theburrowhub/heimdallm/commit/6c8d34ed596efab910d192b2521b7f47cb493a93))
* config perdida entre compilaciones — dev-stop mata UI + skip single-instance en debug ([5ce0dee](https://github.com/theburrowhub/heimdallm/commit/5ce0dee25a3119c10f59feaa8b3dd094edec2c4c))
* daemon en ProcessStartMode.detached — sobrevive al cierre de Flutter ([e9e3d82](https://github.com/theburrowhub/heimdallm/commit/e9e3d825e807e778fdf657c5f0d2fb340f8481ed))
* excluir PRs sin repo del dashboard ([dfcc280](https://github.com/theburrowhub/heimdallm/commit/dfcc280ad775f3059bef41a94bc5ce1df9f775a7))
* executor busca CLI en login shell — /opt/homebrew/bin invisible desde GUI ([a811fe3](https://github.com/theburrowhub/heimdallm/commit/a811fe31da00fcdbe078bc811224a14dfc0a3ff7))
* layout tile PR, orden severidades y spinner de revisión en curso ([fc43e9e](https://github.com/theburrowhub/heimdallm/commit/fc43e9ec8b48ba7e49be909eab1ef44461bae7b8))
* Linux taskbar icon not showing on GNOME ([#10](https://github.com/theburrowhub/heimdallm/issues/10)) ([#11](https://github.com/theburrowhub/heimdallm/issues/11)) ([d2a1865](https://github.com/theburrowhub/heimdallm/commit/d2a1865324e5d8f6f8e8ee3f1498647a13a1fbf3))
* **logs:** leer stderr log + header auth correcto en SseClient ([8958f33](https://github.com/theburrowhub/heimdallm/commit/8958f339348659459a315b6f014b542f635723f9))
* no pasar stdin por el login shell — consumía el prompt enviado a claude ([d0347c7](https://github.com/theburrowhub/heimdallm/commit/d0347c73d54e9c1419d68d61060106e0133b368c))
* normalizar strings vacíos a null en repo_overrides del config ([4cd53de](https://github.com/theburrowhub/heimdallm/commit/4cd53de2aeb02c3a0b3c618060dfb93561ab109d))
* platform-agnostic daemon log path in /logs/stream ([f58578b](https://github.com/theburrowhub/heimdallm/commit/f58578b98b47d7b21228dacd786fba88484c4dc3))
* PRs con repo vacío — botón deshabilitado, aviso y SSE error propagado ([3987f2e](https://github.com/theburrowhub/heimdallm/commit/3987f2ee0feed5972014bd5521c45564161d14b5))
* PRs ordenadas por fecha desc dentro de cada nivel de severidad ([9738112](https://github.com/theburrowhub/heimdallm/commit/9738112a266397c91361e120d027e43dcac24ec2))
* re-review loop — gracia de 30s para el updated_at que GitHub cambia al postear review ([9e8006c](https://github.com/theburrowhub/heimdallm/commit/9e8006ce2883ae192457a742ef8f53424a394742))
* renombrar rutas auto-pr → heimdallr en logs y LaunchAgent ([4c13332](https://github.com/theburrowhub/heimdallm/commit/4c13332377552bfbe8e48e6c84fb833306db7ef2))
* retention no se guardaba + SnackBar permanente en dismiss ([2f009ad](https://github.com/theburrowhub/heimdallm/commit/2f009ad4d36eeb568a52b9a50dd5eeead50540bb))
* reviews en goroutine — poll loop no bloquea durante análisis largos ([81b937d](https://github.com/theburrowhub/heimdallm/commit/81b937d2cbefcb1b53a0555d1e3a002733eebd55))
* **security:** abort startup if API token cannot be created ([f1cd314](https://github.com/theburrowhub/heimdallm/commit/f1cd314e63ac597b50fb409c0c9605c7162a8054))
* **security:** allowlist CLI names + positional arg in resolveCLIPath (issue [#2](https://github.com/theburrowhub/heimdallm/issues/2)) ([#8](https://github.com/theburrowhub/heimdallm/issues/8)) ([ac1d5d4](https://github.com/theburrowhub/heimdallm/commit/ac1d5d40871b665835a4dcc21c71505730b07235))
* **security:** authenticate mutating API endpoints with per-daemon token (issue [#3](https://github.com/theburrowhub/heimdallm/issues/3)) ([#9](https://github.com/theburrowhub/heimdallm/issues/9)) ([7a73e03](https://github.com/theburrowhub/heimdallm/commit/7a73e031eca1ce6b6b4894cf17aaa23fb20f1a0e))
* **security:** block additional credential directories from AI workdir ([c9b54a5](https://github.com/theburrowhub/heimdallm/commit/c9b54a5ef17827b0feed8e52936bf11225578293))
* **security:** catch --flag=value bypass in ValidateExtraFlags, block --permission-mode ([#18](https://github.com/theburrowhub/heimdallm/issues/18)) ([fbc59f5](https://github.com/theburrowhub/heimdallm/commit/fbc59f5cf5bcbabfd12e9b9b86a218eb722c46ea))
* **security:** corregir 3 vulnerabilidades detectadas en auditoría ([#20](https://github.com/theburrowhub/heimdallm/issues/20)) ([94ce891](https://github.com/theburrowhub/heimdallm/commit/94ce891d936a4ca9d5e7ad749a15cdf5f97a75ca))
* **security:** corregir vulnerabilidades backend daemon (C-1, C-2, A-1, A-2, A-3, M-1, M-2, M-4, M-5, B-1) ([c8a15ab](https://github.com/theburrowhub/heimdallm/commit/c8a15ab8478e45e61f66283fa66bcc1935c5ccb5))
* **security:** corregir vulnerabilidades Flutter frontend (C-3, C-4, A-7, M-9, B-3) ([6d6d619](https://github.com/theburrowhub/heimdallm/commit/6d6d6191f8b6b90be46139bfec835c4748b4f63f))
* **security:** corregir vulnerabilidades infra (A-4, A-5, A-6, M-3, M-6, M-7, M-8) ([e079e53](https://github.com/theburrowhub/heimdallm/commit/e079e534a8ad57ceadb5103ec409c899ce1d8e0f))
* **security:** escape user strings in TOML config generation (issue [#6](https://github.com/theburrowhub/heimdallm/issues/6)) ([#14](https://github.com/theburrowhub/heimdallm/issues/14)) ([7acb643](https://github.com/theburrowhub/heimdallm/commit/7acb64377522ad81d48ba3079a87424437074d5b))
* **security:** limit concurrent review triggers with counting semaphore ([3befdfe](https://github.com/theburrowhub/heimdallm/commit/3befdfe9d5f755640ccbbc5d12ea34c36daedefb))
* **security:** protect cachedLogin with mutex to eliminate data race ([d47adbc](https://github.com/theburrowhub/heimdallm/commit/d47adbc6154b69f2e72d100db01f489d3e2f353f))
* **security:** remove CORS wildcard from SSE endpoint (issue [#4](https://github.com/theburrowhub/heimdallm/issues/4)) ([#12](https://github.com/theburrowhub/heimdallm/issues/12)) ([65ce4b1](https://github.com/theburrowhub/heimdallm/commit/65ce4b1b4fb05815dfa352481733ae9e5ecc8040))
* **security:** require auth on GET /config and GET /agents; allowlist PUT /config keys ([#19](https://github.com/theburrowhub/heimdallm/issues/19)) ([7d72a86](https://github.com/theburrowhub/heimdallm/commit/7d72a8613b1a61acdcca5b43f3352527992bb738))
* **security:** require auth token on /me, /prs, /stats endpoints ([2eb611a](https://github.com/theburrowhub/heimdallm/commit/2eb611a40aed083337f58d0f60a394debb63c253))
* **security:** resolve symlinks in ValidateWorkDir before applying denylist ([#17](https://github.com/theburrowhub/heimdallm/issues/17)) ([a33e400](https://github.com/theburrowhub/heimdallm/commit/a33e4006deea72aa0a3afca4803ba02e82c4fb04))
* **security:** use json.Marshal instead of fmt.Sprintf for SSE event JSON ([3657923](https://github.com/theburrowhub/heimdallm/commit/3657923109c6b2c0146649eeb41fb7043ec54d36))
* **security:** validate config values in PUT /config ([406a4b0](https://github.com/theburrowhub/heimdallm/commit/406a4b04e7203441ce6c71676df4f7ca16e89775))
* **security:** validate WorkDir before setting as AI CLI working directory ([#13](https://github.com/theburrowhub/heimdallm/issues/13)) ([750abd8](https://github.com/theburrowhub/heimdallm/commit/750abd8bd9a266db7a122cea2c562e31175e9e97))
* setPreventClose desde postFrameCallback — garantiza que el window está listo ([b383daf](https://github.com/theburrowhub/heimdallm/commit/b383dafacf03011cf076057026346efdfccafb9d))
* single instance en todos los modos — check activo en debug + dev-stop más agresivo ([676ab7a](https://github.com/theburrowhub/heimdallm/commit/676ab7a0223889c54089ed0720d1b4865b4380dc))
* single instance via PID file — funciona en debug y producción ([c22eb21](https://github.com/theburrowhub/heimdallm/commit/c22eb21d7213cb4ec64799b5b573813efea7b708))
* single instance vía SIGUSR1 — la app lo gestiona sin depender del OS ni del Makefile ([4f72082](https://github.com/theburrowhub/heimdallm/commit/4f720822420c22c0268bf91886809e9a78c8ef3d))
* SnackBar dismiss — showCloseIcon + duration explícito en todos los snackbars ([29d9501](https://github.com/theburrowhub/heimdallm/commit/29d9501bd7be01b69d36627bd5d0060636d3b408))
* SSE reconnect + sort persistente en Reviews ([40e13a5](https://github.com/theburrowhub/heimdallm/commit/40e13a599ac45d90749db449b1a6810b5b2860b7))
* **sse:** enviar token de auth en SseClient — corrige 401 en /events y /logs/stream ([d6227be](https://github.com/theburrowhub/heimdallm/commit/d6227be73b61fbc885fd49b33719d2fc311ee11e))
* sudo gem install fpm — permisos en Ubuntu CI ([8c2e757](https://github.com/theburrowhub/heimdallm/commit/8c2e757c3059f3cc5471decd0d36166822cdcd3a))
* test config_test actualizado (claude movido a Agents tab) + rpm en lugar de rpm-build en CI ([9e616f6](https://github.com/theburrowhub/heimdallm/commit/9e616f6b7187c409c6d2b7ed080b2d195f218ce5))
* tray mostraba mis PRs cuando me aún no cargó — prsProvider depende de meProvider ([496d3e3](https://github.com/theburrowhub/heimdallm/commit/496d3e3fdef9f710c31f19a31dcd9b611e11a04d))
* validar cli_flags de perfiles de prompt contra denylist antes de ejecutar ([faa8862](https://github.com/theburrowhub/heimdallm/commit/faa8862fa6774e3b8ff5804bb433fd11094a05a1))


### Documentation

* actualizar README, LLM guide y GitHub Pages para Linux y modos de revisión ([feab4b5](https://github.com/theburrowhub/heimdallm/commit/feab4b587cfa6bcd476c8b2247b2a225373bb1f2))
* Heimdallm v2 design spec — issue tracking, rename, web UI ([43b4cb6](https://github.com/theburrowhub/heimdallm/commit/43b4cb6905b01f3cb778812e0f065c30a520714f))
* plan PR comments context injection ([ef214d1](https://github.com/theburrowhub/heimdallm/commit/ef214d1d0ac57449103f84308887273ae73b77c4))
* spec PR comments context injection ([86a61b0](https://github.com/theburrowhub/heimdallm/commit/86a61b06c39eb6bf95a63fecc566dbf054604f4e))

## [0.1.3](https://github.com/theburrowhub/heimdallm/compare/v0.1.2...v0.1.3) (2026-04-12)


### Features

* add local test infrastructure with 3-level smoke/github/e2e tests ([e791243](https://github.com/theburrowhub/heimdallm/commit/e7912431c7aed43ca9d81fa01392962129e5b228))


### Documentation

* add E2E test guide with step-by-step verification plan ([#14](https://github.com/theburrowhub/heimdallm/issues/14)) ([558c76a](https://github.com/theburrowhub/heimdallm/commit/558c76a626f7c09a0a43cbabfa4ee51277920f0c)), closes [#7](https://github.com/theburrowhub/heimdallm/issues/7)
* add MIT LICENSE file ([#12](https://github.com/theburrowhub/heimdallm/issues/12)) ([ba938e8](https://github.com/theburrowhub/heimdallm/commit/ba938e88fb2408aec65e98742283e3f8bdd52972)), closes [#5](https://github.com/theburrowhub/heimdallm/issues/5)
* document Gemini CLI authentication options for Docker ([#15](https://github.com/theburrowhub/heimdallm/issues/15)) ([b757d5a](https://github.com/theburrowhub/heimdallm/commit/b757d5af4d963a3b3968a8bbad8b7cd662a657e3)), closes [#8](https://github.com/theburrowhub/heimdallm/issues/8)

## [0.1.2](https://github.com/theburrowhub/heimdallm/compare/v0.1.1...v0.1.2) (2026-04-11)


### Bug Fixes

* chain docker build in release workflow to bypass GITHUB_TOKEN limitation ([967b75d](https://github.com/theburrowhub/heimdallm/commit/967b75dda3afad5d2521a9548f084a1191aaddce))

## [0.1.1](https://github.com/theburrowhub/heimdallm/compare/v0.1.0...v0.1.1) (2026-04-11)


### Bug Fixes

* trigger docker build on release event instead of tag push ([0ca7f5f](https://github.com/theburrowhub/heimdallm/commit/0ca7f5f3ef5490b3ef91257ea87fbae3ea6ce88c))

## 0.1.0 (2026-04-11)


### Features

* initial release of heimdallm ([bb91e08](https://github.com/theburrowhub/heimdallm/commit/bb91e0808f244d369f0ba22e3bfa8dc68aec3c18))


### Bug Fixes

* remove $schema from release-please manifest ([9065608](https://github.com/theburrowhub/heimdallm/commit/90656085b85159e4072c2fe1f08d52655613d73c))
