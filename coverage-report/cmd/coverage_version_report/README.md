# Coverage version report

`coverage_version_report` inventories completed runtime coverage snapshots and
binds every snapshot to the exact Git commit and deployment that produced it.
It never merges snapshots from different source revisions into one coverage
result.

```bash
go run ./tools/coverage_version_report \
  -repo /path/to/repository \
  -snapshots /path/to/releases \
  -final-sha "$FINAL_SHA" \
  -out coverage-version-report.json
```

For each historical deployment, the JSON report lists source files that were
added, modified, removed, renamed, copied, or type-changed before the final
revision. A legacy snapshot without `build_sha` and `deployment_id`, a snapshot
whose Git commit is unavailable, or an incomplete snapshot is rejected and
causes a non-zero exit status.

This tool establishes source-version validity. Jira test-window selection and
coverage aggregation within compatible revisions belong to the higher-level QA
workflow and must be performed after this check.
