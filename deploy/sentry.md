# gitrdone Sentry

gitrdone can report sanitized errors to Sentry when `SENTRY_DSN` is configured.
Leave `SENTRY_DSN` unset to disable Sentry reporting.

The DSN is a public routing identifier, not a credential, but deployment-specific
DSNs and Sentry project details should stay in your environment configuration.
Keep Sentry auth tokens and deployment secrets out of repo.

Hosted environments:

- Development: `SENTRY_ENVIRONMENT=dev`, template:
  `deploy/env/gitrdone-dev.env.example`
- Production: `SENTRY_ENVIRONMENT=main`, template:
  `deploy/env/gitrdone-main.env.example`

Deployment should set `SENTRY_RELEASE` to the exact deployed Git SHA. Hosted
templates use `SENTRY_TRACES_SAMPLE_RATE=0.05`; omit or set it to `0` to keep
Sentry to error reporting only.

The process initializes Sentry only when `SENTRY_DSN` is set. Before sending an
event, gitrdone strips request headers, cookies, bodies, and query strings so
control tokens, repo tokens, and database material are not reported.
