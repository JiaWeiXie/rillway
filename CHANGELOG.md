# Changelog

Generated from committed Git history with git-cliff. Regenerate before a release.

## 0.8.0 - 2026-10-14

### Features

- **engine:** share adaptive affinity per registrable domain and fail over per dial

### Fixes

- **outbound:** pace WARP SOCKS5 handshakes and retry transient resets

### Performance

- **pac:** bound client DNS bypass decisions

### Documentation

- **verification:** record v0.7.0 deployment
- describe WARP retries, adaptive affinity, and PAC DNS cache

## 0.7.0 - 2026-10-13

### Features

- **config:** add outbound groups and composite rule schema
- **routing:** match source, inbound, port, and rule sets
- **engine:** select and balance outbound groups
- **control:** explain routes and manage outbound groups
- **web:** add accessible light and dark themes
- **web:** add group, rule-set, and route-preview editors
- **web:** add search, bulk actions, keyboard flows, and responsive controls
- **control:** improve management web interactions
- **tui:** display outbound groups

### Fixes

- **config:** allow group replacements for fixed routes
- **web:** invalidate previews only on revision change
- **routing:** preserve legacy rule matching
- address proxy core review findings
- **tui:** bound group rows to viewport

### Refactoring

- **web:** make browser initialization explicit

### Maintenance

- **engine:** cover disabled preview targets
- **web:** cover logout busy cleanup
- **tui:** cover service switch log reset

## 0.6.0 - 2026-10-12

### Features

- **resource-control:** manage WARP service logging
- **control:** add WARP service logging management and migrate resource control
- **web:** add WARP logging and refresh controls
- **tui:** add WARP logging and refresh controls
- **resource-control:** manage WARP service logging

### Fixes

- **tui:** gate status refresh during loading
- **warp:** preserve forced status refresh
- **web:** correct outbound refresh scheduling
- **web:** translate WARP logging status
- **tui:** translate WARP logging status
- **control:** localize WARP logging errors
- **web:** complete resource-control error translations
- **tui:** complete resource control message translations
- **i18n:** cover resource control messages
- **web:** translate unavailable resource control reason
- **resource-control:** roll back failed socket activation
- **platform:** satisfy release lint checks
- **resource-control:** format Linux helper imports

### Performance

- **warp:** reduce status polling overhead

### Refactoring

- **resource-control:** generalize privileged helper

### Documentation

- **verification:** record v0.5.1 release deployment and post-deployment checks

### Maintenance

- **resource-control:** cover migration and acceptance
## 0.5.1 - 2026-10-11

### Fixes

- **proxy:** configure shared connection capacity and expose rejections

### Documentation

- **verification:** record v0.5.0 release and deployment
- **verification:** use Traditional Chinese source terminology

### Maintenance

- **deps:** patch Go and x/net security vulnerabilities
- **release:** prepare v0.5.1 release notes
## 0.5.0 - 2026-10-08

### Features

- **proxy:** add live source access control

### Fixes

- **network:** harden proxy and tunnel reliability

### Documentation

- **verification:** record v0.4.1 release and deployment acceptance
- **release:** prepare v0.5.0 changelog
## 0.4.1 - 2026-10-08

### Fixes

- **proxy:** preserve connection budgets and adaptive routes

### Documentation

- **release:** prepare v0.4.1 changelog
## 0.4.0 - 2026-10-06

### Features

- **service:** add safe memory controls and self-traffic protection

### Fixes

- **ci:** focus dependency digest on direct updates

### Documentation

- **release:** prepare v0.4.0 changelog
## 0.3.1 - 2026-10-05

### Fixes

- **ui:** show server version and hide configuration revisions

### Documentation

- **release:** prepare v0.3.1 changelog
## 0.3.0 - 2026-10-05

### Features

- **agent:** add safe JSON CLI and public operations skill

### Documentation

- **release:** prepare operations skill release notes
## 0.2.2 - 2026-10-05

### Fixes

- **release:** ignore Python test bytecode in clean source checks

### Maintenance

- **release:** prepare v0.2.2 changelog
## 0.2.1 - 2026-10-05

### Fixes

- **ci:** reuse the digest issue with GitHub CLI bot identities

### Maintenance

- **deps:** bump actions/download-artifact from 4.3.0 to 8.0.1
- **deps:** bump actions/checkout from 6.1.0 to 7.0.1
- **deps:** bump jdx/mise-action from 3.6.3 to 5.0.1
- **deps:** bump actions/upload-artifact from 4.6.2 to 7.0.1
- **deps:** bump actions/attest-build-provenance from 3.0.0 to 4.2.2
- **ci:** merge reviewed action updates and report dependencies monthly
- **release:** prepare v0.2.1 changelog
- **release:** refresh v0.2.1 notes after digest verification
## 0.2.0 - 2026-10-05

### Features

- **control:** manage VPN profiles and restart service from the web

### Documentation

- record cleanup of disposable test environments

### Maintenance

- **release:** prepare v0.2.0 changelog
## 0.1.0 - 2026-10-05

### Features

- **ui:** add bilingual interfaces and Rillway branding
- **docker:** export proxy settings for pulls builds and containers
- **install:** add guided standalone binary setup and bilingual CLI docs
- **ui:** guide setup and group destination traffic
- **pac:** add managed bypass presets
- **tui:** reveal web management token
- **security:** prepare public CI and protected release workflows

### Fixes

- **deploy:** protect state directory and recognize WARP proxy mode
- **warp:** preserve existing device on repeat registration
- **outbounds:** protect direct and replace references on deletion
- **ui:** compact sidebar and add bilingual glossary
- **review:** harden routing lifecycle and traffic UI
- **review:** resolve provider lifecycle and UI refresh findings
- **cli:** prefer OS configuration conventions over remembered connections
- **cli:** follow installed service configuration before fallback paths
- **security:** reject runtime credentials before staging checks
- **ci:** freeze tools and reject dirty release source

### Documentation

- **deploy:** record Ubuntu rollout and live WARP verification
- **warp:** record repeat-registration fix and Unlimited validation
- **docker:** record deployed API and bilingual UI validation
- **outbounds:** record deployment and deletion UI validation
- **deployment:** record Ubuntu binary update verification
- record installed CLI configuration acceptance
- record public source and release artifact security checks
- prepare v0.1.0 public release
- **security:** record public repository protections

### Maintenance

- **tooling:** configure git-cliff and commit message hooks
- **security:** remove private deployment details
- **release:** prepare v0.1.0 verified source
- detect tracked source changes during tool setup
- **release:** finalize v0.1.0 changelog

### Other changes

- Initialize Rillway multi-outbound proxy with management interfaces and tests
- Record Ubuntu deployment target and verified prerequisites
- Configure local agent guidance and validation hooks

<!-- generated by git-cliff -->
