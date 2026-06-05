# E2E examples

Manual end-to-end examples for workflows that touch real Entire services.

These are **not run by CI**. They are intended as copy/pasteable golden paths for release verification and debugging. Run them from a git repository with an `origin` remote that resolves to a repo with trails, and authenticate first with `entire login`.

## Trail finding golden path

```bash
./e2e/examples/trail-finding-golden-path.sh
```

Optional environment variables:

- `ENTIRE_BIN`: path to the CLI binary, defaults to `entire`
- `ENTIRE_TRAIL_FINDING_SELECTOR`: trail number, id, or branch; defaults to the current branch's trail
- `ENTIRE_TRAIL_FINDING_FILE`: file path for the example finding, defaults to `README.md`
- `ENTIRE_TRAIL_FINDING_LINE`: line for the example finding, defaults to `1`

The script exercises the intended user/agent path:

1. discover trails with `entire trail list --status any`
2. show finding dashboard for current branch/default target
3. create a trail-scoped finding with a `client_id`
4. list findings
5. show the created finding
6. resolve the created finding
