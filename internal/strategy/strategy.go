package strategy

import (
	"context"

	"github.com/vagustrader/vagustrader/internal/cfg"
	"github.com/vagustrader/vagustrader/internal/client/clob"
	"github.com/vagustrader/vagustrader/internal/client/dataapi"
	"github.com/vagustrader/vagustrader/internal/client/gamma"
	"github.com/vagustrader/vagustrader/internal/db"
	"github.com/vagustrader/vagustrader/internal/notify"
)

type Context struct {
	Clob     clob.Client
	Gamma    *gamma.Client
	DataAPI  *dataapi.Client
	Config   *cfg.AppConfig
	DB       *db.DB
	Paper    bool
	Notifier *notify.Notifier
	RPCURL   string
}

type Strategy interface {
	Name() string
	Run(ctx context.Context, sctx *Context) error
}
