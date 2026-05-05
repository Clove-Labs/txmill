package api

import (
	"github.com/clove-labs/txmill/internal/app"
	"github.com/clove-labs/txmill/internal/relay"
	"github.com/clove-labs/txmill/internal/treasury"
)

type Handlers struct {
	Apps     *app.Service
	Relay    *relay.Service
	Chain    ChainBalances
	Treasury *treasury.Service
}
