package resourcecontrol

type WarpLogLevel string

const (
	WarpLogLevelInherit WarpLogLevel = "inherit"
	WarpLogLevelWarning WarpLogLevel = "warning"
	WarpLogLevelInfo    WarpLogLevel = "info"
	WarpLogLevelDebug   WarpLogLevel = "debug"
)

func IsValidWarpLogLevel(level WarpLogLevel) bool {
	switch level {
	case WarpLogLevelInherit, WarpLogLevelWarning, WarpLogLevelInfo, WarpLogLevelDebug:
		return true
	default:
		return false
	}
}

type WarpLogRequest struct {
	Level    WarpLogLevel `json:"level"`
	Revision string       `json:"revision"`
}

type WarpLogStatus struct {
	Supported      bool         `json:"supported"`
	Reason         string       `json:"reason,omitempty"`
	Level          WarpLogLevel `json:"level"`
	EffectiveLevel string       `json:"effective_level,omitempty"`
	Revision       string       `json:"revision"`
}

var ErrWarpLogConflict = messageError("WARP logging settings changed elsewhere. Refresh settings before saving.")
