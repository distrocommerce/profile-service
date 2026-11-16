# profile-service

First DistroCommerce microservice and the template for the others: keep it small, idiomatic and
production-shaped. Infrastructure (kind cluster, Envoy Gateway, ZITADEL, observability, the
`service-lib` Helm library chart) lives in the sibling checkout `../distrocommerce-infra`.

## Fixed decisions (do not reopen unless asked)
- Go 1.27.3, standard library `net/http` ServeMux (no web framework), pgx v5, `log/slog` JSON logs.
- Endpoints: `GET /healthz` (liveness, no dependencies), `GET /readyz` (readiness, checks the DB),
  public API under `/v1/`.
- Every error is RFC 9457 `application/problem+json` with a stable `code` field, including router 404/405,
  413 and recovered panics.
- Request bodies are limited (`MAX_BODY_BYTES`, default 1 MiB) → 413.
- OpenTelemetry for traces, metrics and logs (OTLP gRPC to the collector); probes are not traced. Logs also stay
  on stdout as JSON; `LOG_EXPORT_LEVEL` (default `info`) keeps debug probe lines off OTLP.
- SQL migrations are embedded and applied at startup.
- Multi-stage Docker build, distroless `static-debian13:nonroot` runtime, non-root.
- Images must have linux/arm64 builds (Apple silicon). Pin every version; never `latest`.
- Envoy validates the JWT and sets `X-User-ID` to the token `sub`. The service trusts that header and does
  no JWT validation itself.

## Layout
- `cmd/profile-service/`: main (wiring, server, graceful shutdown only)
- `internal/config`: environment configuration, fail-fast validation
- `internal/httpapi`: routes, handlers, middleware; `errors.go` holds the error codes and `writeError`
- `internal/store`: pgx store; migrations in `internal/store/migrations/NNNN_name.sql`
- `internal/telemetry`: OTel providers, slog logger with `trace_id`
- `deploy/chart/`: thin Helm chart over `service-lib`, plus `values-local.yaml`
- `Makefile`: build, test, lint, run, image, kind-load, deploy, undeploy
- `.github/workflows/ci.yml`: CI checks (no image push, no deploy)

## How to work here
- Before finishing a change: `make test lint` (go vet, tests, golangci-lint, gofmt/goimports diff) must pass.
  `make lint` uses `go run` with a pinned golangci-lint, because a prebuilt binary built with an older Go
  refuses this module. A change is done only when CI on the pushed commit is green.
- CI checks out `distrocommerce-infra` at the tag `service-lib-v<version>` for the chart job. Bump that ref in
  `.github/workflows/ci.yml` together with the service-lib version in `deploy/chart/Chart.yaml`; the tag is
  created and pushed in the infra repo.
- Actions in `.github/workflows/` are pinned to a full commit SHA with the version in a comment.
- New endpoints: handler test with `httptest` for success and every error case; errors use the shared helper.
  Request bodies go through `decodeJSON`, so the body limit and the 413 come from one place.
- Middleware between `otelhttp` and the mux must pass the same `*http.Request` on (no `r.WithContext`).
  The mux sets `r.Pattern` on it, and the span name, `http.route` and the access log read it afterwards.
- Migrations are append-only: add a new numbered file, never edit an applied one.
- `make test` skips the store test. After changing SQL or migrations, also run it against the compose
  PostgreSQL with `PROFILE_TEST_DATABASE_URL` (see the README); it works in a throwaway schema.
- `/readyz` must answer within the kubelet probe timeout (1 s; `service-lib` sets none): every dependency
  check gets a timeout below that (the DB ping uses 800 ms).
- The pod runs with the `service-lib` security defaults: non-root, read-only root filesystem, no capabilities.
  Never write files at runtime (no temp files, no local caches); if that becomes necessary, propose a writable
  volume in `service-lib` instead of turning the defaults off here.
- Chart values: shared defaults in `deploy/chart/values.yaml`, only environment-specific settings in
  `values-local.yaml` (and later one file per environment).
- Check APIs and versions (`go doc`, module docs, `helm template`) instead of guessing; say when something is unverified.
- Deploy loop: `make image kind-load deploy PROJECT_ID=<zitadel project id>`. Do not reset or recreate the kind
  cluster or the compose stores from this repo. The project ID changes after an infra reset, so it is never
  committed. `make run` listens on 8081 because kind holds 8080.
- The image tag is the short SHA, plus `-dirty-<hash of the changes>` for an uncommitted tree: a changed tree
  must get a new tag or `helm upgrade` is a no-op and the old pod keeps running. Keep that suffix; do not
  commit just to deploy.
