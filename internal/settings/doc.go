// Package settings 负责应用设置的持久化（读写配置文件，纯 Go）。
//
// 当前为结构占位，具体实现将在对应功能开发阶段落地。
// 设计约定：本包只放业务逻辑与可独立单测的函数，禁止 import
// github.com/egoist/mygo/ui，以保证脱离窗口也能测试；UI 层通过本包
// 暴露的接口读写设置，不直接操作配置文件。
package settings
