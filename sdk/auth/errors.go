package auth

// EmailRequiredError 表示调用上下文必须提供邮箱或别名。
type EmailRequiredError struct {
	Prompt string
}

func (e *EmailRequiredError) Error() string {
	if e == nil || e.Prompt == "" {
		return "cliproxy auth: email is required"
	}
	return e.Prompt
}
