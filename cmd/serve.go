package cmd

import (
	"github.com/spf13/cobra"

	"github.com/ivan/gligabao/internal/ai"
	"github.com/ivan/gligabao/internal/config"
	"github.com/ivan/gligabao/internal/server"
)

var (
	serveDBPath       string
	serveVectorDBPath string
)

//---------------------------------------------------------------------------------------------

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the HTTP server",
	RunE: func(cmd *cobra.Command, args []string) error {
		return server.Run(cmd.Context(), server.Options{
			Addr:                cfg.Addr(),
			DBBackend:           cfg.DBBackend,
			DBPath:              serveDBPath,
			PostgresDSN:         cfg.PostgresDSN,
			AnthropicKey:        cfg.AnthropicKey,
			VectorStore:         cfg.VectorStore,
			VectorDBPath:        serveVectorDBPath,
			EmbeddingDimensions: cfg.EmbeddingDimensions,
			PineconeKey:         cfg.PineconeKey,
			PineconeIndexHost:   cfg.PineconeIndexHost,
			Prices:              aiPrices(cfg),
		})
	},
}

//---------------------------------------------------------------------------------------------

// aiPrices converts the configured prices; omitted cache prices default to the input price.
func aiPrices(c *config.Config) map[string]ai.Price {
	out := make(map[string]ai.Price, len(c.Prices))
	for model, p := range c.Prices {
		ap := ai.Price{Input: p.Input, Output: p.Output, CacheWrite: p.Input, CacheRead: p.Input}
		if p.CacheWrite != nil {
			ap.CacheWrite = *p.CacheWrite
		}
		if p.CacheRead != nil {
			ap.CacheRead = *p.CacheRead
		}
		out[model] = ap
	}
	return out
}

//---------------------------------------------------------------------------------------------

func init() {
	serveCmd.Flags().StringVar(&serveDBPath, "db", "bin/gligabao.db", "SQLite database file path")
	serveCmd.Flags().StringVar(&serveVectorDBPath, "vector-db", "", "sqlite-vec database file path (default: next to --db)")
	rootCmd.AddCommand(serveCmd)
}
