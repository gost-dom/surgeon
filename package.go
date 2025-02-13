// Package surgeon is a tool intended for testing code that involves a larger
// dependency graph. E.g., the root HTTP handler in a web application may well
// have a dependency to everything.
//
// The surgeon can surgically remove specific dependencies in a large dependency
// graph, and install a test double; mock, stub, fake, etc. Installing a "spy"
// on top of an existing component isn't in scope, but could be considered if
// requested.
//
// The surgery will, unlike actual surgery, not affect the original graph, but a
// partial clone. A branch of the dependency tree that isn't affected by the
// surgery will be shared between clones, only components with direct or
// indirect dependencies will be replaced.
//
// The intended use case is when you have an object graphs constructed once
// **during application startup**.
package surgeon
