# Contributing

Thanks for your interest in wb2api. This project wraps Tencent CodeBuddy /
WorkBuddy subscriptions into an OpenAI-compatible API; it is **unofficial**,
for **personal study only**, and requires the user to hold a valid
subscription. Keep that framing in everything you contribute.

## Ground rules (read AGENTS.md first)

AGENTS.md is the project charter. Highlights:

- `core/` must stay pure stdlib and offline-testable — it may **not** import
  any CLIProxyAPI package.
- `cmd/plugin` is a thin ABI shell only: registration/adapter glue, no
  protocol logic. Protocol logic lives in `core/`.
- Never commit anything from `third_party/` into this repository or its git
  history.
- The MIT-licensed references (`CLIProxyAPI`, `workbuddy-cliproxy`) may be
  studied and followed; large borrowings must keep attribution in NOTICE.md.
- Unlicensed references (`workbuddy2api`, `cpa-plugin`) must **not** be copied
  or line-translated — behavior may only be re-implemented independently.
  Do not open their `.go` sources at all.
- Protocol claims are ranked: your own capture records in `specs/` first,
  then observed behavior of MIT references, then community docs. Record new
  captures under `specs/` and note conflicts there.

## Development

```bash
go build ./... && go vet ./...   # root module (core + cmd/server)
go test ./...                    # offline unit tests (mocked upstream)
```

Plugin builds need Go 1.26+ and gcc (see README).

## Pull requests

- Keep changes focused; explain the reasoning in the description.
- If your change touches upstream protocol facts (headers, body rewrites,
  endpoints), reference the capture/spec it is based on.
- Run the root-module checks above before pushing. CI runs them again and
  also compiles the plugin.

## Reporting issues

- Bug reports: include the request/response shape (redact tokens!) and which
  client (server or CPA plugin) you used.
- Never paste live credentials or real JWT contents into issues.
- Feature ideas are welcome, but understand the project is unofficial and
  may decline anything that risks ToS violations or cache/anti-abuse
  circumvention beyond personal study.
