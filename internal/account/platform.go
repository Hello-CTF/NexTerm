package account

import (
	"context"
	"crypto/rand"
	"encoding/base64"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

// 平台托管模式（--auth=platform）下的保留账号。
//
// 为什么不复用初始化码：平台模式的第一层门在网关上 —— 请求能到达应用，就说明它已经
// 过了平台的登录门；再让用户去容器控制台抄一次性码，是把同一件事做了两遍。这里刻意
// 不消费初始化码，也不打印任何码。
//
// 为什么仍然要有账号：网关头只能回答"过了门"，回答不了"是谁"。而设备管理、分享链接、
// 审计这些路由的授权都要落到一个具体主体上（fleetGuard 只认会话 cookie，不看网关头）。
// 所以平台模式维持这一个保留账号作为归属主体，并让浏览器始终持有它的会话。
const (
	PlatformOwnerUsername    = "owner"
	PlatformOwnerDisplayName = "平台所有者"
)

// EnsurePlatformOwner 幂等地返回平台所有者账号，不存在时创建之。
//
// 口令由服务端随机生成且绝不外露：平台模式下不存在"用密码登录"这条路，用户永远经网关
// 进入，因此这个口令只是一枚占位符（列上 NOT NULL，且不能让任何人猜中）。
func (a *Accounts) EnsurePlatformOwner(ctx context.Context) (*User, error) {
	existing, err := a.GetUserByUsername(ctx, PlatformOwnerUsername)
	if err == nil {
		return existing, nil
	}
	if ipc.NormalizeError(err).Code != ipc.CodeNotFound {
		return nil, err
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, ipc.WrapError(ipc.CodeCrypto, "加密错误: 平台所有者口令生成失败", err)
	}
	password, err := hashPassword("platform-owner!" + base64.RawURLEncoding.EncodeToString(raw))
	if err != nil {
		return nil, err
	}

	now := a.now()
	owner := &User{
		ID:           ids.New(),
		Username:     PlatformOwnerUsername,
		DisplayName:  PlatformOwnerDisplayName,
		Role:         RoleSuperadmin,
		State:        StateActive,
		CreatedAt:    now,
		UpdatedAt:    now,
		passwordHash: password,
	}
	// 用 INSERT + 唯一索引冲突回读，而不是 upsert 语法：sqlite 的唯一约束在列上、
	// postgres 的建在 lower(username) 表达式上，两边 ON CONFLICT 的目标写法不同。
	if _, err := a.db.ExecContext(ctx, `INSERT INTO `+userTable+`(id, username, display_name, role, password_hash, state, must_change_password, created_at, updated_at)
VALUES(?,?,?,?,?,?,0,?,?)`, owner.ID, owner.Username, owner.DisplayName, string(owner.Role), password, string(owner.State), now, now); err != nil {
		if !store.IsUniqueErr(err) {
			return nil, translateUserWriteError(err)
		}
		// 并发下被另一个请求抢先创建：回读那一行。
	}
	return a.GetUserByUsername(ctx, PlatformOwnerUsername)
}
