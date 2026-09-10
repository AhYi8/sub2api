//go:build unit

// CheckAPIKeysDuplicate handler 单元测试：参数校验与响应结构
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type checkAPIKeysAdminServiceStub struct {
	service.AdminService
	platform string
	apiKeys  []string
	hits     []service.DuplicateAPIKeyHit
	err      error
	called   bool
}

func (s *checkAPIKeysAdminServiceStub) FindDuplicateAPIKeys(_ context.Context, platform string, apiKeys []string) ([]service.DuplicateAPIKeyHit, error) {
	s.called = true
	s.platform = platform
	s.apiKeys = apiKeys
	return s.hits, s.err
}

func setupCheckAPIKeysRouter(svc *checkAPIKeysAdminServiceStub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router.POST("/api/v1/admin/accounts/check-api-keys-duplicate", handler.CheckAPIKeysDuplicate)
	return router
}

func postCheckAPIKeys(router *gin.Engine, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/check-api-keys-duplicate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	return rec
}

func TestCheckAPIKeysDuplicateReturnsHits(t *testing.T) {
	svc := &checkAPIKeysAdminServiceStub{hits: []service.DuplicateAPIKeyHit{
		{APIKey: "key-a", AccountID: 7, AccountName: "智谱-1"},
	}}
	rec := postCheckAPIKeys(setupCheckAPIKeysRouter(svc), `{"platform":"zhipu","api_keys":[" key-a ","key-b","key-a"]}`)

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, svc.called)
	require.Equal(t, "zhipu", svc.platform)
	// 入参应被 trim、过滤空串后传入 service（去重由 service 层负责）
	require.Equal(t, []string{"key-a", "key-b", "key-a"}, svc.apiKeys)

	var body struct {
		Data struct {
			Duplicates []service.DuplicateAPIKeyHit `json:"duplicates"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Data.Duplicates, 1)
	require.Equal(t, "key-a", body.Data.Duplicates[0].APIKey)
	require.Equal(t, int64(7), body.Data.Duplicates[0].AccountID)
	require.Equal(t, "智谱-1", body.Data.Duplicates[0].AccountName)
}

func TestCheckAPIKeysDuplicateRejectsInvalidPlatform(t *testing.T) {
	svc := &checkAPIKeysAdminServiceStub{}
	rec := postCheckAPIKeys(setupCheckAPIKeysRouter(svc), `{"platform":"unknown","api_keys":["key-a"]}`)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.False(t, svc.called)
}

func TestCheckAPIKeysDuplicateRejectsEmptyKeys(t *testing.T) {
	svc := &checkAPIKeysAdminServiceStub{}
	rec := postCheckAPIKeys(setupCheckAPIKeysRouter(svc), `{"platform":"zhipu","api_keys":[]}`)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.False(t, svc.called)
}

func TestCheckAPIKeysDuplicateAllBlankKeysReturnsEmptyWithoutRepo(t *testing.T) {
	// 全部为纯空白串（binding 的 dive,required 只拦截空字符串，空白串放行）：
	// handler trim 过滤后为空，直接返回空列表，不触发 service 查询
	svc := &checkAPIKeysAdminServiceStub{}
	rec := postCheckAPIKeys(setupCheckAPIKeysRouter(svc), `{"platform":"zhipu","api_keys":["  ","   "]}`)

	require.Equal(t, http.StatusOK, rec.Code)
	require.False(t, svc.called)
	require.Contains(t, rec.Body.String(), `"duplicates":[]`)
}

func TestCheckAPIKeysDuplicateRejectsBlankStringElement(t *testing.T) {
	// 空字符串元素在 binding 阶段即被拒绝
	svc := &checkAPIKeysAdminServiceStub{}
	rec := postCheckAPIKeys(setupCheckAPIKeysRouter(svc), `{"platform":"zhipu","api_keys":["key-a",""]}`)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.False(t, svc.called)
}

func TestCheckAPIKeysDuplicateRejectsOversizedKey(t *testing.T) {
	// 单条密钥超过长度上限：直接 400，且不触发 service 查询
	svc := &checkAPIKeysAdminServiceStub{}
	oversized := strings.Repeat("a", 1025)
	rec := postCheckAPIKeys(setupCheckAPIKeysRouter(svc), `{"platform":"zhipu","api_keys":["`+oversized+`"]}`)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.False(t, svc.called)
	// 拒绝响应不回显超长内容
	require.NotContains(t, rec.Body.String(), "api key too long"+oversized[:50])
}

func TestCheckAPIKeysDuplicateRejectsOversizedBody(t *testing.T) {
	// 请求体超过 64 KiB 上限：JSON 绑定前即被 MaxBytesReader 拦截为 400
	svc := &checkAPIKeysAdminServiceStub{}
	largeKey := strings.Repeat("b", 1024)
	// 66 条 1KB 密钥 + JSON 包装 > 64 KiB
	keys := make([]string, 66)
	for i := range keys {
		keys[i] = largeKey
	}
	body := `{"platform":"zhipu","api_keys":[` + `"`+strings.Join(keys, `","`)+`"` + `]}`
	require.Greater(t, len(body), 64*1024)

	rec := postCheckAPIKeys(setupCheckAPIKeysRouter(svc), body)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.False(t, svc.called)
}

func TestCheckAPIKeysDuplicateSanitizesServiceError(t *testing.T) {
	// 服务层错误的原始文本（可能含数据库细节）不得透传给客户端，只返回 envelope 化的 500
	svc := &checkAPIKeysAdminServiceStub{err: errors.New("pq: connection refused at postgres://user:secret-password@10.0.0.1:5432/db")}
	rec := postCheckAPIKeys(setupCheckAPIKeysRouter(svc), `{"platform":"kimi","api_keys":["key-a"]}`)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.NotContains(t, rec.Body.String(), "secret-password")
	require.NotContains(t, rec.Body.String(), "postgres://")
}
