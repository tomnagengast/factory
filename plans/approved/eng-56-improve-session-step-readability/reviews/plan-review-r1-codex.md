P1-01: Correlated Claude updates do not define how both raw source events survive. Plan lines 10, 34, and 40 promise complete redacted raw diagnostics, while lines 35 and 94 collapse `tool_use` and `tool_result` into one stable row. The current contract has only one `Payload` string (`internal/agentrun/observer.go:59-65`), creates it from one JSONL line (`observer.go:411-427`), and replaces the entire step on an ID collision (`observer.go:401-403`). Following the plan therefore risks discarding either the tool-use record or its result. The verification at plan line 156 checks normalized output/error but not preservation of both raw records. Smallest correction: define how a correlated row retains ordered, redacted raw evidence from both records, whether through a specified payload encoding or an additive raw-events field, and require a test proving distinct markers from both source records remain inspectable.

P2/P3: None.

VERDICT: REVISE
BLOCKERS: P1-01
