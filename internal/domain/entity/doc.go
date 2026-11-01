/*
 * Package entity 定义领域实体及其状态迁移规则。
 *
 * 职责：维护实体不变量（昵称、密码哈希、验证码消费、令牌轮换、目录根节点），
 * 非法迁移一律返回 errs 业务错误，副作用只修改实体自身字段。
 * 不负责：持久化、ID 与哈希生成、事务边界；实体不得发起任何 IO。
 * 依赖：domain/valueobject、domain/errs 与标准库。
 * 参考：docs/ARCHITECTURE.md#54-领域对象与状态机
 */
package entity
