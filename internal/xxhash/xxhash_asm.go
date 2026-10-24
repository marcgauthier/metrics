//go:build (amd64 || arm64) && !appengine && gc && !purego
// +build amd64 arm64
// +build !appengine
// +build gc
// +build !purego

package xxhash

// Sum64 computes the 64-bit xxHash digest of b with a zero seed.
//
//go:noescape
func Sum64(b []byte) uint64

// Note: the first parameter is named s (not d) because the amd64 assembly
// uses `d` as a register macro and refers to this parameter as s+0(FP).
// This fixes a vet failure present in upstream v2.3.0; codegen is unchanged.
//
//go:noescape
func writeBlocks(s *Digest, b []byte) int
