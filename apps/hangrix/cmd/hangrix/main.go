package main

import (
	"log"
	"os"

	"github.com/hangrix/hangrix/apps/hangrix/internal/app"
	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/kv"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/actor"
	agentsession "github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/attachment"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth"
	automation "github.com/hangrix/hangrix/apps/hangrix/internal/modules/automation"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/dashboard"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/git"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/healthz"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/hello"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue"
	issuegate "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue_gate"
	llmprovider "github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider"
	llmproxy "github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_proxy"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/org"
	planengine "github.com/hangrix/hangrix/apps/hangrix/internal/modules/plan_engine"
	platformapi "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api"
	platformsettings "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_settings"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/project"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/questionnaire"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/release"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo"
	reposilence "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo_silence"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/skill"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/token"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/user"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/apps/hangrix/internal/web"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func main() {
	c := ioc.NewContainer()
	c.Provide(func() *ioc.Container { return c }).ToSelf()
	c.Load(
		app.Module(),
		server.Module(),
		database.Module(),
		kv.Module(),
		healthz.Module(),
		attachment.Module(),
		hello.Module(),
		user.Module(),
		auth.Module(),
		token.Module(),
		git.Module(),
		org.Module(),
		repo.Module(),
		release.Module(),
		issue.Module(),
		project.Module(),
		issuegate.Module(),
		skill.Module(),
		reposilence.Module(),
		planengine.Module(),
		questionnaire.Module(),
		llmprovider.Module(),
		llmproxy.Module(),
		runner.Module(),
		actor.Module(),
		agentsession.Module(),
		dashboard.Module(),
		platformapi.Module(),
		platformsettings.Module(),
		automation.Module(),
		workflow.Module(),
		web.Module(),
	)

	a := ioc.Get[*app.App](c)
	if err := a.Run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}
