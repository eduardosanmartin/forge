package config

// DefaultInferenceCores computes the RNF-1.6 default core budget for a
// machine with numCPU logical cores: max(1, numCPU-2). This leaves a
// 2-core margin for the OS and the user so local inference never reserves
// 100% of the cores by default; machines with 2 or fewer cores clamp to 1
// rather than producing a zero or negative budget.
//
// numCPU is taken as a parameter (instead of reading runtime.NumCPU()
// internally) so tests can exercise the edges deterministically and
// callers can report against an observed CPU count.
func DefaultInferenceCores(numCPU int) int {
	if numCPU <= 2 {
		return 1
	}
	return numCPU - 2
}
