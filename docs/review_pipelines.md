# Review pipelines

Orpheus selects a task review pipeline in this order:

1. `orpheus task review --pipeline <name-or-alias> <task-id>`
2. the repository `review-pipeline` config value
3. global `reviews.default_pipeline` in Orpheus `config.yaml`
4. the built-in `default` manual local-review pipeline

Global pipelines are defined under `reviews.pipelines` in Orpheus config:

```yaml
reviews:
  default_pipeline: standard
  pipelines:
    standard:
      steps:
        - kind: manual
          name: local-review
    go:
      steps:
        - kind: check
          name: test
          command: make
          args: ["test"]
```

## Repository default pipeline

Inspect a repository's stored and effective review pipeline:

```bash
orpheus repo config get my-repo review-pipeline
```

Set the repository default pipeline to a named global pipeline:

```bash
orpheus repo config set my-repo review-pipeline go
```

Clear the repository default and return to global or built-in fallback behavior:

```bash
orpheus repo config set my-repo review-pipeline ''
```

`repo config set` rejects unknown pipeline names before it updates the registry.

## Repository pipeline aliases

Aliases are repo-local CLI shorthand for named global pipelines. They do not define inline repo-local steps.

Create or replace an alias:

```bash
orpheus repo config set my-repo review-pipeline-alias.quick go
```

Use the alias when reviewing a task in that repository:

```bash
orpheus task review --pipeline quick my-task
```

The review state records the resolved pipeline name, such as `go`, not the alias.

Delete an alias by setting it to an empty value:

```bash
orpheus repo config set my-repo review-pipeline-alias.quick ''
```

Show all configured repository values, including aliases:

```bash
orpheus repo config get my-repo
```
