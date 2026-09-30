# E2B files

Copied unchanged from [e2b-dev/infra](https://github.com/e2b-dev/infra) at commit
`5cfcf3282ddb95e90a8214ce71329641bdfe6cb6` (2026-09-29). That's the commit `vm/install-e2b.sh`
installs on the VM (`E2B_INFRA_REF`), so the clients always match the server. Apache License 2.0;
see [LICENSE](LICENSE).

| Here | Upstream | Generates (`make generate`) |
| --- | --- | --- |
| `spec/openapi.yml` | `spec/openapi.yml` | The REST client, `internal/engine/e2b/e2bapi` |
| `envd/process/process.proto` | `packages/envd/spec/process/process.proto` | envd's process client, `internal/engine/e2b/envdpb` |
| `LICENSE` | `LICENSE` | |

## Updating E2B

1. Set `E2B_INFRA_REF` in `vm/install-e2b.sh` to the new commit.
2. Copy the files above from it:

   ```sh
   base=https://raw.githubusercontent.com/e2b-dev/infra/<commit>
   curl -fsSo third_party/e2b/spec/openapi.yml "$base/spec/openapi.yml"
   curl -fsSo third_party/e2b/envd/process/process.proto "$base/packages/envd/spec/process/process.proto"
   curl -fsSo third_party/e2b/LICENSE "$base/LICENSE"
   ```

3. Update the commit and date above, then run `make generate lint test`.
4. `make e2e-slow` reinstalls E2B on the VM at the new commit and checks it end to end.
