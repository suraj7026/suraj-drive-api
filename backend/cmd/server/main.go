package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog/log"

	"surajdrive/backend/internal/config"
	"surajdrive/backend/internal/database"
	"surajdrive/backend/internal/handler"
	"surajdrive/backend/internal/importer"
	appmiddleware "surajdrive/backend/internal/middleware"
	"surajdrive/backend/internal/repository"
	"surajdrive/backend/internal/storage"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}
	db, err := database.Open(context.Background(), database.PoolConfig{
		URL:              cfg.Database.URL,
		MaxConnections:   int32(cfg.Database.MaxConnections),
		MinConnections:   int32(cfg.Database.MinConnections),
		HealthTimeout:    time.Duration(cfg.Database.HealthTimeoutSecs) * time.Second,
		MaxConnectionAge: time.Duration(cfg.Database.MaxConnectionAgeMins) * time.Minute,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to PostgreSQL")
	}
	defer db.Close()
	metadata := repository.NewMetadata(db)

	store, err := storage.NewMinIOClient(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to MinIO")
	}

	legacyImporter := importer.NewLegacy(metadata, store)
	authHandler := handler.NewAuthHandler(cfg, store, metadata, legacyImporter)
	fileHandler := handler.NewFileHandler(store, metadata)
	metadataHandler := handler.NewMetadataHandler(legacyImporter)

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
	r.Get("/api/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(cfg.Database.HealthTimeoutSecs)*time.Second)
		defer cancel()
		var metadataProbe int
		if err := db.QueryRow(ctx, "SELECT count(*) FROM drive.user_account WHERE false").Scan(&metadataProbe); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})

	r.Route("/api/auth", func(r chi.Router) {
		r.Get("/google/login", authHandler.GoogleLogin)
		r.Get("/google/callback", authHandler.GoogleCallback)
		r.With(appmiddleware.RequireAuth(cfg.JWT.Secret, metadata)).Post("/logout", authHandler.Logout)
		r.With(appmiddleware.RequireAuth(cfg.JWT.Secret, metadata)).Get("/me", authHandler.Me)
	})

	r.Route("/api", func(r chi.Router) {
		r.Use(appmiddleware.RequireAuth(cfg.JWT.Secret, metadata))

		r.Get("/files", fileHandler.List)
		r.Post("/files/upload", fileHandler.Upload)
		r.Post("/files/upload/complete", fileHandler.CompletePresignedUpload)
		r.Delete("/files", fileHandler.Delete)
		r.Post("/files/copy", fileHandler.Copy)
		r.Get("/files/presign/download", fileHandler.PresignDownload)
		r.Get("/files/presign/upload", fileHandler.PresignUpload)
		r.Get("/files/preview", fileHandler.Preview)
		r.Post("/folders", fileHandler.CreateFolder)
		r.Delete("/folders", fileHandler.DeleteFolder)
		r.Get("/search", fileHandler.Search)
		r.Post("/metadata/reconcile", metadataHandler.ReconcileLegacy)
		r.Post("/items/{itemID}/trash", fileHandler.TrashItem)
		r.Post("/items/{itemID}/restore", fileHandler.RestoreItem)
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
