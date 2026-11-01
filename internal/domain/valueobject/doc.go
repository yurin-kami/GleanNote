/*
 * Package valueobject 定义跨实体复用的值对象与输入校验规则。
 *
 * 职责：邮箱、密码、昵称等字段的归一化与合法性判定，错误一律为 errs 业务错误。
 * 不负责：持久化、加密（哈希由端口实现）、与 HTTP 参数绑定。
 * 依赖：domain/errs 与标准库；不得导入任何框架或基础设施包。
 * 参考：docs/ARCHITECTURE.md#5-构建块视图、docs/API.md#512-注册
 */
package valueobject
