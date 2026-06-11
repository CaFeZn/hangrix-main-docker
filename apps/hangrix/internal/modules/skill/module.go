package skill

import (
	skilldomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/skill/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/skill/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/skill/service"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(service.NewResolver).ToInterface(new(skilldomain.Resolver))
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))
	return m
}
