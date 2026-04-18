# Changelog

> **Note:** Versions 0.1.0–0.1.3 were originally published under [`theburrowhub/heimdallr-docker`](https://github.com/theburrowhub/heimdallr-docker) (now archived). The project was unified into this repository and renamed to Heimdallm in v0.1.1.

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
