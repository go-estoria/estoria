package lifecycle

// ProcessorReturnedForTest exposes the handle's unserialized return
// observation: the channel closes immediately after the processor's return
// has claimed its exit order, before publication takes the handle lock —
// deliberately lock-free, so it stays observable while a test holds the
// handle's lock open through a parked save. Callers must already have
// synchronized with the run's start (waited on any post-start signal), which
// orders the field's one write before this read. Regressions that stage a
// processor death and then act on the handle wait on it, so their subsequent
// actions provably follow the return's claim — the in-handler failure signal
// alone closes before the processor's Run returns and orders nothing.
func ProcessorReturnedForTest(r *Rebuild) <-chan struct{} {
	return r.processorReturned
}
