# gitrdone Sentry

Sentry project details:

- Org: `sky-valley-ambient-computing`
- Project slug: `gitrdone`
- Project ID: `4511632392585216`
- Client key: `a7d68c7c3711e1426d841348664398c0`
- DSN:
  `https://a7d68c7c3711e1426d841348664398c0@o4511632351363072.ingest.us.sentry.io/4511632392585216`

The DSN is a public routing identifier, not a credential. Keep Sentry auth
tokens and deployment secrets out of repo.

Hosted environments:

- Dev: `SENTRY_ENVIRONMENT=dev`, template:
  `deploy/env/gitrdone-dev.env.example`
- Main: `SENTRY_ENVIRONMENT=main`, template:
  `deploy/env/gitrdone-main.env.example`

Deployment should set `SENTRY_RELEASE` to the exact deployed Git SHA. Hosted
templates use `SENTRY_TRACES_SAMPLE_RATE=0.05`; omit or set it to `0` to keep
Sentry to error reporting only.

The process initializes Sentry only when `SENTRY_DSN` is set. Before sending an
event, gitrdone strips request headers, cookies, bodies, and query strings so
control tokens, repo tokens, and database material are not reported.
