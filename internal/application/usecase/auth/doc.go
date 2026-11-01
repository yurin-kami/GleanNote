/*
 * Package auth 编排账号与认证用例。
 *
 * 职责：验证码发送、注册、登录、刷新、登出、重置密码、账号资料与注销；
 * 负责事务边界、限流与锁定、令牌轮换，并调用领域实体维护不变量。
 * 不负责：SQL 与 Redis 细节、HTTP 参数解析、Docker/供应商选择。
 * 依赖方向：本包依赖 application/port 与 domain，不引用 infrastructure 与 transport。
 * 边界外的副作用：注册时创建根目录属于账号初始化，通过 FolderWriter 完成；
 * 目录树的其他操作归 folder 用例。
 * 参考：docs/API.md#51-账号与认证、docs/ARCHITECTURE.md#61-注册与登录
 */
package auth
