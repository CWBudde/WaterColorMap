# Changelog

## Unreleased

### Bug Fixes

- **watercolor:** anchor hi-DPI rendering to world position. Noise scale, all blur/shade/edge
  sigmas, the adaptive-noise distances and the paper/layer texture period are lengths, and they
  were fixed **device**-pixel constants while the sampling offsets scaled with the tile size. An
  `@2x` tile therefore drew grain, texture and blur at half the ground size of the 256px tile
  covering the same area. They are now scaled by `tileSize / 256`, so a 512px tile samples the
  same field as its 256px twin, just at twice the resolution.

  Behaviour change worth knowing about: **`--tile-size 512` without `@2x` now renders at scale 2.**
  That is correct and consistent, but it is a visible look change for anyone running a non-256
  base tile size. Output at the default 256px is bit-identical — the scale path is a no-op there.

  Still mismatched, tracked separately: Mapnik `stroke-width` in `assets/styles/layers/*.xml` is
  fixed device px, so `@2x` roads remain half as wide in ground terms.

## [0.3.0](https://github.com/CWBudde/WaterColorMap/compare/v0.2.0...v0.3.0) (2026-08-13)

### Features

- add railroad and civic layers, rename module to cwbudde ([#4](https://github.com/CWBudde/WaterColorMap/issues/4)) ([9f6b504](https://github.com/CWBudde/WaterColorMap/commit/9f6b5047dc3fe1d178445c62bb3dc3372098d8ec))
- improve layer ordering, add playgrounds, and enhance road sizing at high zoom levels ([1d64e69](https://github.com/CWBudde/WaterColorMap/commit/1d64e6903c949834fb20a42b3938e3fce5361952))
- improved inset shadow algorithm ([af6ce3a](https://github.com/CWBudde/WaterColorMap/commit/af6ce3aa7a88f6abf7ac3a382e346feba2473323))
- more advanced render pipeline ([0d14bda](https://github.com/CWBudde/WaterColorMap/commit/0d14bda4ccc134d73b92dee831118b505a00e42a))
- more stamen like style ([6947e01](https://github.com/CWBudde/WaterColorMap/commit/6947e0163527226f73017354f83eaf655c793083))
- new baseline ([0aabc7c](https://github.com/CWBudde/WaterColorMap/commit/0aabc7ca7627da0f534dea5253d01fae3525ae0e))
- new baseline rendering ([6a011e5](https://github.com/CWBudde/WaterColorMap/commit/6a011e56d141048a91b02bac7cf1326d25fc9b59))
- recent improvements ([e693390](https://github.com/CWBudde/WaterColorMap/commit/e693390ccd5dd16fddd4cd3659b488493a084812))
- rendering is fast and beautiful now. Features are so so... ([44d1989](https://github.com/CWBudde/WaterColorMap/commit/44d19898fa27cc0e87ccce3abb4d27f645b48420))

### Bug Fixes

- performance fix ([c4538f7](https://github.com/CWBudde/WaterColorMap/commit/c4538f702c600495342d44c8a84aac90cbcca4a8))
- polygon rendering fix ([a4044cc](https://github.com/CWBudde/WaterColorMap/commit/a4044cc3ae9bdc023933bbb990405c8f4d6832ef))

## [0.2.0](https://github.com/MeKo-Christian/WaterColorMap/compare/v0.1.0...v0.2.0) (2025-12-20)

### Features

- **5.6a:** WASM Browser Playground with on-demand generation and GitHub Pages CI/CD ([e967eac](https://github.com/MeKo-Christian/WaterColorMap/commit/e967eac8b7e59595c2a08943bc70440637a04e8f))
- add --allow-failures flag for graceful API rate limit handling ([1379adf](https://github.com/MeKo-Christian/WaterColorMap/commit/1379adf4651f5acfc49503db5b1ab13d6be5d50b))
- added ci ([8d7bf4a](https://github.com/MeKo-Christian/WaterColorMap/commit/8d7bf4a21e313ab69935fcdd6e0467df2420b39f))
- added release ([8d830a8](https://github.com/MeKo-Christian/WaterColorMap/commit/8d830a804c34539e8fddbe7564902592d38a5446))
- added release please and reduced response ([844999a](https://github.com/MeKo-Christian/WaterColorMap/commit/844999a6e002d59fc5938dc31f34b79e813e7bcf))
- completed phase 3 ([b2aff37](https://github.com/MeKo-Christian/WaterColorMap/commit/b2aff37bf5eb8dd36562f88741aac83ddf483f18))
- MBTiles first draft ([839dadd](https://github.com/MeKo-Christian/WaterColorMap/commit/839dadd3910dba0a920457405e0de36a5fed083e))
- more about license ([2f51e02](https://github.com/MeKo-Christian/WaterColorMap/commit/2f51e027a19e7663a46ac858990401146343ae4c))
- more details ([aa75e14](https://github.com/MeKo-Christian/WaterColorMap/commit/aa75e14a571c5091b0f022a89ff1e476b54af0ca))
- more progress ([c326074](https://github.com/MeKo-Christian/WaterColorMap/commit/c3260747c007a254defe78208c8748b62338a176))
- new dockerfile ([3e261bc](https://github.com/MeKo-Christian/WaterColorMap/commit/3e261bc4f8f124f9ec2254fa8c3a37735d47d266))
- recent work ([2227859](https://github.com/MeKo-Christian/WaterColorMap/commit/2227859330c4cbec29d57e4e631a9d676d829b2f))
- static pre-rendering ([096c113](https://github.com/MeKo-Christian/WaterColorMap/commit/096c1135355a550c7bdf48f9c6db8c4652a60a4d))
- task 5.2 ([94bca06](https://github.com/MeKo-Christian/WaterColorMap/commit/94bca068404c2e9ef8f318101025868e9259efcd))

### Bug Fixes

- added exponential back-off strategy ([1562020](https://github.com/MeKo-Christian/WaterColorMap/commit/15620205544690ed1dc0de0fb612d631e5be3eda))
- better strategy for border on blur ([0f33a89](https://github.com/MeKo-Christian/WaterColorMap/commit/0f33a89e09ca67706625332dbbeffa731907e648))
- bug fixes ([76683c9](https://github.com/MeKo-Christian/WaterColorMap/commit/76683c9e229cb334d755d2568e899192fa09abd1))
- CI for playground ([64dea5a](https://github.com/MeKo-Christian/WaterColorMap/commit/64dea5a288fccf010e7f572d730779194604a521))
- deprecated API ([8b80020](https://github.com/MeKo-Christian/WaterColorMap/commit/8b80020d7f8fa3e4f6d3429ae6793a77f1fbe100))
- format ([633e52e](https://github.com/MeKo-Christian/WaterColorMap/commit/633e52e35e8000611d15562b8f2fe535f01dc915))
- formatting ([577dc00](https://github.com/MeKo-Christian/WaterColorMap/commit/577dc00ae369eaf624ad52156623b840fa05bafc))
- formatting ([6b10498](https://github.com/MeKo-Christian/WaterColorMap/commit/6b10498c251394739a406b263cbcd256ee467305))
- lint & formatting fixes ([84ce7e6](https://github.com/MeKo-Christian/WaterColorMap/commit/84ce7e643e47433329617a38b9eaf17427d033b8))
- lint issues and test fixes ([009ac1c](https://github.com/MeKo-Christian/WaterColorMap/commit/009ac1caa3dd2a77f8e48291ec4ab39ea78e2797))
- more dockerfile ([cf3e367](https://github.com/MeKo-Christian/WaterColorMap/commit/cf3e367b51526ebd8b7e0b9f6b61a8f60e15b3a1))
- more features ([3a6387f](https://github.com/MeKo-Christian/WaterColorMap/commit/3a6387fd374fc68567cfb8aac5040ffb66cf7a81))
- more wasm fixes ([edd9643](https://github.com/MeKo-Christian/WaterColorMap/commit/edd9643a52b41aca9af5bc0cf90b8f30701e03b2))
- playground ([50bd0cc](https://github.com/MeKo-Christian/WaterColorMap/commit/50bd0ccf76b9c15265f676c8594080a1a1be2310))
- playground ([931390b](https://github.com/MeKo-Christian/WaterColorMap/commit/931390b090799e3ffbbef39541f45a4d2a5e62a7))
- playground ([e6de40c](https://github.com/MeKo-Christian/WaterColorMap/commit/e6de40cc3eb0f8e16e615455c10ab2c080a884bc))
- playground issues fixed ([6ac711a](https://github.com/MeKo-Christian/WaterColorMap/commit/6ac711a2f1cd252620bef286b9f204961a9f4594))
- playground issues fixed ([4c3c106](https://github.com/MeKo-Christian/WaterColorMap/commit/4c3c106a456b2cec2ec63250b07f2a6ceac2b1e9))
- playground issues fixed ([788d186](https://github.com/MeKo-Christian/WaterColorMap/commit/788d18629cdcd12b4d9feb5d8db30dac50359fa7))
- playground issues fixed ([556cea2](https://github.com/MeKo-Christian/WaterColorMap/commit/556cea2f022d09ff250e37a4d1799f3dcfb1df22))
- playground issues fixed ([b2de348](https://github.com/MeKo-Christian/WaterColorMap/commit/b2de348b453de5f9057b8e3a3cc1223cf2df12f7))
- playground issues fixed ([a637c67](https://github.com/MeKo-Christian/WaterColorMap/commit/a637c67f73b412f02993b7e74b161e04b8c9aec2))
- playground issues fixed ([871ae21](https://github.com/MeKo-Christian/WaterColorMap/commit/871ae21a206d0e15ba8e86d3c56f70c633543440))
- polygon issue fixed ([ad1ff70](https://github.com/MeKo-Christian/WaterColorMap/commit/ad1ff70c5bd81af66da0045c72a763af277e9184))
- update ([3724ab3](https://github.com/MeKo-Christian/WaterColorMap/commit/3724ab32e75950d68432fb489d4da2819e9e9fc6))
- update .gitignore to not ignore cmd/watercolormap directory ([9517977](https://github.com/MeKo-Christian/WaterColorMap/commit/9517977a4d805112e778d77bd123851f240615f2))
- wasm ([80f3586](https://github.com/MeKo-Christian/WaterColorMap/commit/80f358634669de4346896328f7ec82bc4e5b08f8))
- wasm deploy ([4882969](https://github.com/MeKo-Christian/WaterColorMap/commit/48829697c5112da91d120dcbdf56fa5f2c2e2767))
- wasm deploy ([4d29a53](https://github.com/MeKo-Christian/WaterColorMap/commit/4d29a53bc7c676687b3828162b7bca409f138db0))
