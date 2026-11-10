# Changelog

Notable changes to BSOS Node Manager. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.0.0]

First public release of the open-source edition.

### Added

- Validator governance: propose and vote on adding or removing validators (IBFT 2.0 and QBFT), with each proposal's vote count, history and the quorum and offline tolerance after the change.
- Authorized sender addresses: manage Besu's account allowlist per node or through a shared list.
- Node RPC access: access identities with method-level permissions, and JWTs signed by the platform that nodes verify with its public key.
- Monitoring: chain health, per-node status, peers, transaction pool and host metrics, with default alert rules and notifications by email, Telegram, Slack or webhook.
- Node onboarding: a one-line install puts an agent beside an existing Besu node (systemd or Docker), with an offline bundle, agent upgrades with rollback and a capability checklist per node.
- Built-in PKI: each instance runs its own CA and issues short-lived mTLS certificates to its agents; the root key can be kept offline or an existing enterprise PKI imported.
- Deployment: `up.sh` builds and starts the platform with Docker Compose, with automatic (ACME), own-certificate or plain HTTP modes.
- Accounts with three roles (system administrator, operator, viewer) and a sign-in history.
- English and Traditional Chinese user interface.

[Unreleased]: https://github.com/bsostech/bsos-node-manager/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/bsostech/bsos-node-manager/releases/tag/v1.0.0
