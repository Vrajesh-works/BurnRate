# BurnRate - Product Doc

## Problem

Payment platforms expose card-authorization, refund and payout APIs to many
merchants and partners at once. One misbehaving integration (a retry storm, a
broken batch job, a scraped key) can starve every other merchant of capacity and
push the platform past its reliability targets. Teams need two things that are
usually built separately and badly:

1. A fast, fair **rate-limiting decision service** that every gateway can ask
   "may merchant X spend N units right now?"
2. Proof that the service itself is **run like production**: it deploys through
   Git, scales on load, rolls back a bad release on its own, alerts on SLO burn
   rate instead of raw thresholds, and recovers from pod loss in a measured time.

BurnRate is that service plus the platform around it, running locally on kind.

## Target users

| User | What they need from BurnRate |
| --- | --- |
| API gateway / edge team | A low-latency HTTP check endpoint with clear allow/deny answers and `Retry-After` hints |
| Platform / SRE team | GitOps deploys, canary releases gated on metrics, SLO dashboards and burn-rate alerts |
| Merchant success / risk | Per-merchant policies (burst size and refill rate) that can be changed without a deploy |
| Hiring reviewers | A small, readable codebase that shows Kubernetes, observability, Go and CI/CD done properly |

## Core features (MVP, today)

1. **Rate-limit decision API (Go)**
   - `POST /v1/check` - token-bucket decision per `(tenant, key)` with a cost.
   - `PUT/GET /v1/policies/{tenant}` - per-tenant bucket capacity and refill rate.
   - `GET /healthz`, `GET /readyz`, `GET /metrics`.
2. **Observability built in**
   - Structured JSON logs (`log/slog`) with trace IDs on every request line.
   - Prometheus RED metrics plus decision counters.
   - OpenTelemetry traces exported over OTLP when a collector is configured.
3. **Production-grade Helm chart**
   - Liveness and readiness probes, resource requests and limits.
   - HorizontalPodAutoscaler, PodDisruptionBudget, non-root read-only container.
   - Optional Argo Rollouts `Rollout` with a Prometheus `AnalysisTemplate` that
     aborts and rolls back when the canary 5xx ratio rises.
4. **GitOps** - Argo CD `Application` manifests (app-of-apps) for the service,
   the monitoring stack and Argo Rollouts.
5. **SLOs** - availability and p99 latency SLOs written as sloth specs and
   generated into multi-window multi-burn-rate Prometheus rules; a Grafana SLO
   dashboard as JSON.
6. **Reliability tests** - a k6 load test that drives the HPA to scale out and a
   pod-kill chaos script that records time to recovery.
7. **Supply chain and CI** - multi-stage Dockerfile on a distroless non-root base,
   Trivy image scan, Go unit and HTTP tests, Helm lint, and a kind-based
   end-to-end smoke test in GitHub Actions.

## Non-goals (today)

- Cloud deployment (EKS, AKS, Terraform, managed Prometheus or Grafana). These are
  future adapters, listed in `docs/PLAN.md`.
- A distributed shared bucket store (Redis). The store is an interface with an
  in-memory implementation; each replica enforces its own share.
- Authentication and multi-region policy replication.
- A UI beyond Grafana.
- Running inside the full opentelemetry-demo store (stretch goal).

## Success metrics

| Metric | Target |
| --- | --- |
| `/v1/check` p99 latency at 500 RPS on kind | < 25 ms |
| Availability SLO | 99.9% over 30 days, with page and ticket burn-rate alerts |
| Latency SLO | 99% of checks under 100 ms over 30 days |
| HPA reaction | k6 load scales the Deployment from 2 to at least 3 replicas |
| Pod-kill recovery | Ready replica count restored in under 60 s, recorded per run |
| Bad canary | A canary with injected 5xx errors is aborted automatically |
| CI | Unit, HTTP, Helm and kind smoke tests green on every PR; Trivy finds no HIGH/CRITICAL fixable CVEs |
| Setup | One command (`make up` or `docker compose up`) brings up a working stack |
