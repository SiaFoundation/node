package api

import (
	"net/http"

	"go.sia.tech/core/types"
	"go.sia.tech/jape"
)

// ChainManager provides an interface for accessing chain information.
type ChainManager interface {
	Tip() types.ChainIndex
}

type server struct {
	chain ChainManager
}

func (s *server) handleGetConsensusTip(jc jape.Context) {
	jc.Encode(s.chain.Tip())
}

// NewHandler returns a new HTTP handler for the API.
func NewHandler(cm ChainManager) http.Handler {
	s := &server{
		chain: cm,
	}
	return jape.Mux(map[string]jape.Handler{
		"GET /consensus/tip": s.handleGetConsensusTip,
	})
}
