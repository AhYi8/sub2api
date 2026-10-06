package admin

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 手动临时不可调度端点的最小桩仓储：只实现该路径用到的能力。
type setTempUnschedHandlerRepo struct {
	service.AccountRepository
	account       *service.Account
	overrideCalls int
	overrideArg   string
	overrideUntil time.Time
}

func (r *setTempUnschedHandlerRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	if r.account != nil && r.account.ID == id {
		return r.account, nil
	}
	return nil, service.ErrAccountNotFound
}

func (r *setTempUnschedHandlerRepo) SetTempUnschedulableOverride(_ context.Context, id int64, until time.Time, reason string) error {
	r.overrideCalls++
	r.overrideUntil = until
	r.overrideArg = reason
	// 模拟真实仓储：回写到内存账号，供 handler 组装响应
	if r.account != nil && r.account.ID == id {
		r.account.TempUnschedulableUntil = &until
		r.account.TempUnschedulableReason = reason
	}
	return nil
}

func newSetTempUnschedHandlerContext(method, target, body, id string) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, io.NopCloser(bytes.NewBufferString(body)))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = request
	if id != "" {
		ctx.Params = gin.Params{{Key: "id", Value: id}}
	}
	return ctx, recorder
}

func newSetTempUnschedHandler(repo *setTempUnschedHandlerRepo) *AccountHandler {
	svc := service.NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	return &AccountHandler{rateLimitService: svc}
}

func TestSetTempUnschedulableHandlerValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("invalid account id", func(t *testing.T) {
		ctx, recorder := newSetTempUnschedHandlerContext(http.MethodPost, "/admin/accounts/not-an-id/temp-unschedulable", `{"duration_minutes":10}`, "not-an-id")
		newSetTempUnschedHandler(&setTempUnschedHandlerRepo{}).SetTempUnschedulable(ctx)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})

	t.Run("missing duration", func(t *testing.T) {
		ctx, recorder := newSetTempUnschedHandlerContext(http.MethodPost, "/admin/accounts/7/temp-unschedulable", `{}`, "7")
		newSetTempUnschedHandler(&setTempUnschedHandlerRepo{}).SetTempUnschedulable(ctx)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})

	t.Run("duration above cap", func(t *testing.T) {
		ctx, recorder := newSetTempUnschedHandlerContext(http.MethodPost, "/admin/accounts/7/temp-unschedulable", `{"duration_minutes":10081}`, "7")
		newSetTempUnschedHandler(&setTempUnschedHandlerRepo{}).SetTempUnschedulable(ctx)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})

	t.Run("account not found", func(t *testing.T) {
		repo := &setTempUnschedHandlerRepo{}
		ctx, recorder := newSetTempUnschedHandlerContext(http.MethodPost, "/admin/accounts/7/temp-unschedulable", `{"duration_minutes":10}`, "7")
		newSetTempUnschedHandler(repo).SetTempUnschedulable(ctx)
		require.Equal(t, http.StatusNotFound, recorder.Code)
		require.Equal(t, 0, repo.overrideCalls)
	})

	t.Run("valid request stores manual mark", func(t *testing.T) {
		repo := &setTempUnschedHandlerRepo{account: &service.Account{ID: 7, Status: service.StatusActive}}
		ctx, recorder := newSetTempUnschedHandlerContext(http.MethodPost, "/admin/accounts/7/temp-unschedulable", `{"duration_minutes":30,"reason":"维护"}`, "7")
		newSetTempUnschedHandler(repo).SetTempUnschedulable(ctx)
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Equal(t, 1, repo.overrideCalls)
		require.Equal(t, "manual:维护", repo.overrideArg)
		require.WithinDuration(t, time.Now().Add(30*time.Minute), repo.overrideUntil, 2*time.Minute)
		require.Contains(t, recorder.Body.String(), `"temp_unschedulable_reason":"manual:维护"`)
	})
}
