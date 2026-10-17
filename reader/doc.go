// Package reader defines the read-only GraphReader contract and the in-memory
// MemoryReader fixture implementation for SemSparql query planning and
// evaluation. All entity IDs, literal prefixes, and six-position wildcard
// patterns are validated at entry using SemEngine's shared pkg/types
// validators, and all scans enforce explicit entity-read budgets without silent
// truncation.
package reader
