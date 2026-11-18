# Terraform Provider for Autter

Manage [Autter](https://autter.dev) alert rules, synthetic checks and Runtime dashboards as code, through the
Autter API (`/api/v1`).

- Registry: [`Autter-dev/autter`](https://registry.terraform.io/providers/Autter-dev/autter/latest/docs)
- Resources: `autter_alert_rule`, `autter_synthetic_check`, `autter_dashboard`
- Data sources: `autter_repository`, `autter_repositories`

```terraform
terraform {
  required_providers {
    autter = {
      source  = "Autter-dev/autter"
      version = "~> 0.1"
    }
  }
}

provider "autter" {} # reads AUTTER_API_TOKEN

data "autter_repository" "web" {
  full_name = "acme/web"
}

resource "autter_alert_rule" "checkout_errors" {
  repository_id = data.autter_repository.web.id
  name          = "Checkout errors"
  triggers      = ["new", "regressed"]
  filters       = { services = ["checkout"] }
  actions       = { slack = {}, oncall = true }
}
```

Create a personal access token in the Autter dashboard under **Settings → Access Tokens**, with the `api:read`
and `api:write` scopes, and set it in `AUTTER_API_TOKEN`. `AUTTER_BASE_URL` (or `base_url`) points the provider
at another Autter API host; it defaults to `https://api.autter.dev`.

Every resource in the Autter dashboard has **Export as Terraform**, which writes configuration for this
provider; see [the export guide](docs/guides/export-from-autter.md). The full documentation is in [`docs/`](docs/).

## Requirements

- [Terraform](https://developer.hashicorp.com/terraform/downloads) 1.5 or later (`import` blocks), or OpenTofu.
- [Go](https://go.dev/doc/install) 1.27 to build the provider.

## Developing

```sh
make build      # go build ./...
make lint       # golangci-lint v2
make test       # unit tests
make testacc    # acceptance tests (TF_ACC=1) against an in-memory mock of the API; needs terraform, no secrets
make generate   # docs/ from the schemas, templates/ and examples/ (tfplugindocs, pinned in tools/go.mod)
```

The design in brief:

- `internal/client` calls the API. Writes go one at a time (the API allows 10 writes a minute per token), a
  `429` is retried after `Retry-After`, and list reads are cached for a minute so a refresh costs one read per
  repository and resource type.
- `internal/provider` holds the resources. A resource is a whole-resource `PUT` by (repository, name). State
  keeps what the configuration says; the resource's private state keeps what the API returned per field after
  the last apply (`snapshot.go`). A refresh compares the two, so the server's normalization never shows as a
  diff and a change made outside Terraform does.
- `internal/mockapi` is the in-memory API the acceptance tests run against.

### Keeping up with the API

The API and its "Export as Terraform" live in the Autter monorepo. After either changes, copy their contract
files and run the tests:

```sh
scripts/sync-from-monorepo.sh ../autter-monorepo
make test testacc
```

This copies `docs/openapi/autter-v1.yaml` (checked by `openapi_test.go`: an API input field the provider does
not manage fails the test, because an apply would reset it) and the export fixtures
`backend/src/routes/public-api/testdata/terraform-export/*.{tf,json}` (applied and imported by
`export_contract_test.go`).

### Trying a local build

Build and install the provider, then point Terraform at it with a CLI configuration file:

```hcl
# autter-dev.tfrc; replace the path with the output of `go env GOPATH` followed by /bin
provider_installation {
  dev_overrides {
    "autter-dev/autter" = "/home/you/go/bin"
  }
  direct {}
}
```

```sh
go install .
TF_CLI_CONFIG_FILE=autter-dev.tfrc terraform -chdir=examples/resources/autter_alert_rule plan
```

## Releasing

Releases are built by GoReleaser from a `v*` tag (`.github/workflows/release.yml`): zips per platform, a
`SHA256SUMS` file signed with the release GPG key, and the registry manifest. The repository needs the secrets
`GPG_PRIVATE_KEY` and `PASSPHRASE`, and the key's public half registered with the Terraform Registry for the
`Autter-dev` namespace. Update `CHANGELOG.md` and sync from the monorepo before tagging.

## License

[MPL-2.0](LICENSE).
