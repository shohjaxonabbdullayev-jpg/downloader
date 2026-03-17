package platforms

import "errors"

// ErrEngineUnavailable indicates the engine cannot run in the current environment
// (e.g. missing python binary). The pipeline should skip retries for this engine.
var ErrEngineUnavailable = errors.New("engine unavailable")

