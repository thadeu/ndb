module github.com/thadeu/ndb/sdk/go/ndbredis

go 1.26.10

require (
	github.com/redis/go-redis/v9 v9.23.0
	github.com/thadeu/ndb/sdk/go v1.23.1
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
)

// For work in this repo. A program that imports the module does not see
// this line: it gets the required version of the SDK.
replace github.com/thadeu/ndb/sdk/go => ../
