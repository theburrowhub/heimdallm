# Changelog

## [0.10.7](https://github.com/theburrowhub/heimdallm/compare/daemon/v0.10.6...daemon/v0.10.7) (2026-11-19)


### Bug Fixes

* **executor:** reject agent answers that are not a review ([#891](https://github.com/theburrowhub/heimdallm/issues/891)) ([775122b](https://github.com/theburrowhub/heimdallm/commit/775122b71de419c0e781d55a27aba7c3b37cb438))
* **executor:** retry a review that ran out of the token-saving turn cap when claude exits 1 ([#909](https://github.com/theburrowhub/heimdallm/issues/909)) ([6e78e0e](https://github.com/theburrowhub/heimdallm/commit/6e78e0eb43f34cffd9bb093732108d943a064898))
* **executor:** truncate oversized diffs on line boundaries and name the files cut ([#893](https://github.com/theburrowhub/heimdallm/issues/893)) ([53fe2a1](https://github.com/theburrowhub/heimdallm/commit/53fe2a1c1b04b2b5c5fa6f53478cd08e7a2052cc))
* **pipeline:** tell the review agent its checkout may not contain the PR ([#896](https://github.com/theburrowhub/heimdallm/issues/896)) ([e4fc083](https://github.com/theburrowhub/heimdallm/commit/e4fc083db31bf93434e1e2677b212d65b0833788))
* report failing GitHub polls in /health and heimdallm-cli status ([#898](https://github.com/theburrowhub/heimdallm/issues/898)) ([11ae297](https://github.com/theburrowhub/heimdallm/commit/11ae297882b1032f8ad6b5e4546c844268a566de))

## [0.10.6](https://github.com/theburrowhub/heimdallm/compare/daemon/v0.10.5...daemon/v0.10.6) (2026-11-12)


### Features

* exempt GitHub teams from the never-approve gate and autocomplete the exempt list ([#888](https://github.com/theburrowhub/heimdallm/issues/888)) ([143c840](https://github.com/theburrowhub/heimdallm/commit/143c840b80e76c66f835f7afcc996c497b1b556a))

## [0.10.5](https://github.com/theburrowhub/heimdallm/compare/daemon/v0.10.4...daemon/v0.10.5) (2026-11-12)


### Features

* **app:** render agent settings from schemas and edit custom agents ([#875](https://github.com/theburrowhub/heimdallm/issues/875)) ([d2c53e0](https://github.com/theburrowhub/heimdallm/commit/d2c53e0a7fc673fa838fc114289cf82c79739c89))
* **compat:** daemon API version and per-component compatibility matrix ([#874](https://github.com/theburrowhub/heimdallm/issues/874)) ([bbcb2b4](https://github.com/theburrowhub/heimdallm/commit/bbcb2b4d9ad1be1fa44ed957b8a2313a2bcfa83b))
* **daemon:** schema-driven agent options and custom CLI agents ([#873](https://github.com/theburrowhub/heimdallm/issues/873)) ([20ad82e](https://github.com/theburrowhub/heimdallm/commit/20ad82edc183c2bf7064dc25602c44d1f9fea685))
* exempt listed PR authors from the never-approve gate ([#884](https://github.com/theburrowhub/heimdallm/issues/884)) ([6ba6784](https://github.com/theburrowhub/heimdallm/commit/6ba67844ea18e8b9df6f6d312b7309198ddd2a67))
* **release:** per-component releases next to the unified installer ([#872](https://github.com/theburrowhub/heimdallm/issues/872)) ([5c6a52f](https://github.com/theburrowhub/heimdallm/commit/5c6a52f2d32aa82852262a43fb902915b08dd818))


### Bug Fixes

* **cli:** apply the daemon's custom schema rules in ValidateCustom ([#881](https://github.com/theburrowhub/heimdallm/issues/881)) ([0ad5408](https://github.com/theburrowhub/heimdallm/commit/0ad54081e5911115271156a3c3a80a7c1b8c0537))
* **daemon:** deny interpreter names that end in letters or trailing dots ([#879](https://github.com/theburrowhub/heimdallm/issues/879)) ([46d5738](https://github.com/theburrowhub/heimdallm/commit/46d5738464784374093aa4b7b1d42cf585af586e))
