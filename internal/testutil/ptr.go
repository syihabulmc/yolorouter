package testutil

// Ptr returns a pointer to v. Table-driven tests need addressable literals
// for optional (pointer-typed) struct fields and function arguments, which
// the language otherwise only allows via a named local variable per value.
func Ptr[T any](v T) *T { return &v }
