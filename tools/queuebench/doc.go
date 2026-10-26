// Command queuebench compares River with a Valkey Streams queue on the same
// workload: a producer transaction writes a business row and enqueues a job,
// and a worker runs the job (nothing, or one UPDATE of that row).
//
// Systems:
//
//	river   InsertTx in the producer's transaction, as Tanvo enqueues.
//	stream  commit, then XADD: not atomic with the row (an upper bound).
//	outbox  the row and an outbox row in one transaction; relays move the
//	        outbox to the stream (what a transactional Redis queue costs).
//
// Each run prints one line: the enqueue and end-to-end rates and the
// latency quantiles from enqueue to the end of the job. run.sh starts the
// containers and runs the matrix of docs/benchmarks.md §1.
package main
