package sync

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func linkPatch(url, username, password string) LinkPatch {
	return LinkPatch{URL: testPtr(url), Username: testPtr(username), Password: testPtr(password)}
}

func TestLinkSetConnectedCachesSession(t *testing.T) {
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)

	result, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL, "alice", "alice-pw-123"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Connect != LinkConnectConnected {
		t.Fatalf("connect = %q, want connected", result.Connect)
	}
	if result.VerifiedAt == 0 || result.LastError != "" {
		t.Fatalf("probe not recorded: %+v", result.Link)
	}

	// 会话已在 LinkSet 时建立: 远端调用不再重登。
	if _, err := instance.service.Remote().ListDevices(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fixture.logins.Load(); got != 1 {
		t.Fatalf("logins = %d, want 1 (LinkSet probe session reused)", got)
	}
}

func TestLinkSetBadPasswordRecordsError(t *testing.T) {
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)

	// 先存后验: 探测失败不回滚保存、不报错, 状态为空且 LastError 落盘。
	result, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL, "alice", "wrong-pw"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Connect != "" || result.LastError == "" {
		t.Fatalf("result = %+v, want empty connect with recorded error", result)
	}

	link, err := instance.service.LinkGet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !link.IsConfigured() || link.LastError == "" {
		t.Fatalf("link = %+v, want configured with recorded error", link)
	}
}

func TestLinkMFAFlow(t *testing.T) {
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	secret := fixture.bindTOTP(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)
	ctx := context.Background()

	result, err := instance.service.LinkSet(ctx, linkPatch(fixture.URL, "alice", "alice-pw-123"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Connect != LinkConnectMFARequired || result.MFATicket == "" || result.MFAExpiresAt == 0 {
		t.Fatalf("connect = %+v, want mfa_required with ticket", result)
	}

	// 错误验证码被拒且不消费票据: 同一票据重试正确动态码必须成功(与服务端语义一致)。
	if _, err := instance.service.LinkMFA(ctx, "000000"); err == nil {
		t.Fatal("wrong TOTP code must fail")
	} else {
		requireCode(t, err, ipc.CodeForbidden)
	}
	result, err = instance.service.LinkMFA(ctx, testTOTPCode(t, secret, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if result.Connect != LinkConnectConnected {
		t.Fatalf("connect = %q, want connected", result.Connect)
	}

	// 会话(cookie+CSRF)内存持有: 远端读与 CSRF 写都直接可用, 且不再重登。
	if _, err := instance.service.Remote().ListDevices(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.service.Remote().PutBaseURLs(ctx, []RemoteBaseURLEntry{{URL: "https://a.example.com"}}); err != nil {
		t.Fatal(err)
	}
	if got := fixture.logins.Load(); got != 1 {
		t.Fatalf("logins = %d, want 1 (MFA 完成复用票据会话, 不重登)", got)
	}
}

func TestLinkResetRequiredFlow(t *testing.T) {
	fixture := newRemoteAPIFixture(t)
	userID := fixture.createUser(t, "alice", "temp-pw-123")
	if err := fixture.accounts.AdminResetUser(context.Background(), userID); err != nil {
		t.Fatal(err)
	}
	instance := newTestInstance(t, true)
	ctx := context.Background()

	result, err := instance.service.LinkSet(ctx, linkPatch(fixture.URL, "alice", "temp-pw-123"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Connect != LinkConnectResetRequired {
		t.Fatalf("connect = %q, want reset_required", result.Connect)
	}

	// 强制改密入职: 用新口令签发全新 DEK 信封, 恢复密钥只在响应里下发一次。
	result, err = instance.service.LinkResetPassword(ctx, "brand-new-pw-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Connect != LinkConnectConnected || result.RecoveryKey == "" {
		t.Fatalf("connect = %+v, want connected with one-shot recovery key", result)
	}

	// 受限会话被服务端保留为正式会话: 读与 CSRF 写直接可用, 全程只登录一次。
	if _, err := instance.service.Remote().ListDevices(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.service.Remote().PutBaseURLs(ctx, []RemoteBaseURLEntry{{URL: "https://a.example.com"}}); err != nil {
		t.Fatal(err)
	}
	if got := fixture.logins.Load(); got != 1 {
		t.Fatalf("logins = %d, want 1 (reset 完成复用受限会话, 不重登)", got)
	}

	// 中间态已消费: 重复提交新密码必须拒绝。
	if _, err := instance.service.LinkResetPassword(ctx, "another-pw-1"); err == nil {
		t.Fatal("consumed reset state must reject a second password")
	} else {
		requireCode(t, err, ipc.CodeBadParam)
	}

	// 保存的口令已更新为新口令: 不带口令重新保存(合并既有口令)必须直接 connected。
	link, err := instance.service.LinkGet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if link.VerifiedAt == 0 {
		t.Fatalf("probe not recorded after reset: %+v", link)
	}
	result, err = instance.service.LinkSet(ctx, LinkPatch{URL: testPtr(fixture.URL), Username: testPtr("alice")})
	if err != nil {
		t.Fatal(err)
	}
	if result.Connect != LinkConnectConnected {
		t.Fatalf("re-connect = %q, want connected with updated password", result.Connect)
	}
}

func TestRemoteProxyRequiresDesktop(t *testing.T) {
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true, WithMetadata("test", false))

	if _, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL, "alice", "alice-pw-123")); err != nil {
		t.Fatal(err)
	}
	_, err := instance.service.Remote().ListDevices(context.Background())
	requireCode(t, err, ipc.CodeUnsupported)
}
