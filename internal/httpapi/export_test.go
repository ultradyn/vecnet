package httpapi

// ActiveStreams reports the SSE handlers currently running.
func ActiveStreams() int64 { return activeStreams.Load() }
