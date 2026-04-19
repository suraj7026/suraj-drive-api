package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog/log"

	"surajdrive/backend/internal/config"
	"surajdrive/backend/internal/handler"
	appmiddleware "surajdrive/backend/internal/middleware"
	"surajdrive/backend/internal/storage"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	store, err := storage.NewMinIOClient(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to MinIO")
	}

	authHandler := handler.NewAuthHandler(cfg, store)
	fileHandler := handler.NewFileHandler(store)

	r := chi.NewRouter()
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	r.Use(appmiddleware.CORS(cfg.Server.FrontendURL))

	r.Get("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	r.Route("/api/auth", func(r chi.Router) {
		r.Get("/google/login", authHandler.GoogleLogin)
		r.Get("/google/callback", authHandler.GoogleCallback)
		r.Post("/logout", authHandler.Logout)
		r.With(appmiddleware.RequireAuth(cfg.JWT.Secret)).Get("/me", authHandler.Me)
	})

	r.Route("/api", func(r chi.Router) {
		r.Use(appmiddleware.RequireAuth(cfg.JWT.Secret))

		r.Get("/files", fileHandler.List)
		r.Post("/files/upload", fileHandler.Upload)
		r.Delete("/files", fileHandler.Delete)
		r.Post("/files/copy", fileHandler.Copy)
		r.Get("/files/presign/download", fileHandler.PresignDownload)
		r.Get("/files/presign/upload", fileHandler.PresignUpload)
		r.Post("/folders", fileHandler.CreateFolder)
		r.Delete("/folders", fileHandler.DeleteFolder)
		r.Get("/search", fileHandler.Search)
	})

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:      r,
		ReadTimeout:  time.Duration(cfg.Server.ReadTimeoutSecs) * time.Second,
		WriteTimeout: time.Duration(cfg.Server.WriteTimeoutSecs) * time.Second,
	}

	log.Info().Str("addr", server.Addr).Msg("server starting")
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal().Err(err).Msg("server stopped")
	}
}
