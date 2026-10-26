// Command engine is the playground's in-browser engine; it builds only for
// GOOS=js (see main_js.go). On other platforms it is an empty program so
// that `go build ./...` and `go vet ./...` stay green.
package main
