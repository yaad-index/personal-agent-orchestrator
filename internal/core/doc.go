// Package core holds pao's engine: the domain types and the collaborator
// interfaces every other package implements or consumes. It depends on nothing
// concrete (no terminal driver, no transport, no store), so each of those stays
// swappable (ADR-0002).
package core
