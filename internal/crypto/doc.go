// Package crypto 负责“公钥私钥 / 生成用户密码”等密码学相关功能（纯 Go）。
//
// 当前为结构占位，具体实现将在对应功能开发阶段落地。
// 设计约定：本包只放业务逻辑与可独立单测的函数，禁止 import
// github.com/egoist/mygo/ui，以保证脱离窗口也能测试。
package crypto
