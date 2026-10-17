// Package auth 为各类 AI 服务供应商提供认证功能。
// 它包含令牌存储与认证方法的接口和实现。
package auth

// TokenStorage 定义用于存储认证令牌的接口。
// 该接口的实现应提供将认证令牌持久化
// 到文件系统位置的方法。
type TokenStorage interface {
	// SaveTokenToFile 将认证令牌持久化到指定文件路径。
	//
	// 参数：
	//   - authFilePath: 保存认证令牌的文件路径
	//
	// 返回：
	//   - error: 保存操作失败时返回错误，否则为 nil
	SaveTokenToFile(authFilePath string) error
}
