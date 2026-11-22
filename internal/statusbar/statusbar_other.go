//go:build !darwin

package statusbar

type noopController struct{}

// NewController 在非 macOS 平台返回零开销空实现，保障跨平台无 CGO 编译。
func NewController(_ Callbacks) Controller {
	return noopController{}
}

// SetSystemWakeCallback 在非 macOS 平台为空实现。
func SetSystemWakeCallback(_ func()) {}

// SetPreventAppNap 在非 macOS 平台为空实现。
func SetPreventAppNap(_ bool) {}

func (noopController) Start(_ bool)                   {}
func (noopController) SetCloseToBackground(_ bool)    {}
func (noopController) SetSystemWakeCallback(_ func()) {}
func (noopController) SetPreventAppNap(_ bool)        {}
func (noopController) Update(_ Snapshot)              {}
func (noopController) Stop()                          {}
