// Package contract holds structural guards for SemSparql's agent setup,
// rule-index parity, dependency pins, and verification gates. Each guard
// carries a paired sensitivity test that plants violations in a temporary tree
// and verifies the guard fails naming the planted file and rule.
package contract
