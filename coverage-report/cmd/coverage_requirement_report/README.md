# Requirement coverage report

`coverage_requirement_report` turns one QA test window into one or more
source-correct coverage segments.

If a service is redeployed while requirement A is still being tested, the tool
uses the automatically captured deployment boundaries:

```text
A start -> old deployment prestop
new deployment start -> A finish
```

Each segment is subtracted only inside the same `build_sha`, `deployment_id`,
and PID. The generated index links to a separate Go HTML coverage report for
each exact source revision. Counters from incompatible binaries are never
merged. Legacy or incomplete snapshots elsewhere in the shared retention root
are ignored; either requested boundary must itself be a complete, attributed
snapshot.

```sh
go run ./tools/coverage_requirement_report \
  -repo . \
  -snapshots /var/lib/jenkins/coverage-sim-deploy/releases \
  -start-id requirement_a_cross_deploy_start \
  -finish-id requirement_a_cross_deploy_finish \
  -out /path/to/report
```

The output contains `index.html`, `report.json`, and one directory per
deployment segment with `coverage.out`, function totals, exact historical
source files, and `coverage.html`.
