package project

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/project/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/project/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/project/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(infra.NewPostgresStore).ToInterface(new(domain.Store))
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))
	return m
}
