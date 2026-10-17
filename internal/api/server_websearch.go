package api

import (
	"context"

	log "github.com/sirupsen/logrus"
	"github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/internal/websearch"
)

// refreshWebSearchService 按最新配置创建或更新搜索渠道服务。
//
// 服务在配置启用时创建（幂等），并在每次配置热重载时重建渠道视图。处理器在服务启动时
// 就已创建，这里只替换它绑定的服务实例，因此「启动时未启用、之后通过配置启用」也能
// 立即生效，不需要重启进程。创建失败只记录日志，不影响服务整体启动。
func (s *Server) refreshWebSearchService(cfg *config.Config) {
	if s == nil {
		return
	}

	var service *websearch.Service
	if cfg != nil && cfg.WebSearchEffectiveEnabled() {
		if s.webSearch == nil {
			created, errCreate := websearch.NewService(cfg, s.configFilePath, "")
			if errCreate != nil {
				log.WithError(errCreate).Error("搜索渠道子系统初始化失败，相关接口将不可用")
			} else {
				s.webSearch = created
				log.Info("搜索渠道子系统已启用")
			}
		} else if errUpdate := s.webSearch.UpdateConfig(cfg); errUpdate != nil {
			log.WithError(errUpdate).Error("搜索渠道配置更新失败")
		}
		service = s.webSearch
	} else if s.webSearch != nil {
		// 之前启用过：保留服务对象但视图置空，所有入口按「未启用」处理。
		if errUpdate := s.webSearch.UpdateConfig(cfg); errUpdate != nil {
			log.WithError(errUpdate).Warn("搜索渠道配置更新失败")
		}
		service = s.webSearch
	}

	if s.webSearchHandler != nil {
		s.webSearchHandler.SetService(service)
	}
	if s.mgmt != nil {
		s.mgmt.SetWebSearchService(service)
	}
}

// startWebSearchScheduler 启动搜索渠道定时任务（渠道状态检测 + 额度刷新）。
func (s *Server) startWebSearchScheduler(ctx context.Context) {
	if s == nil || s.webSearch == nil {
		return
	}
	s.webSearch.StartScheduler(ctx)
}
