// Package proto holds the wire contracts bankferry's programs share with
// other projects. The generated Go code is committed, so building and
// testing need no protoc; regenerate only when a .proto changes.
//
// plaid_snapshot.proto is the investments contract between brokerferry and
// finance2, and this copy is the primary source: a change lands here first,
// and the same pull request notes that finance2's verbatim clone needs it
// too. Bump schema_version on any breaking change.
//
// Regenerating needs protoc on PATH (finance2 compiles the same contract
// with protoc 4.34.1, which its Gradle build caches) and protoc-gen-go at
// the version pinned in go.mod:
//
//	go build -o "$(go env GOPATH)/bin/protoc-gen-go" google.golang.org/protobuf/cmd/protoc-gen-go
//	go generate ./proto/
package proto

//go:generate protoc --proto_path=.. --go_out=.. --go_opt=module=github.com/jeffbstewart/bankferry proto/plaid_snapshot.proto
