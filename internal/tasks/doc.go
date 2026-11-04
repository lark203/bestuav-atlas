// Package tasks 负责“异步任务”的调度与执行（纯 Go，不依赖 UI）。
//
// 当前为结构占位，具体实现将在对应功能开发阶段落地。
// 设计约定：本包只放业务逻辑与可独立单测的函数，禁止 import
// github.com/egoist/mygo/ui，以保证脱离窗口也能测试。
package tasks
