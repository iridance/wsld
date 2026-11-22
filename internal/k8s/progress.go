package k8s

import "context"

// 啟動流程的步驟（GUI 的 Enabling 清單依此順序顯示）。
const (
	StepEngine  = "engine"  // 確認 dockerd 在跑
	StepInstall = "install" // 安裝 k3s（已安裝則略過）
	StepNode    = "node"    // 啟動 k3s 並等 node Ready
	StepContext = "context" // 把 context 合併進 Windows kubeconfig（由 GUI 層回報）
)

// Steps 是啟動流程步驟的固定順序。
var Steps = []string{StepEngine, StepInstall, StepNode, StepContext}

type progressKey struct{}

// WithProgress 回傳會讓 Start 在進入各步驟時呼叫 fn 的 context。
func WithProgress(ctx context.Context, fn func(step string)) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

// ReportStep 回報目前步驟；ctx 沒帶 WithProgress 時為空操作。
func ReportStep(ctx context.Context, step string) {
	if fn, ok := ctx.Value(progressKey{}).(func(string)); ok && fn != nil {
		fn(step)
	}
}
