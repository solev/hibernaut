package linkproxy

// unscaled does nothing on Linux, where a receive buffer set by hand does not grow, so it keeps
// the receive window small on its own.
func unscaled(uintptr) error { return nil }
