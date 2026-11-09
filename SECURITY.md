# Security Policy

## Reporting a vulnerability

Please do not report security vulnerabilities in public issues, pull requests or discussions.

Report them privately by email to contact@bsos.co, with `[security]` at the start of the subject.

Include what you can of the following:

- the component affected (platform server, node agent, web UI, deployment files) and the version or commit
- the steps to reproduce, or a proof of concept
- what an attacker could do with it
- any suggested fix

## What to expect

- We acknowledge your report within 3 business days.
- We send an initial assessment within 10 business days, including whether we consider it a vulnerability and how severe it is.
- We keep you updated while we work on a fix, and agree a disclosure date with you. We aim to publish a fix and an advisory within 90 days of the report.
- We credit you in the advisory unless you ask us not to.

## Supported versions

Security fixes are made on the latest release only, or on `main` until the first release. Please check that the issue still exists there before reporting.

## Scope

This policy covers the code in this repository. Vulnerabilities in Hyperledger Besu itself should be reported to the Besu project under its [security policy](https://github.com/hyperledger/besu/security/policy). Misconfiguration of your own deployment, such as exposing a node's RPC port to the internet, is not a vulnerability in this project, but we are glad to hear about defaults or documentation that make such mistakes easy.
