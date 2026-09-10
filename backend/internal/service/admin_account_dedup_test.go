//go:build unit

// FindDuplicateAPIKeys 单元测试：创建账号前的同平台 API Key 查重逻辑
package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// dedupAccountRepoStub 仅覆写 ListAllWithFilters 的仓储桩；其余方法经嵌入接口不会被调用。
// capturedXxx 记录查询参数，用于断言“按平台 + apikey 类型 + 不限状态”的查询语义。
type dedupAccountRepoStub struct {
	AccountRepository
	accounts          []Account
	listErr           error
	called            bool
	capturedPlatform  string
	capturedType      string
	capturedStatus    string
}

func (s *dedupAccountRepoStub) ListAllWithFilters(_ context.Context, platform, accountType, status, _ string, _ int64, _ string) ([]Account, error) {
	s.called = true
	s.capturedPlatform = platform
	s.capturedType = accountType
	s.capturedStatus = status
	return s.accounts, s.listErr
}

func newDedupAdminService(repo *dedupAccountRepoStub) *adminServiceImpl {
	return &adminServiceImpl{accountRepo: repo}
}

func TestFindDuplicateAPIKeys_MatchesAccountsAcrossAllStatuses(t *testing.T) {
	repo := &dedupAccountRepoStub{accounts: []Account{
		// 同密钥第二个账号（ID 更大，故意排在最前）：必须仍取 ID 更小的账号，
		// 验证同密钥多账号时的确定性选择不依赖仓储返回顺序
		{ID: 4, Name: "智谱-重复", Status: StatusActive, Credentials: map[string]any{"api_key": " key-a "}},
		// 停用账号：同样参与查重（防止停用后重新录入同一密钥被静默放行）
		{ID: 2, Name: "智谱-停用", Status: StatusDisabled, Credentials: map[string]any{"api_key": "key-disabled"}},
		// apikey 但凭据为空：跳过
		{ID: 3, Name: "empty", Status: StatusActive, Credentials: map[string]any{}},
		// 活跃账号：凭据命中（同密钥 key-a 中 ID 最小，应被选中）
		{ID: 1, Name: "智谱-1", Status: StatusActive, Credentials: map[string]any{"api_key": "key-a"}},
	}}
	svc := newDedupAdminService(repo)

	hits, err := svc.FindDuplicateAPIKeys(context.Background(), PlatformZhipu, []string{"key-a", "key-disabled", "key-b", "key-a", " key-a "})
	require.NoError(t, err)
	require.Len(t, hits, 2)
	require.Equal(t, "key-a", hits[0].APIKey)
	require.Equal(t, int64(1), hits[0].AccountID)
	require.Equal(t, "智谱-1", hits[0].AccountName)
	require.Equal(t, "key-disabled", hits[1].APIKey)
	require.Equal(t, "智谱-停用", hits[1].AccountName)

	// 查询必须按平台 + apikey 类型，且不限账号状态（status 空 = 不过滤）
	require.True(t, repo.called)
	require.Equal(t, PlatformZhipu, repo.capturedPlatform)
	require.Equal(t, AccountTypeAPIKey, repo.capturedType)
	require.Equal(t, "", repo.capturedStatus)
}

func TestFindDuplicateAPIKeys_NoDuplicatesReturnsEmpty(t *testing.T) {
	repo := &dedupAccountRepoStub{accounts: []Account{
		{ID: 9, Name: "other", Status: StatusActive, Credentials: map[string]any{"api_key": "zzz"}},
	}}
	svc := newDedupAdminService(repo)

	hits, err := svc.FindDuplicateAPIKeys(context.Background(), PlatformKimi, []string{"key-a", "key-b"})
	require.NoError(t, err)
	require.Empty(t, hits)
}

func TestFindDuplicateAPIKeys_EmptyInputSkipsRepo(t *testing.T) {
	repo := &dedupAccountRepoStub{}
	svc := newDedupAdminService(repo)

	hits, err := svc.FindDuplicateAPIKeys(context.Background(), PlatformZhipu, nil)
	require.NoError(t, err)
	require.Nil(t, hits)
	require.False(t, repo.called, "空入参不应触发仓储查询")
}

func TestFindDuplicateAPIKeys_PropagatesRepoError(t *testing.T) {
	repo := &dedupAccountRepoStub{listErr: errors.New("db down")}
	svc := newDedupAdminService(repo)

	_, err := svc.FindDuplicateAPIKeys(context.Background(), PlatformZhipu, []string{"key-a"})
	require.ErrorContains(t, err, "db down")
}
