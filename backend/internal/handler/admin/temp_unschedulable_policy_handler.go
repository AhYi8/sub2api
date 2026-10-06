package admin

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// GetTempUnschedulablePolicy 返回指定平台的临时不可调度策略。
func (h *SettingHandler) GetTempUnschedulablePolicy(c *gin.Context) {
	platform := strings.ToLower(strings.TrimSpace(c.Param("platform")))
	if platform == "" {
		response.BadRequest(c, "平台不能为空")
		return
	}
	policy, err := h.settingService.GetTempUnschedulablePolicy(c.Request.Context(), platform)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"platform": platform, "policy": policy})
}

// UpdateTempUnschedulablePolicy 更新指定平台的临时不可调度策略。
func (h *SettingHandler) UpdateTempUnschedulablePolicy(c *gin.Context) {
	platform := strings.ToLower(strings.TrimSpace(c.Param("platform")))
	if platform == "" {
		response.BadRequest(c, "平台不能为空")
		return
	}
	var policy service.TempUnschedulablePolicy
	if err := c.ShouldBindJSON(&policy); err != nil {
		response.BadRequest(c, "无效的策略参数")
		return
	}
	if err := h.settingService.SetTempUnschedulablePolicy(c.Request.Context(), platform, policy); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	saved, err := h.settingService.GetTempUnschedulablePolicy(c.Request.Context(), platform)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"platform": platform, "policy": saved})
}