- Never copy library templates; propose changes to the infra repo instead. If `service-lib` cannot express
  something this service needs, stop and describe the change for `../distrocommerce-infra`; do not work
  around it in `deploy/chart`, and do not edit the infra repo from here.
- OpenSpec: specs in `openspec/specs/`, changes in `openspec/changes/` (archived under `archive/`). The
  `/openspec-*` skills use the OpenSpec 2.0 layout: the files live in `.agents/skills/` (with
  `.openspec-target`), and `.claude/skills/` holds relative symlinks to them; commit both, and edit neither by
  hand. `openspec update` regenerates them. `openspec-verify-change` is not in the `core` profile; with
  OpenSpec 2.0.0, `update` (also with `--force`) keeps it. After upgrading OpenSpec, check that it is still
  there.
- Features follow the spec-driven workflow below.
- Never commit secrets. All code, comments, docs and commit messages are in English. No AI attribution in commits.
- Never commit without the user's approval: propose the files and the message, then wait. One approval
  covers one commit. Keep commits to logical steps on `main`; push only when asked.

## Feature workflow (spec-driven, OpenSpec)
0. **explore** (optional, `/openspec-explore`): research the idea and the existing code and architecture
   without changing files.
1. **propose** (`/openspec-propose`): create the change: `proposal.md` (why and scope), delta specs (`specs/`),
   `design.md` and an atomic plan (`tasks.md`). After propose and after every update-change, run
   `openspec validate <change> --strict` (structure only, not whether the content is right).
2. **spec review + update-change** (`/openspec-update-change`): the user reviews the plan, looks for
   architectural weak spots and decides the open questions; the artifacts are adjusted before any code is
   written.
3. **apply-change** (`/openspec-apply-change`): implement `tasks.md` step by step: business logic, tests,
   docs, Dockerfile, Helm chart and manifests, within the chart rules above (no copied library templates, no
   workarounds in `deploy/chart`, no edits to the infra repo; if `service-lib` cannot express something, stop
   and describe the change for `../distrocommerce-infra`).
4. **technical checks**: `make test lint` (go vet, unit tests with `-race`, golangci-lint, gofmt/goimports
   diff); for SQL or migration changes also the store test with `PROFILE_TEST_DATABASE_URL`. CI runs the same
   checks on every push to `main` and every pull request, plus the store test against PostgreSQL 18.6,
   `helm template` with the security defaults check, `docker build`, `govulncheck` and
   `openspec validate --specs --strict`. Planned: kubeconform, kube-linter, Trivy.
5. **verify-change** (`/openspec-verify-change`): AI audit of the code and configs against `specs/` and
   `design.md`, with CRITICAL, WARNING and SUGGESTION findings. Spec gaps go back to step 2, code gaps to
   step 3, then step 4 again.
6. **code review**: `/code-review` (plus `/security-review` when identity, request parsing or secrets are
   touched), and the user reads the diff against the specs. Verify is the same model checking its own work;
   this is the independent check. Findings go back to step 2 or 3 like verify's.
7. **deploy to local**: `make image kind-load deploy PROJECT_ID=...`. The image is built from the uncommitted
   tree, so its tag has a `-dirty-<hash>` suffix. `PROJECT_ID` is never committed.
8. **manual end-to-end check**: real requests through Envoy with a ZITADEL token and a check of the database.
   The user fetches the token in a separate terminal with `read`, so the client secret never reaches the
   conversation, and writes it to the token file in Claude's session scratchpad; Claude never prints it and
   deletes the file afterwards. Note the existing rows first (stop if the test user already has a profile);
   afterwards delete only the rows the test created.
9. **archive-change** (`/openspec-archive-change`): merge the delta specs into `openspec/specs/`, verify the
   sync (nothing moves if it fails), then move the change to `openspec/changes/archive/<date>-<change>/`;
   optionally `openspec validate --specs --strict`.
10. **commit & push**: code, tests, docs, main specs and the archived change in one commit after approval;
    push only when asked; no AI attribution. After the push, CI on that commit must be green before the change
    counts as done; a red run goes back to step 3.
11. **clean redeploy**: run `make image kind-load deploy PROJECT_ID=...` again from the committed tree, so the
    image gets the clean short-SHA tag that matches the commit. Quick check: pod Running and one request
    through Envoy.

Later: branch + PR workflow (wait for green CI before merge), Tilt (makes steps 7 and 11
automatic), GitOps deploy (Argo CD).
