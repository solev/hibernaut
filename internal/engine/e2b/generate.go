package e2b

// Clients generated from E2B's specs, vendored at the pinned commit in third_party/e2b.
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config oapi-codegen.yaml ../../../third_party/e2b/spec/openapi.yml
//go:generate go run github.com/bufbuild/buf/cmd/buf@v1.73.0 generate ../../../third_party/e2b/envd --template buf.gen.yaml
