# Contributing

Thanks for your interest in BSOS Node Manager. Bug reports, fixes, documentation and ideas are all welcome.

Please follow the [Code of Conduct](CODE_OF_CONDUCT.md). Security issues go through the [security policy](SECURITY.md), not public issues.

## Before you start

- **Bugs:** open an issue with the bug report form. Include versions and steps to reproduce.
- **Features or larger changes:** open an issue first and describe the problem you want to solve. Agreeing on the approach before you write code saves both of us time.
- **Small fixes** (typos, docs, an obvious bug): a pull request on its own is fine.

## Development setup

You need Go 1.26 and Node.js 22.

```bash
# Build and test
(cd backend && go build ./... && go test ./...)
(cd agent && go test ./...)
(cd frontend && npm install && npm run build)

# Web UI against mock data, no backend needed: http://localhost:5173
(cd frontend && npm run dev)
```

The mock UI accepts `admin`, `ops` and `viewer` with the password `BesuDemo-2026`.

Tests that need PostgreSQL are skipped unless `TEST_DATABASE_URL` points at a database on `localhost` whose name contains `test`. If your change touches the database layer, run them:

```bash
docker run -d --name bsos-node-manager-test-db -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=besu_test -p 55432:5432 postgres:16
TEST_DATABASE_URL=postgres://postgres:postgres@localhost:55432/besu_test?sslmode=disable \
  sh -c 'cd backend && go test ./...'
```

To run the platform with Docker Compose and connect a Besu node through the agent, see [`deploy/README.md`](deploy/README.md).

## Making a change

- **API changes** start in `contract/openapi.yaml`. Regenerate the types on all three sides and commit them with the change:

  ```bash
  (cd backend && make generate)
  (cd agent && make generate)
  (cd frontend && npm run gen:api)
  ```

- **UI text** lives in `frontend/src/i18n/en.json` and `zh.json`. Change both.
- **Comments** explain why the code is the way it is, in a sentence or two, and must make sense on their own. Do not point to documents outside this repository.
- **Tests:** add or update tests for the behaviour you change. A bug fix should come with a test that fails without it.
- Keep a pull request to one change. Unrelated fixes go in separate pull requests.

## Commit messages

```
type(scope): short summary in the imperative mood

A body in plain prose, wrapped at about 72 characters, saying what was
wrong or missing and why this change is the right fix.

Signed-off-by: Your Name <you@example.com>
```

`type` is the kind of change, for example `feat`, `fix`, `docs`, `refactor`, `test` or `chore`. `scope` is the area you changed, for example `agent`, `alerting`, `ui` or `deploy`. Write the summary in lowercase, without a trailing period. Use the same form for the pull request title: it becomes the commit subject in this repository. Write code comments in English.

## Developer Certificate of Origin

Every commit must be signed off, certifying that you wrote the change or otherwise have the right to submit it under the project's license, as described in the [Developer Certificate of Origin](https://developercertificate.org/). Add the sign-off with `-s`:

```bash
git commit -s -m "fix(agent): retry the tunnel after a DNS failure"
```

The name and email must be real. Pull requests with unsigned commits cannot be accepted. To add the sign-off to earlier commits, run `git rebase --signoff upstream/main` (or whichever remote points at this repository) and force-push your branch.

## How pull requests are merged

The public GitHub repository is an export of the maintainers' internal repository, one commit per change. Pull requests are reviewed here as usual, but they are not merged with the merge button:

1. A maintainer reviews your pull request and may ask for changes.
2. Once it is approved, the maintainer applies your commits to the internal repository, keeping your `Signed-off-by`. You stay the author; if a maintainer adds follow-up commits, you are credited as co-author.
3. The change appears in the public repository with the next sync, as one commit titled with your pull request title.
4. The maintainer closes your pull request with a link to that commit.

This means your pull request shows as closed rather than merged, even though your change was accepted.

## License

By contributing, you agree that your contributions are licensed under the [Apache License 2.0](LICENSE).
